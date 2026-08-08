package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const openUsage = `Usage: osc8wrap open <id> [--print]

Opens one Claude Code session. <id> is field 2 of an "osc8wrap sessions"
record: the claude session UUID, or the wrapper pid for a live session that
has no UUID yet.

A session that is still running is brought to the front in Ghostty. A finished
one is resumed with "claude --resume", run in the directory it started in --
the only directory claude can find it from.

Options:
  --print   Print the "claude --resume" command for <id> and run nothing
  --help    Show this message
`

// accessibilityHint is the one piece of setup a user cannot guess from the
// failure: macOS attributes the Accessibility permission to the application
// owning the process tree, so it is Ghostty that must be enabled and never the
// osc8wrap binary, which macOS does not even offer as a choice.
const accessibilityHint = `Jumping to a session drives macOS Accessibility, and macOS grants that to the
terminal application osc8wrap runs under. Enable Ghostty under System Settings
> Privacy & Security > Accessibility.
`

// summaryWidth is how much of a summary an ambiguity error quotes. Long enough
// to tell two sessions apart, short enough that a dozen candidates still read
// as a list.
const summaryWidth = 60

// openOptions is the parsed form of the arguments after "open".
type openOptions struct {
	id    string
	print bool
	help  bool
}

func parseOpenArgs(args []string) (openOptions, error) {
	var opts openOptions
	for _, arg := range args {
		switch {
		case arg == "--print":
			opts.print = true
		case arg == "--help", arg == "-h":
			opts.help = true
		case strings.HasPrefix(arg, "-"):
			return opts, fmt.Errorf("unknown option: %s", arg)
		case opts.id != "":
			return opts, fmt.Errorf("open takes one session id, and %q is a second", arg)
		default:
			opts.id = arg
		}
	}
	return opts, nil
}

// runOpenCommand implements `osc8wrap open`. It returns the process exit code.
func runOpenCommand(args []string) int {
	return runOpen(args, os.Stdout, os.Stderr)
}

// runOpen is runOpenCommand with its streams injected, so a test can read what
// --print emitted without a real claude and without moving window focus.
func runOpen(args []string, stdout, stderr io.Writer) int {
	opts, err := parseOpenArgs(args)
	if err != nil {
		printMessage(stderr, "osc8wrap open: %v\n", err)
		printMessage(stderr, "%s", openUsage)
		return 1
	}
	if opts.help {
		printMessage(stdout, "%s", openUsage)
		return 0
	}

	// candidatesFor is the narrow lookup rather than the full listing: `open`
	// and `preview` share it so that neither pays for a scan of every
	// transcript on the machine to act on one id.
	candidates, err := candidatesFor(opts.id)
	if err != nil {
		printMessage(stderr, "osc8wrap open: %v\n", err)
		return 1
	}
	session, err := resolveSession(candidates, opts.id)
	if err != nil {
		printMessage(stderr, "osc8wrap open: %v\n", err)
		return 1
	}

	if opts.print {
		if reason := resumeRefusal(session); reason != "" {
			printMessage(stderr, "osc8wrap open: %s\n", reason)
			return 1
		}
		printMessage(stdout, "%s\n", resumeCommand(session))
		return 0
	}
	if session.Live != nil {
		// focusLive rather than focusLiveSession: the indirection is what lets a
		// test pin that a live record reaches this path at all. Going straight to
		// Navigator.Focus from here would skip both Available() and the
		// recycled-pid guard, and Focus opens by writing an OSC 2 title into the
		// recorded terminal -- a stranger's, if the pid has been reused.
		return focusLive(NewGhosttyNavigator(), *session.Live, stderr)
	}
	return resumeSession(session, stderr)
}

