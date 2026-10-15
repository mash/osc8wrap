package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Installing a signal handler disables the default terminate action, so a
// wrapper that is signalled keeps running and keeps wrapping. Its registry
// entry has to survive with it: nothing ever re-registers, so removing it here
// would hide a live session from `osc8wrap sessions` for the rest of its life.
func TestForwardSignalsKeepsTheRegistryEntry(t *testing.T) {
	dir := useTempRegistry(t)

	cleanup, err := registerSession(RegistryEntry{WrapperPID: os.Getpid(), StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	path := filepath.Join(dir, strconv.Itoa(os.Getpid())+".json")

	// A child that traps the signal instead of dying, like claude cancelling
	// its turn or an interactive shell ignoring SIGINT. It announces itself
	// once the trap is installed: until then the default action still applies
	// and a signal forwarded straight after Start would simply kill it.
	cmd := exec.Command("/bin/sh", "-c", "trap 'echo caught' INT; echo ready; while :; do sleep 0.05; done")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// One reader for both lines: a second one would drop whatever the first
	// buffered past the newline.
	out := bufio.NewReader(stdout)
	if line, err := out.ReadString('\n'); err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("child never became ready: line %q, err %v", line, err)
	}

	forwardSignals(cmd)
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}

	// The child reporting the signal proves the handler ran; the entry must
	// still be there once it has.
	line, err := out.ReadString('\n')
	if err != nil {
		t.Fatalf("child never saw the forwarded signal: %v", err)
	}
	if got := strings.TrimSpace(line); got != "caught" {
		t.Fatalf("child printed %q, want %q", got, "caught")
	}
	time.Sleep(100 * time.Millisecond)

	if _, err := os.Stat(path); err != nil {
		t.Errorf("registry entry gone although the wrapper survived the signal: %v", err)
	}
}

// A subcommand nobody can discover may as well not exist, and the table is
// meant to grow, so the usage text has to grow with it.
func TestSubcommandsAreDocumented(t *testing.T) {
	for name := range subcommands {
		if !strings.Contains(usage, name) {
			t.Errorf("subcommand %q is missing from the usage text", name)
		}
	}
}

// Every option osc8wrap documents must be consumed as an option rather than
// mistaken for the start of the wrapped command, or dispatch would read the
// wrong argument as a subcommand name.
func TestParseArgsConsumesEveryOption(t *testing.T) {
	for _, arg := range []string{
		"--scheme=cursor", "--terminator=bel", "--domains=github.com,example.com",
		"--exclude-dir=vendor", "--no-resolve-basename", "--no-symbol-links",
		"--debug-writes", "--github-org=example",
	} {
		args := []string{arg, "echo", "hi"}
		_, cmdArgs, sawDoubleDash := parseArgs(args)
		if len(cmdArgs) != 2 || cmdArgs[0] != "echo" {
			t.Errorf("parseArgs(%q) = %q, want the command to start at %q", args, cmdArgs, "echo")
		}
		if sawDoubleDash {
			t.Errorf("parseArgs(%q) reported a double dash that is not there", args)
		}
	}
}

func TestParseArgsMaxWatches(t *testing.T) {
	tests := []struct {
		env  string
		want int
	}{
		{"50000", 50000},
		// Anything that is not a positive number leaves MaxWatches at zero, which
		// NewFileIndex reads as the default. A typo must not lift the cap.
		{"", 0},
		{"lots", 0},
		{"0", 0},
		{"-1", 0},
	}

	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			t.Setenv("OSC8WRAP_MAX_WATCHES", tt.env)
			opts, _, _ := parseArgs([]string{"echo", "hi"})
			if opts.MaxWatches != tt.want {
				t.Errorf("OSC8WRAP_MAX_WATCHES=%q gave MaxWatches %d, want %d", tt.env, opts.MaxWatches, tt.want)
			}
		})
	}
}

func TestParseArgsGitHubOrg(t *testing.T) {
	t.Setenv("OSC8WRAP_GITHUB_ORG", "example")
	for _, tt := range []struct {
		args []string
		want string
	}{
		{nil, "example"},
		{[]string{"--github-org=other"}, "other"},
		{[]string{"--github-org="}, ""},
	} {
		opts, _, _ := parseArgs(tt.args)
		if opts.GitHubOrg != tt.want {
			t.Errorf("parseArgs(%q).GitHubOrg = %q, want %q", tt.args, opts.GitHubOrg, tt.want)
		}
	}
}
