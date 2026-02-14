package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"golang.org/x/term"
)

const usage = `Usage: osc8wrap-replay [options] <debug-log-file>

Replay osc8wrap --debug-writes logs one write at a time.
Press Enter to advance to the next write chunk.

Options:
  --file PATH           Path to debug log file (alternative to positional arg)
  --stream MODE         Stream to replay: output, input (default: output)

Examples:
  osc8wrap-replay --file /tmp/osc8wrap-debug-foo-20260214-110857.log
  osc8wrap-replay --stream=input /tmp/osc8wrap-debug-foo-20260214-110857.log
  osc8wrap-replay --stream=output /tmp/osc8wrap-debug-foo-20260214-110857.log
`

func main() {
	os.Exit(run())
}

func run() int {
	var filePath string
	var stream string

	fs := flag.NewFlagSet("osc8wrap-replay", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprint(os.Stderr, usage)
	}
	fs.StringVar(&filePath, "file", "", "Path to debug log file")
	fs.StringVar(&stream, "stream", string(StreamOutput), "Replay stream: output, input")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}

	if filePath != "" && fs.NArg() > 0 {
		_, _ = fmt.Fprintf(os.Stderr, "osc8wrap-replay: unexpected positional arguments: %s\n\n", strings.Join(fs.Args(), " "))
		fs.Usage()
		return 2
	}

	if filePath == "" {
		if fs.NArg() != 1 {
			_, _ = fmt.Fprintln(os.Stderr, "osc8wrap-replay: missing debug log file path")
			_, _ = fmt.Fprintln(os.Stderr)
			fs.Usage()
			return 2
		}
		filePath = fs.Arg(0)
	}

	mode, err := ParseStreamMode(stream)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "osc8wrap-replay: %v\n", err)
		return 2
	}

	f, err := os.Open(filePath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "osc8wrap-replay: open %s: %v\n", filePath, err)
		return 1
	}
	defer f.Close() //nolint:errcheck

	records, err := ParseDebugLog(f)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "osc8wrap-replay: parse %s: %v\n", filePath, err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "osc8wrap-replay: make raw stdin: %v\n", err)
		return 1
	}
	defer func() {
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
	}()

	_, _ = fmt.Fprintf(os.Stderr, "osc8wrap-replay: replaying %s. Press enter to proceed one step\n", filePath)

	opts := ReplayOptions{
		Mode: mode,
	}
	defer resetTerminal(os.Stdout)

	if err := ReplayWrites(ctx, records, os.Stdin, os.Stdout, opts); err != nil {
		if errors.Is(err, errInterrupted) {
			return 130
		}
		_, _ = fmt.Fprintf(os.Stderr, "osc8wrap-replay: %v\n", err)
		return 1
	}

	return 0
}

func resetTerminal(w io.Writer) {
	_, _ = w.Write([]byte(strings.Join([]string{
		"\x1b[<u",     // pop keyboard mode (Kitty protocol)
		"\x1b[?1004l", // disable focus reporting
		"\x1b[?2004l", // disable bracketed paste
		"\x1b[?2026l", // disable synchronized output
		"\x1b[r",      // reset scroll region
		"\x1b[0m",     // reset SGR attributes
		"\x1b[?25h",   // show cursor
	}, "")))
}