// resolveSession picks the one record named by id. It is pure and total: every
// caller hands it a slice it already has, and every outcome is a value.
//
// There is no prefix matching. An id is never hand-typed -- it comes from field
// 2 of the record a picker just displayed -- so accepting a prefix could only
// turn a correct id into a different session, which is the one failure that
// cannot be noticed until the wrong transcript is on screen.
func resolveSession(sessions []Session, id string) (Session, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Session{}, errors.New("no session id given; run `osc8wrap sessions` to list them")
	}

	// Matching is per record, not per spelling, so a record answering to both
	// its UUID and its wrapper pid is one candidate rather than two.
	var matches []Session
	for _, s := range sessions {
		if sessionMatchesID(s, id) {
			matches = append(matches, s)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Session{}, fmt.Errorf("no session with id %q; run `osc8wrap sessions` to list them", id)
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "id %q matches %d sessions; name one of them exactly:", id, len(matches))
		for _, m := range matches {
			fmt.Fprintf(&b, "\n  %s", candidateLine(m))
		}
		return Session{}, errors.New(b.String())
	}
}

// sessionMatchesID reports whether s answers to id under either spelling a
// record can print. An empty ID never matches, so a record that has none -- a
// live wrapper around something that is not claude -- cannot be reached by an
// empty argument even if resolveSession's own empty check were bypassed.
func sessionMatchesID(s Session, id string) bool {
	if s.ID != "" && s.ID == id {
		return true
	}
	pid := s.wrapperPID()
	return pid != 0 && strconv.Itoa(pid) == id
}

// candidateLine describes one ambiguous match: its id, whether it is still
// running, where it ran, and enough of what it was doing to choose between them.
//
// The kind is not decoration. Two records can print the same token -- a
// finished session whose UUID happens to be decimal, colliding with a live
// wrapper's pid -- and then "name one of them exactly" is advice the reader
// cannot follow. Saying which is live, and with which pid, at least explains
// the collision instead of leaving them staring at two identical lines.
//
// The token is sanitized like the rest of the line, and for the same reason:
// this message is where an id osc8wrap did not author -- claude's sessionId,
// carried by a live record -- is echoed to the terminal.
func candidateLine(s Session) string {
	repo, _ := repoAndWorktree(s)
	summary := truncateSummary(sanitizeField(summaryOf(s)), summaryWidth)
	kind := "finished"
	if pid := s.wrapperPID(); pid != 0 {
		kind = fmt.Sprintf("live, pid %d", pid)
	}
	return strings.TrimRight(fmt.Sprintf("%s  (%s)  %s  %s",
		sanitizeField(sessionToken(s)), kind, sanitizeField(repo), summary), " ")
}

// truncateSummary shortens a summary for an error message, counting runes so a
// multibyte prompt is never cut mid-character. Display width is deliberately
// not computed: this is prose, not one of the picker's columns.
func truncateSummary(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

// sessionIDPattern is the alphabet an id may use. It covers both spellings a
// record prints -- a claude UUID and a decimal pid -- and nothing else.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// validSessionID gates an id before it is interpolated into a glob under
// claude's projects directory. This is a path-traversal gate, not a style
// check: "../../etc/passwd", "a/b", "." and "*" must never reach the
// filesystem, whatever the caller does with the result afterwards.
func validSessionID(id string) bool {
	return sessionIDPattern.MatchString(id)
}

// candidatesFor returns the smallest set of records that can contain id.
//
// It exists so `open` and `preview` never pay for the full transcript scan: fzf
// spawns the preview once per highlighted row, and the corpus is hundreds of
// files. At most one transcript can be named after the id, and the live
// sessions are a handful of small files whatever the corpus size, so the pair
// costs one glob and a bounded head read.
//
// Both halves go through the same mergeSessions the listing uses, so `open` and
// `sessions` can never disagree about what a record is -- which cwd wins, which
// rows are one session, what the git context is.
func candidatesFor(id string) ([]Session, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("no session id given; run `osc8wrap sessions` to list them")
	}
	if !validSessionID(id) {
		return nil, fmt.Errorf("invalid session id %q: expected a claude session UUID or a wrapper pid", id)
	}

	entries, err := loadRegistry()
	if err != nil {
		return nil, err
	}
	claude, err := loadClaudeSessions()
	if err != nil {
		return nil, err
	}
	return mergeSessions(joinSessions(entries, claude), transcriptsForID(id)), nil
}

