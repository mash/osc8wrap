package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const sessionsUsage = `Usage: osc8wrap sessions [--format FMT] [query...]

Lists every Claude Code session on this machine -- the ones running under
osc8wrap right now and the ones that have finished -- one record per line,
with the sessions waiting on you first. Pipe it into fzf; see the README.

Query:
  Every argument that is not an option is part of the query, joined with
  spaces, so "sessions raw sql" and "sessions 'raw sql'" are one search either
  way. Only the sessions matching every whitespace-separated term are listed.
  A term matches either the conversation -- the message text of the user and
  assistant turns, not tool output and not the files they touched -- or where
  the session lives: its cwd, repository, worktree and branch. Different terms
  may match different halves, so "sessions osc8wrap ratchet" finds the session
  in that repo where ratchets came up. The tag words (done, wait, busy, live)
  are not searched. A record that hit in the conversation gains a .Match
  excerpt saying where.

  No query, or a query that is nothing but whitespace, filters nothing and
  lists everything. That is what lets a picker bind this to every keystroke
  and pass its query straight through, the empty one included.

Options:
  --format FMT   Output format: a preset (fzf, json) or a Go text/template
                 (default: fzf). Both "--format x" and "--format=x" work.
  --help         Show this message
  --             End of options: every argument after it is query text, which
                 is how a term beginning with "-" is searched for.

Template fields:
  .ID .Cwd .Tag .Live .Status .WaitingFor .Repo .Worktree .Branch .Root
  .Prompt .Summary .Name .Date .PID .Command .Match

A custom template supplies its own record terminator, e.g. {{"\n"}}.

The fzf preset emits three tab-separated fields per record -- display, id,
cwd -- so a picker can show field 1 and act on fields 2 and 3.
`

// sessionsOptions is the parsed form of the arguments after "sessions".
type sessionsOptions struct {
	format string
	// query is every non-option argument joined with a space, still unsplit.
	// Joining and letting parseSearchQuery split again is what makes
	// "sessions raw sql" and "sessions 'raw sql'" one query rather than two
	// rules; a string rather than a slice also keeps this struct comparable,
	// which is how the parser's table test states its expectations.
	query string
	help  bool
}

func parseSessionsArgs(args []string) (sessionsOptions, error) {
	var opts sessionsOptions
	var words []string
	// The same convention as main.go's parseArgs, and for the same reason: a
	// query term may begin with "-", and "--" is the only way to say that the
	// word after it is text rather than a mistyped option.
	sawDoubleDash := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case sawDoubleDash:
			words = append(words, arg)
		case arg == "--":
			sawDoubleDash = true
		case arg == "--help", arg == "-h":
			opts.help = true
		case arg == "--format" || strings.HasPrefix(arg, "--format="):
			v, ok := flagValue(args, &i, "--format")
			if !ok {
				return opts, fmt.Errorf("--format needs a value")
			}
			opts.format = v
		case strings.HasPrefix(arg, "-"):
			// Not folded into the query. A mistyped flag looks exactly like a
			// term nothing matches, and answering "--fromat json" with a
			// confident empty listing is the one reply the user cannot tell
			// from having no such sessions.
			return opts, fmt.Errorf("unknown option: %s", arg)
		default:
			words = append(words, arg)
		}
	}
	opts.query = strings.Join(words, " ")
	return opts, nil
}

// flagValue reads the value of name, accepting both "--name value" and
// "--name=value" and advancing i past a consumed value. Taking both spellings
// is deliberate: osc8wrap's own option table is "="-only while ccsearch, whose
// record shape these subcommands copy, is space-only, so users arrive expecting
// either and being right only half the time is worse than accepting both.
//
// Accepting both means judging both the same way: an empty value is rejected
// whether it was written as "--format=" or as an empty second word. Letting
// the latter
// through would silently fall back to the default preset, which is the one
// outcome a user who typed an explicit value did not ask for.
func flagValue(args []string, i *int, name string) (string, bool) {
	if v, ok := strings.CutPrefix(args[*i], name+"="); ok {
		return v, v != ""
	}
	if *i+1 >= len(args) {
		return "", false
	}
	*i++
	return args[*i], args[*i] != ""
}

// runSessionsCommand implements `osc8wrap sessions`. It returns the process
// exit code.
func runSessionsCommand(args []string) int {
	return runSessions(args, os.Stdout, os.Stderr)
}

// printMessage writes a diagnostic or the usage text. The error is dropped
// deliberately and in one place: there is nothing left to do when the stream a
// failure would be reported on is the stream that failed.
func printMessage(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...) //nolint:errcheck // nowhere to report a failed report
}

// runSessions is runSessionsCommand with its streams injected, so a test can
// hold it to the invariant that matters most here: a --format that does not
// resolve leaves stdout untouched.
func runSessions(args []string, stdout, stderr io.Writer) int {
	opts, err := parseSessionsArgs(args)
	if err != nil {
		printMessage(stderr, "osc8wrap sessions: %v\n", err)
		printMessage(stderr, "%s", sessionsUsage)
		return 1
	}
	if opts.help {
		printMessage(stdout, "%s", sessionsUsage)
		return 0
	}

	// Before the listing is even read, let alone written: a bad format has to
	// fail cleanly rather than emit the rows it managed before hitting one it
	// could not render.
	format, err := resolveFormat(opts.format)
	if err != nil {
		printMessage(stderr, "osc8wrap sessions: %v\n", err)
		return 1
	}

	// Like the format, split before the listing is read rather than while it is
	// being written. The one way this can come out empty -- no terms in it, an
	// empty query or one that is all whitespace -- is not a failure but the
	// unfiltered listing, which is what a picker sends before the first
	// keystroke and what the zero searchQuery already means.
	query, _ := parseSearchQuery(opts.query)

	sessions, err := listSessionsMatching(query)
	if err != nil {
		printMessage(stderr, "osc8wrap sessions: %v\n", err)
		return 1
	}
	// The format is written either way, including as zero bytes: whatever is
	// reading stdout asked for a listing and gets one, empty or not.
	if err := format(stdout, sessions); err != nil {
		printMessage(stderr, "osc8wrap sessions: %v\n", err)
		return 1
	}
	return 0
}
