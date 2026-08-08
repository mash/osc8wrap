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
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

var defaultExcludeDirs = []string{"vendor", "node_modules", ".git", "__pycache__", ".cache"}

const usage = `Usage: osc8wrap [options] <command> [args...]
       <other command> | osc8wrap [options]
       osc8wrap sessions [--format FMT] [query...]
       osc8wrap open <id> [--print]
       osc8wrap preview <id> [query...]

Subcommands:
  sessions                List every Claude Code session on this machine, the
                          ones running under osc8wrap and the finished ones,
                          as records for a fuzzy picker such as fzf.
                          Recognized only as the first non-flag argument, so
                          "osc8wrap -- sessions" still wraps a command that is
                          itself named "sessions".
                          See "osc8wrap sessions --help".
  open <id>               Open one of those sessions: jump to its window if it
                          is still running, or resume it with "claude --resume"
                          if it has finished. <id> is field 2 of a "sessions"
                          record. See "osc8wrap open --help".
  preview <id> [query...] Render one session for a picker's preview pane.
                          See "osc8wrap preview --help".

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
  --no-symbol-links       Disable symbol linking (default: enabled when scheme != file)
                          Can also be set via OSC8WRAP_NO_SYMBOL_LINKS=1
  --debug-writes          Log each Write call to a temp file (path printed to stderr)

Examples:
  osc8wrap go build ./...
  osc8wrap --scheme=cursor grep -rn "TODO" .
  grep -rn "TODO" . | osc8wrap
  osc8wrap sessions
`

func main() {
	os.Exit(run())
}

func run() int {
	opts, cmdArgs, sawDoubleDash := parseArgs(os.Args[1:])

	if runCommand, ok := lookupSubcommand(cmdArgs, sawDoubleDash); ok {
		return runCommand(cmdArgs[1:])
	}

	hostname, _ := os.Hostname()
	cwd, _ := os.Getwd()

	opts.Output = os.Stdout
	opts.Cwd = cwd
	opts.Hostname = hostname

	linker := NewLinker(opts)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go linker.StartIndexer(ctx)

	if len(cmdArgs) == 0 {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprint(os.Stderr, usage)
			return 1
		}
		if err := runPipeMode(linker); err != nil {
			fmt.Fprintf(os.Stderr, "osc8wrap: %v\n", err)
			return 1
		}
		return 0
	}
	exitCode, err := runPTYMode(linker, cmdArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "osc8wrap: %v\n", err)
	}
	return exitCode
}

// subcommands maps a subcommand name to its implementation, which receives the
// arguments after the name and returns the process exit code. Every name here
// must also appear in usage; TestSubcommandsAreDocumented enforces that.
var subcommands = map[string]func([]string) int{
	"sessions": runSessionsCommand,
	"open":     runOpenCommand,
	"preview":  runPreviewCommand,
}

// lookupSubcommand reports whether the boundary parseArgs found starts a
// subcommand rather than a command to wrap, and returns its implementation.
// Reusing parseArgs' boundary is what keeps the two from disagreeing: a
// mistyped option before a subcommand still produces parseArgs' "unknown
// option" error rather than being silently dropped.
//
// An explicit "--" always introduces a command, so "osc8wrap -- sessions"
// still wraps a program that is itself named "sessions".
func lookupSubcommand(cmdArgs []string, sawDoubleDash bool) (func([]string) int, bool) {
	if sawDoubleDash || len(cmdArgs) == 0 {
		return nil, false
	}
	run, ok := subcommands[cmdArgs[0]]
	return run, ok
}

