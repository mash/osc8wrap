package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

var defaultExcludeDirs = []string{"vendor", "node_modules", ".git", "__pycache__", ".cache"}

const usage = `Usage: osc8wrap [options] <command> [args...]
       <other command> | osc8wrap [options]

Options:
  --scheme=NAME           URL scheme for file links (default: file)
                          Can also be set via OSC8WRAP_SCHEME env var
                          Examples: file, vscode, cursor, zed
  --terminator=TYPE       OSC8 string terminator (default: st)
                          Can also be set via OSC8WRAP_TERMINATOR env var
                          st: ESC \ (ECMA-48 standard)
                          bel: BEL 0x07 (legacy xterm)
  --domains=LIST          Comma-separated domains to linkify without https://
                          (default: github.com, env: OSC8WRAP_DOMAINS)
  --no-resolve-basename   Disable basename resolution (default: enabled)
                          Can also be set via OSC8WRAP_NO_RESOLVE_BASENAME=1
  --exclude-dir=DIR,...   Directories to exclude from search (replaces defaults)
                          Default: vendor,node_modules,.git,__pycache__,.cache
                          Can also be set via OSC8WRAP_EXCLUDE_DIRS

Examples:
  osc8wrap go build ./...
  osc8wrap --scheme=cursor grep -rn "TODO" .
  grep -rn "TODO" . | osc8wrap
`

func main() {
	os.Exit(run())
}

func run() int {
	opts, cmdArgs := parseArgs(os.Args[1:])

	hostname, _ := os.Hostname()
	cwd, _ := os.Getwd()

	opts.Output = os.Stdout
	opts.Cwd = cwd
	opts.Hostname = hostname

	linker := NewLinkerWithOptions(opts)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go linker.StartIndexer(ctx)

	if len(cmdArgs) == 0 {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprint(os.Stderr, usage)
			return 1
		}
		return runPipeMode(linker)
	}
	return runPTYMode(linker, cmdArgs)
}

func parseArgs(args []string) (opts LinkerOptions, cmdArgs []string) {
	opts.Scheme = os.Getenv("OSC8WRAP_SCHEME")
	opts.Terminator = os.Getenv("OSC8WRAP_TERMINATOR")
	opts.Domains = []string{"github.com"}
	if env := os.Getenv("OSC8WRAP_DOMAINS"); env != "" {
		opts.Domains = splitDomains(env)
	}
	opts.ResolveBasename = os.Getenv("OSC8WRAP_NO_RESOLVE_BASENAME") != "1"
	opts.ExcludeDirs = defaultExcludeDirs
	if env := os.Getenv("OSC8WRAP_EXCLUDE_DIRS"); env != "" {
		opts.ExcludeDirs = splitDomains(env)
	}

	for i, arg := range args {
		if v, ok := strings.CutPrefix(arg, "--scheme="); ok {
			opts.Scheme = v
		} else if v, ok := strings.CutPrefix(arg, "--terminator="); ok {
			opts.Terminator = v
		} else if v, ok := strings.CutPrefix(arg, "--domains="); ok {
			opts.Domains = splitDomains(v)
		} else if arg == "--no-resolve-basename" {
			opts.ResolveBasename = false
		} else if v, ok := strings.CutPrefix(arg, "--exclude-dir="); ok {
			opts.ExcludeDirs = splitDomains(v)
		} else if arg == "--" {
			cmdArgs = args[i+1:]
			return
		} else if strings.HasPrefix(arg, "-") {
			fmt.Fprintf(os.Stderr, "unknown option: %s\n", arg)
			fmt.Fprint(os.Stderr, usage)
			os.Exit(1)
		} else {
			cmdArgs = args[i:]
			return
		}
	}
	return
}

func splitDomains(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			result = append(result, t)
		}
	}
	return result
}

func runPipeMode(linker *Linker) int {
	io.Copy(linker, os.Stdin)
	return 0
}

func runPTYMode(linker *Linker, cmdArgs []string) int {
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start pty: %v\n", err)
		return 1
	}
	defer ptmx.Close()

	handleResize(ptmx)
	forwardSignals(cmd)

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err == nil {
		defer func() {
			os.Stdout.WriteString("\033[0m")
			term.Restore(int(os.Stdin.Fd()), oldState)
		}()
	}

	go io.Copy(ptmx, os.Stdin)

	io.Copy(linker, ptmx)

	linker.Flush()

	cmd.Wait()

	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	return 0
}

func handleResize(ptmx *os.File) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)

	go func() {
		for range ch {
			pty.InheritSize(os.Stdin, ptmx)
		}
	}()

	ch <- syscall.SIGWINCH
}

func forwardSignals(cmd *exec.Cmd) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		for sig := range ch {
			if cmd.Process != nil {
				cmd.Process.Signal(sig)
			}
		}
	}()
}