// transcriptsForID finds the transcripts named after id, which is at most one
// per project directory. The only glob metacharacter in the pattern is the "*"
// written here; validSessionID has already established that id contributes
// none, and no separator either, so the match cannot escape the projects tree.
func transcriptsForID(id string) []transcriptMeta {
	matches, err := filepath.Glob(filepath.Join(claudeProjectsDir(), "*", id+".jsonl"))
	if err != nil {
		return nil
	}
	metas := make([]transcriptMeta, 0, len(matches))
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		metas = append(metas, transcriptMeta{
			ID:         id,
			Path:       path,
			ProjectDir: filepath.Dir(path),
			ModTime:    info.ModTime(),
		})
	}
	return metas
}

// focusLive is the seam runOpen goes through to reach a live session. It is a
// variable so a test can assert that a live record is handed to the focus path
// -- Available(), then the recycled-pid guard, then Focus -- rather than to any
// shortcut around it, without a Ghostty anywhere near the test.
var focusLive = focusLiveSession

// focusLiveSession brings the terminal surface hosting l to the front and
// returns the process exit code, with a distinct message for each of the three
// ways it can fail.
//
// It is its own function because Navigator is an interface: a fake exercises
// every branch here on any machine, where exercising the real path would move
// the window focus of whoever is running the tests. stderr is injected for the
// same reason the navigator is: the messages are the observable half of the two
// refusals, and a test must be able to read them without capturing a global.
func focusLiveSession(nav Navigator, l LiveSession, stderr io.Writer) int {
	pid := l.Entry.WrapperPID

	if ok, reason := nav.Available(); !ok {
		printMessage(stderr, "osc8wrap open: cannot jump to session %d: %s\n", pid, reason)
		printMessage(stderr, "%s", accessibilityHint)
		return 1
	}

	// The recycled-pid guard, and the reason it belongs on this path rather
	// than inside the navigator: focusing tags the target surface by writing an
	// OSC 2 title into the recorded device, so an entry whose pid has since
	// been reused would retitle -- and navigate to -- a terminal the user never
	// asked about. Refusing beats corrupting it.
	if !ttyBelongsToOwner(l.Entry) {
		printMessage(stderr,
			"osc8wrap open: refusing to jump to session %d: %s is no longer the terminal of pid %d\n",
			pid, sanitizeField(l.Entry.TTY), pid)
		return 1
	}

	if err := nav.Focus(l); err != nil {
		printMessage(stderr, "osc8wrap open: %v\n", err)
		printMessage(stderr, "%s", accessibilityHint)
		return 1
	}
	return 0
}

// resumeRefusal reports why s cannot be resumed, or "" when it can. The two
// impossibilities are structural rather than environmental: a wrapper around
// something that is not claude has no session to resume, and a record with no
// recorded directory has nowhere to resume it from.
//
// Both messages quote strings osc8wrap did not author -- claude's sessionId and
// the cwd its transcript recorded -- so both go through sanitizeField. Prose is
// printed to a terminal, and an id containing an OSC introducer would otherwise
// be executed by it rather than read by the user.
func resumeRefusal(s Session) string {
	if s.ID == "" {
		return fmt.Sprintf("session %s has no claude session id, so there is nothing to resume",
			sanitizeField(sessionToken(s)))
	}
	if s.Cwd == "" {
		return fmt.Sprintf("session %s recorded no directory, and claude --resume only finds a session "+
			"from the directory it started in", sanitizeField(s.ID))
	}
	return ""
}