func parseArgs(args []string) (opts LinkerOptions, cmdArgs []string, sawDoubleDash bool) {
	opts.Scheme = os.Getenv("OSC8WRAP_SCHEME")
	opts.Terminator = os.Getenv("OSC8WRAP_TERMINATOR")
	opts.Domains = []string{"github.com"}
	if env := os.Getenv("OSC8WRAP_DOMAINS"); env != "" {
		opts.Domains = splitComma(env)
	}
	opts.ResolveBasename = os.Getenv("OSC8WRAP_NO_RESOLVE_BASENAME") != "1"
	opts.ExcludeDirs = defaultExcludeDirs
	if env := os.Getenv("OSC8WRAP_EXCLUDE_DIRS"); env != "" {
		opts.ExcludeDirs = splitComma(env)
	}
	noSymbolLinks := os.Getenv("OSC8WRAP_NO_SYMBOL_LINKS") == "1"

	for i, arg := range args {
		if v, ok := strings.CutPrefix(arg, "--scheme="); ok {
			opts.Scheme = v
		} else if v, ok := strings.CutPrefix(arg, "--terminator="); ok {
			opts.Terminator = v
		} else if v, ok := strings.CutPrefix(arg, "--domains="); ok {
			opts.Domains = splitComma(v)
		} else if arg == "--no-resolve-basename" {
			opts.ResolveBasename = false
		} else if v, ok := strings.CutPrefix(arg, "--exclude-dir="); ok {
			opts.ExcludeDirs = splitComma(v)
		} else if arg == "--no-symbol-links" {
			noSymbolLinks = true
		} else if arg == "--debug-writes" {
			opts.DebugWrites = true
		} else if arg == "--" {
			cmdArgs = args[i+1:]
			sawDoubleDash = true
			break
		} else if strings.HasPrefix(arg, "-") {
			fmt.Fprintf(os.Stderr, "unknown option: %s\n", arg)
			fmt.Fprint(os.Stderr, usage)
			os.Exit(1)
		} else {
			cmdArgs = args[i:]
			break
		}
	}

	scheme := opts.Scheme
	if scheme == "" {
		scheme = "file"
	}
	opts.SymbolLinks = scheme != "file" && !noSymbolLinks

	return
}

func splitComma(s string) []string {
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

func runPipeMode(linker *Linker) error {
	if _, err := io.Copy(linker, os.Stdin); err != nil {
		return err
	}
	return linker.Flush()
}

func runPTYMode(linker *Linker, cmdArgs []string) (int, error) {
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return 1, fmt.Errorf("failed to start pty: %w", err)
	}
	defer ptmx.Close() //nolint:errcheck

	// Publishing the session is a convenience for `osc8wrap sessions`; it must
	// never keep the wrapped command from running, so errors are dropped.
	unregister := registerCurrentSession(cmd, cmdArgs)
	defer unregister()

	handleResize(ptmx)
	forwardSignals(cmd)

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err == nil {
		defer func() {
			_, _ = os.Stdout.WriteString("\033[0m")
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
		}()
	}

	go func() { _, _ = io.Copy(ptmx, os.Stdin) }()

	if _, err := io.Copy(linker, ptmx); err != nil {
		return 1, err
	}

	if err := linker.Flush(); err != nil {
		return 1, err
	}

	_ = cmd.Wait()

	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode(), nil
	}
	return 0, nil
}

// registerCurrentSession records this wrapper in the session registry and
// returns the (idempotent) cleanup that removes it again. Every input is best
// effort: a session with no tty or no Ghostty surface is still worth listing,
// and a registry that cannot be written at all costs the user nothing but the
// `sessions` listing.
func registerCurrentSession(cmd *exec.Cmd, cmdArgs []string) func() {
	if cmd.Process == nil {
		return func() {}
	}
	// os.Stdout, not the pty we just created: the registry records the
	// terminal emulator's own device, which is what a navigator can address.
	tty, _ := ttyPath(os.Stdout)
	cwd, _ := os.Getwd()

	cleanup, _ := registerSession(RegistryEntry{
		WrapperPID: os.Getpid(),
		ChildPID:   cmd.Process.Pid,
		TTY:        tty,
		SurfaceID:  ghosttySurfaceID(),
		Cwd:        cwd,
		Command:    cmdArgs,
		StartedAt:  time.Now(),
	})
	return cleanup
}

func handleResize(ptmx *os.File) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)

	go func() {
		for range ch {
			_ = pty.InheritSize(os.Stdin, ptmx)
		}
	}()

	ch <- syscall.SIGWINCH
}

// forwardSignals relays SIGINT and SIGTERM to the wrapped command. Installing
// a handler disables the default terminate action, so the wrapper survives the
// signal and keeps wrapping: the registry entry must stay, and it is the
// deferred unregister on the way out (plus loadRegistry's pruning of dead
// wrapper pids) that removes it. Unregistering here would erase a live session
// from `osc8wrap sessions` for the rest of its life, since nothing
// re-registers.
func forwardSignals(cmd *exec.Cmd) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		for sig := range ch {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(sig)
			}
		}
	}()
}