// resumeSession runs claude in place, in the directory the session started in.
//
// cmd.Dir is the whole point. Claude files a transcript under the project
// folder derived from the cwd the session STARTED in, so resuming from a
// directory the session merely cd'd into later yields "No conversation found".
// When that directory is gone -- deleted worktrees are the common case among
// finished sessions -- there is no second-best directory to try, so this
// refuses and hands the user the exact command instead of resuming from
// somewhere claude will not look.
func resumeSession(s Session, stderr io.Writer) int {
	if reason := resumeRefusal(s); reason != "" {
		printMessage(stderr, "osc8wrap open: %s\n", reason)
		return 1
	}
	if fi, err := os.Stat(s.Cwd); err != nil || !fi.IsDir() {
		// The prose is sanitized and the command is quoted: both halves of this
		// message carry a directory and an id that came from a transcript, and
		// this is the moment they are written to a terminal.
		printMessage(stderr,
			"osc8wrap open: %s no longer exists, and claude --resume only finds session %s from there.\n"+
				"Run this by hand from wherever that directory now lives:\n\n    %s\n",
			sanitizeField(s.Cwd), sanitizeField(s.ID), resumeCommand(s))
		return 1
	}
	return runClaude(s.Cwd, []string{"--resume", s.ID})
}

// runClaude runs claude with the given arguments in dir, inheriting this
// process's stdio, and returns its exit code. It is a variable so that tests
// can see the directory and argv without a claude on PATH and without an
// interactive process to tear down.
var runClaude = func(dir string, args []string) int {
	cmd := exec.Command("claude", args...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			// A signalled child reports -1, which is not an exit code a shell
			// can be handed; it becomes a plain failure.
			if code := exit.ExitCode(); code >= 0 {
				return code
			}
			return 1
		}
		printMessage(os.Stderr, "osc8wrap open: %v\n", err)
		return 1
	}
	return 0
}

// resumeCommand is the command that resumes s by hand: a subshell so the cd is
// scoped to it, and POSIX single quotes so a directory or id containing shell
// metacharacters survives being pasted into a shell unchanged.
func resumeCommand(s Session) string {
	return fmt.Sprintf("(cd %s && claude --resume %s)", shellQuote(s.Cwd), shellQuote(s.ID))
}

// shellQuote renders s as one shell word.
//
// The usual form is POSIX single quotes, inside which every character is
// literal except the quote itself -- which is therefore closed, escaped and
// reopened. A value carrying control characters cannot be quoted that way: the
// command is printed to a terminal long before anyone pastes it into a shell,
// and single quotes would put the raw bytes on screen, where a directory named
// "/tmp/<ESC>]0;pwned<BEL>" retitles the user's window instead of describing
// itself. Those values switch to ANSI-C quoting, which spells every control
// character as an escape the shell turns back into the byte on disk, so the
// command stays executable while the screen stays inert.
func shellQuote(s string) string {
	if containsControl(s) {
		return ansiCQuote(s)
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// containsControl reports whether s holds a byte a terminal would act on: C0,
// DEL, or a C1 code (0x80-0x9f), whose 0x9d is a one-byte OSC introducer. It
// scans bytes rather than runes so that the answer matches, byte for byte, what
// ansiCQuote below will escape.
func containsControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f || isC1Prefix(s, i) {
			return true
		}
	}
	return false
}

// isC1Prefix reports whether s[i:] begins with the UTF-8 encoding of a C1
// control, which is always 0xc2 followed by the code point's own byte.
func isC1Prefix(s string, i int) bool {
	return s[i] == 0xc2 && i+1 < len(s) && s[i+1] >= 0x80 && s[i+1] <= 0x9f
}

// ansiCQuote renders s as a $'...' word: bash and zsh rather than POSIX sh,
// which is the price of a form that can carry a control character at all.
//
// It walks bytes and copies anything it does not escape through untouched, so
// multibyte text -- and a path that is not valid UTF-8, which the filesystem
// permits -- survives exactly. Control bytes are written as two hex digits so
// that a literal hex digit after one cannot be swallowed into the escape.
func ansiCQuote(s string) string {
	var b strings.Builder
	b.WriteString("$'")
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\r':
			b.WriteString(`\r`)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, c)
		case isC1Prefix(s, i):
			fmt.Fprintf(&b, `\u%04x`, s[i+1])
			i++
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}
