package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// openTestUUID is a real claude session UUID, so the id cases are exercised
// against the shape ids actually have.
const openTestUUID = "b2dae39b-7833-4a90-a6d2-de2edb061a7f"

// openFixture points both stores `open` reads at scratch directories and
// returns the projects directory to write transcripts into.
func openFixture(t *testing.T) string {
	t.Helper()
	useTempRegistry(t)
	return newProjectsDir(t)
}

// claudeCall is one invocation the resume path would have made.
type claudeCall struct {
	dir  string
	args []string
}

// claudeRecorder stands in for the claude binary. Swapping runClaude is what
// keeps the resume cases hermetic: no claude on PATH, no interactive process
// to tear down, and the directory -- the field the whole resume path exists to
// get right -- is readable afterwards.
type claudeRecorder struct {
	calls []claudeCall
	code  int
}

func newClaudeRecorder(t *testing.T, code int) *claudeRecorder {
	t.Helper()
	rec := &claudeRecorder{code: code}
	orig := runClaude
	runClaude = func(dir string, args []string) int {
		rec.calls = append(rec.calls, claudeCall{dir: dir, args: args})
		return rec.code
	}
	t.Cleanup(func() { runClaude = orig })
	return rec
}

// fakeOpenNavigator records what it was asked to focus and fails on demand, so
// every branch of focusLiveSession runs without a Ghostty anywhere near it.
type fakeOpenNavigator struct {
	available bool
	reason    string
	focusErr  error
	focused   []LiveSession
}

func (n *fakeOpenNavigator) Available() (bool, string) { return n.available, n.reason }

func (n *fakeOpenNavigator) Focus(s LiveSession) error {
	n.focused = append(n.focused, s)
	return n.focusErr
}

// fakeFocus replaces the focus path for the duration of a test and records what
// runOpen handed it. It is how the live branch is exercised without a Ghostty:
// nothing below it runs, so no Available() check, no guard and no OSC 2 title
// reach a real terminal.
type fakeFocus struct {
	focused []LiveSession
	code    int
}

func useFakeFocus(t *testing.T, code int) *fakeFocus {
	t.Helper()
	f := &fakeFocus{code: code}
	orig := focusLive
	focusLive = func(nav Navigator, l LiveSession, stderr io.Writer) int {
		f.focused = append(f.focused, l)
		return f.code
	}
	t.Cleanup(func() { focusLive = orig })
	return f
}

func TestParseOpenArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    openOptions
		wantErr bool
	}{
		{name: "no arguments"},
		{name: "an id", args: []string{openTestUUID}, want: openOptions{id: openTestUUID}},
		{name: "a pid", args: []string{"58515"}, want: openOptions{id: "58515"}},
		{name: "print after the id", args: []string{openTestUUID, "--print"}, want: openOptions{id: openTestUUID, print: true}},
		{name: "print before the id", args: []string{"--print", openTestUUID}, want: openOptions{id: openTestUUID, print: true}},
		{name: "help", args: []string{"--help"}, want: openOptions{help: true}},
		{name: "short help", args: []string{"-h"}, want: openOptions{help: true}},
		{name: "unknown option", args: []string{openTestUUID, "--nope"}, wantErr: true},
		{name: "two ids", args: []string{openTestUUID, "58515"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseOpenArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseOpenArgs(%q) succeeded, want an error", tt.args)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("parseOpenArgs(%q) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

// The id is never hand-typed: it is field 2 of the record a picker just
// displayed. So matching is exact on both spellings a record can print, and a
// prefix -- which could only redirect a correct id to a different session --
// matches nothing.
func TestResolveSession(t *testing.T) {
	const other = "0f3a1c2e-1111-4222-8333-444455556666"
	sessions := []Session{
		liveRow(100, openTestUUID, "/tmp/alpha", "waiting"),
		liveRow(200, "", "/tmp/beta", "busy"),
		doneRow(other, "/tmp/gamma", "an old question"),
	}

	tests := []struct {
		name      string
		id        string
		wantToken string
		wantErr   bool
	}{
		{name: "uuid of a live session", id: openTestUUID, wantToken: openTestUUID},
		{name: "uuid of a finished session", id: other, wantToken: other},
		{name: "wrapper pid of a live session with no uuid", id: "200", wantToken: "200"},
		{name: "surrounding whitespace", id: "  " + other + "\n", wantToken: other},
		{name: "empty", id: "", wantErr: true},
		{name: "whitespace only", id: "   ", wantErr: true},
		{name: "a prefix of a uuid", id: openTestUUID[:8], wantErr: true},
		{name: "a prefix of a pid", id: "2", wantErr: true},
		{name: "the child pid rather than the wrapper pid", id: "201", wantErr: true},
		{name: "unknown", id: "99999", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveSession(sessions, tt.id)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveSession(%q) = %+v, want an error", tt.id, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if token := sessionToken(got); token != tt.wantToken {
				t.Errorf("resolveSession(%q) resolved to %q, want %q", tt.id, token, tt.wantToken)
			}
		})
	}
}

// A record whose ID is empty must be unreachable rather than be the thing an
// empty argument lands on -- both through resolveSession's own check and, in
// case that check is ever bypassed, through the match itself.
//
// The two layers are pinned separately. The outer one is observable only in the
// wording, since sessionMatchesID refuses the same input: an empty id has to be
// answered with "run osc8wrap sessions", not with a quoted empty string that
// tells the user nothing about what they typed.
func TestResolveSessionEmptyIDNeverMatchesAnEmptyRecord(t *testing.T) {
	empty := doneRow("", "/tmp/alpha", "a transcript with no id")
	for _, id := range []string{"", "   "} {
		_, err := resolveSession([]Session{empty}, id)
		if err == nil {
			t.Fatalf("resolveSession(%q) resolved to a record whose ID is empty", id)
		}
		if !strings.Contains(err.Error(), "no session id given") {
			t.Errorf("resolveSession(%q) reported %q, want the empty-id message", id, err)
		}
		if sessionMatchesID(empty, id) {
			t.Errorf("sessionMatchesID(%+v, %q) = true, want false", empty, id)
		}
	}
	// The same must hold for the pid spelling of a record that has no wrapper.
	if sessionMatchesID(empty, "0") {
		t.Error(`a finished record matched the pid "0"`)
	}
}

// Two records answering to one id is a wrong-session hazard, so it fails with
// the candidates rather than picking the first.
func TestResolveSessionAmbiguous(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		sessions []Session
	}{
		{
			name:     "the same uuid twice",
			id:       openTestUUID,
			sessions: []Session{doneRow(openTestUUID, "/tmp/alpha", "the older one"), liveRow(100, openTestUUID, "/tmp/beta", "busy")},
		},
		{
			// The two spellings share one namespace, so a wrapper pid can in
			// principle collide with another record's UUID.
			name:     "a pid that is also another record's id",
			id:       "4242",
			sessions: []Session{liveRow(4242, "", "/tmp/alpha", "busy"), doneRow("4242", "/tmp/beta", "the older one")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveSession(tt.sessions, tt.id)
			if err == nil {
				t.Fatal("an ambiguous id resolved, want an error naming both candidates")
			}
			for _, want := range []string{"alpha", "beta"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not name the candidate in %s: %v", want, err)
				}
			}
		})
	}
}

// One record answering to both of its own spellings is one candidate. Counting
// per spelling instead would report a single session as ambiguous and refuse to
// open it -- and only for the sessions whose UUID happens to be numeric, so it
// would look like a mystery rather than a bug.
func TestResolveSessionCountsARecordOnce(t *testing.T) {
	// ID "100" and wrapper pid 100: the same record under both spellings.
	both := liveRow(100, "100", "/tmp/alpha", "busy")
	got, err := resolveSession([]Session{both}, "100")
	if err != nil {
		t.Fatalf("a record matching id %q under both of its spellings did not resolve: %v", "100", err)
	}
	if sessionToken(got) != "100" {
		t.Errorf("resolved to %q, want %q", sessionToken(got), "100")
	}
}

// The ambiguity error is where an id osc8wrap did not author is echoed to the
// terminal: claude writes the sessionId, and osc8wrap only ever copies it.
func TestResolveSessionAmbiguityStripsControlCharacters(t *testing.T) {
	hostile := "4242\x1b]0;pwned\x07evil"
	sessions := []Session{
		liveRow(4242, hostile, "/tmp/alpha", "busy"),
		doneRow("4242", "/tmp/beta", "the older one"),
	}

	_, err := resolveSession(sessions, "4242")
	if err == nil {
		t.Fatal("an ambiguous id resolved, want an error naming both candidates")
	}
	if strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Errorf("the candidate list carried a raw escape:\n%q", err.Error())
	}
	// The payload survives as inert text, so the candidate is still nameable.
	if !strings.Contains(err.Error(), "]0;pwnedevil") {
		t.Errorf("the candidate list dropped the id along with the escape:\n%v", err)
	}
}

// The id is interpolated into a glob under claude's projects directory, so this
// is a path-traversal gate rather than a spelling check.
func TestValidSessionID(t *testing.T) {
	for _, id := range []string{openTestUUID, "58515", "0", "a_b-C9"} {
		if !validSessionID(id) {
			t.Errorf("validSessionID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{
		"", "../../etc/passwd", "a/b", "*", ".", "..", "**/x", `a\b`,
		"a b", "a.jsonl", "\n", "id\x00", "~", "$HOME",
	} {
		if validSessionID(id) {
			t.Errorf("validSessionID(%q) = true, want false", id)
		}
	}
}

// The gate has to hold at the entry point too, since that is where the glob is
// built: an id that could escape the projects tree must be refused before any
// filesystem call.
func TestCandidatesForRejectsUnsafeIDs(t *testing.T) {
	openFixture(t)
	for _, id := range []string{"", "   ", "../../etc/passwd", "a/b", "*", ".", "a b"} {
		t.Run(id, func(t *testing.T) {
			got, err := candidatesFor(id)
			if err == nil {
				t.Fatalf("candidatesFor(%q) = %+v, want an error", id, got)
			}
			if got != nil {
				t.Errorf("candidatesFor(%q) returned %d candidates alongside its error", id, len(got))
			}
		})
	}
}

// preview runs once per highlighted row, so the lookup must cost one glob
// rather than a scan of the whole corpus. What is observable from outside is
// that the unrelated transcripts never make it into the result.
func TestCandidatesForReadsOnlyTheMatchingTranscript(t *testing.T) {
	projects := openFixture(t)
	writeTranscriptFile(t, filepath.Join(projects, "-tmp-alpha"), "11111111-1111-4111-8111-111111111111.jsonl",
		userLine("/tmp/alpha", "main", strconv.Quote("the wrong one")))
	writeTranscriptFile(t, filepath.Join(projects, "-tmp-alpha"), "22222222-2222-4222-8222-222222222222.jsonl",
		userLine("/tmp/alpha", "main", strconv.Quote("also wrong")))
	// In a second project directory, so the glob is doing real work.
	writeTranscriptFile(t, filepath.Join(projects, "-tmp-beta"), openTestUUID+".jsonl",
		userLine("/tmp/beta", "main", strconv.Quote("the right one")))

	got, err := candidatesFor(openTestUUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("candidatesFor returned %d candidates, want 1: %+v", len(got), got)
	}
	if got[0].ID != openTestUUID {
		t.Errorf("candidate id = %q, want %q", got[0].ID, openTestUUID)
	}
	if got[0].Prompt != "the right one" {
		t.Errorf("candidate prompt = %q, want the transcript named after the id", got[0].Prompt)
	}
}

// The resume trap: claude files the transcript under the project folder derived
// from the cwd the session STARTED in, so a later in-session cd is the one
// directory resuming from would fail in.
func TestOpenResumesInTheTranscriptsFirstCwd(t *testing.T) {
	projects := openFixture(t)
	start := filepath.Join(t.TempDir(), "start")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscriptFile(t, filepath.Join(projects, "-tmp-start"), openTestUUID+".jsonl",
		userLine(start, "main", strconv.Quote("first prompt")),
		userLine(filepath.Join(t.TempDir(), "later"), "main", strconv.Quote("after a cd")))

	rec := newClaudeRecorder(t, 7)
	var stdout, stderr bytes.Buffer
	// The child's exit code is the command's: a resumed session that fails must
	// not look like a success to whatever ran osc8wrap.
	if code := runOpen([]string{openTestUUID}, &stdout, &stderr); code != 7 {
		t.Errorf("exit code = %d, want the child's 7; stderr:\n%s", code, stderr.String())
	}
	if len(rec.calls) != 1 {
		t.Fatalf("claude was run %d times, want once; stderr:\n%s", len(rec.calls), stderr.String())
	}
	if rec.calls[0].dir != start {
		t.Errorf("resumed in %q, want the first cwd %q", rec.calls[0].dir, start)
	}
	if want := []string{"--resume", openTestUUID}; !equalArgs(rec.calls[0].args, want) {
		t.Errorf("ran claude %q, want %q", rec.calls[0].args, want)
	}
}

// Nine of 35 real cwds were deleted worktrees. There is no second-best
// directory to resume from, so this refuses and hands over the command instead.
func TestOpenRefusesWhenTheCwdIsGone(t *testing.T) {
	projects := openFixture(t)
	gone := filepath.Join(t.TempDir(), "gone")
	writeTranscriptFile(t, filepath.Join(projects, "-tmp-gone"), openTestUUID+".jsonl",
		userLine(gone, "main", strconv.Quote("a session in a deleted worktree")))

	rec := newClaudeRecorder(t, 0)
	var stdout, stderr bytes.Buffer
	if code := runOpen([]string{openTestUUID}, &stdout, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if len(rec.calls) != 0 {
		t.Errorf("claude was run %d times from a directory that does not exist", len(rec.calls))
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
	for _, want := range []string{gone, "claude --resume", openTestUUID} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not mention %q:\n%s", want, stderr.String())
		}
	}
}

// --print is a pure formatter: it never stats, never resumes, and quotes for a
// shell it will not run.
func TestOpenPrintQuotesAndRunsNothing(t *testing.T) {
	projects := openFixture(t)
	// A directory whose name would end the quoting if it were not escaped.
	const cwd = "/tmp/it's here"
	writeTranscriptFile(t, filepath.Join(projects, "-tmp-quoted"), openTestUUID+".jsonl",
		userLine(cwd, "main", strconv.Quote("a session in an awkward directory")))

	rec := newClaudeRecorder(t, 0)
	var stdout, stderr bytes.Buffer
	if code := runOpen([]string{openTestUUID, "--print"}, &stdout, &stderr); code != 0 {
		t.Errorf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if len(rec.calls) != 0 {
		t.Errorf("--print ran claude %d times", len(rec.calls))
	}
	want := `(cd '/tmp/it'\''s here' && claude --resume '` + openTestUUID + "')\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

// A directory whose name contains an ESC is creatable, and claude records
// whatever it was started in. The command is printed to a terminal before it is
// pasted into a shell, so it has to be inert on screen and still executable
// afterwards -- which single quotes cannot do at once, and $'...' can.
func TestOpenControlCharactersNeverReachTheTerminal(t *testing.T) {
	projects := openFixture(t)
	// The cwd is "/tmp/osc8wrap-gone<ESC>]0;pwned<BEL>evil<ESC>[2J": set the
	// window title, then clear the screen. The directory does not exist, which
	// is also what makes the refusal case below reachable.
	//
	// The record is written out rather than built with userLine, because that
	// helper quotes its cwd the Go way and "\x1b" is not an escape JSON has, so
	// the line would come out unparseable instead of hostile.
	writeTranscriptFile(t, filepath.Join(projects, "-tmp-hostile"), openTestUUID+".jsonl",
		`{"type":"user","cwd":"/tmp/osc8wrap-gone\u001b]0;pwned\u0007evil\u001b[2J",`+
			`"gitBranch":"main","message":{"role":"user","content":"a session in a hostile directory"}}`)
	rec := newClaudeRecorder(t, 0)

	t.Run("--print", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runOpen([]string{openTestUUID, "--print"}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
		}
		want := `(cd $'/tmp/osc8wrap-gone\x1b]0;pwned\x07evil\x1b[2J' && claude --resume '` +
			openTestUUID + "')\n"
		if stdout.String() != want {
			t.Errorf("stdout = %q, want %q", stdout.String(), want)
		}
		if strings.ContainsAny(stdout.String(), "\x1b\x07") {
			t.Errorf("--print put a raw escape on the terminal:\n%q", stdout.String())
		}
	})

	t.Run("the refusal", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runOpen([]string{openTestUUID}, &stdout, &stderr); code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if len(rec.calls) != 0 {
			t.Errorf("claude was run %d times from a directory that does not exist", len(rec.calls))
		}
		out := stderr.String()
		if strings.ContainsAny(out, "\x1b\x07") {
			t.Fatalf("the refusal put a raw escape on the terminal:\n%q", out)
		}
		// The prose keeps the payload as inert text, so the user can still see
		// which directory is meant.
		if !strings.Contains(out, "/tmp/osc8wrap-gone]0;pwnedevil[2J") {
			t.Errorf("the refusal does not name the directory:\n%s", out)
		}
	})
}

// The id half of the same hole: a live record carries claude's sessionId, which
// osc8wrap validates for neither shape nor content before quoting it back.
func TestResumeRefusalStripsControlCharacters(t *testing.T) {
	hostile := "b2dae39b\x1b]0;pwned\x07"
	got := resumeRefusal(Session{ID: hostile})
	if got == "" {
		t.Fatal("a session with no cwd was not refused")
	}
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("the refusal carried a raw escape:\n%q", got)
	}
	if !strings.Contains(got, "b2dae39b]0;pwned") {
		t.Errorf("the refusal does not name the session:\n%s", got)
	}
}

// A live record must reach the focus path -- Available(), then the recycled-pid
// guard, then Focus -- and not some shortcut to the navigator. Focus opens by
// writing an OSC 2 title into the recorded terminal, so skipping the guard
// retitles, and then jumps to, a stranger's window whenever a pid was reused.
func TestOpenSendsALiveSessionThroughTheFocusPath(t *testing.T) {
	dir := useTempRegistry(t)
	newProjectsDir(t)
	// Not Ghostty: if this wiring is ever bypassed, the real navigator that the
	// bypass reaches must still refuse before it touches a terminal.
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")

	// This process's own pid, so the registry does not prune the entry as dead.
	pid := os.Getpid()
	writeEntry(t, dir, RegistryEntry{
		WrapperPID: pid,
		ChildPID:   pid + 1,
		TTY:        filepath.Join(t.TempDir(), "ttys999"),
		SurfaceID:  "0x1",
		Cwd:        "/tmp/alpha",
		Command:    []string{"claude"},
	})

	focus := useFakeFocus(t, 5)
	rec := newClaudeRecorder(t, 0)

	var stdout, stderr bytes.Buffer
	if code := runOpen([]string{strconv.Itoa(pid)}, &stdout, &stderr); code != 5 {
		t.Errorf("exit code = %d, want the focus path's 5; stderr:\n%s", code, stderr.String())
	}
	if len(focus.focused) != 1 {
		t.Fatalf("the focus path ran %d times, want once; stderr:\n%s", len(focus.focused), stderr.String())
	}
	if focus.focused[0].Entry.WrapperPID != pid {
		t.Errorf("focused the wrapper pid %d, want %d", focus.focused[0].Entry.WrapperPID, pid)
	}
	if len(rec.calls) != 0 {
		t.Errorf("a live session was resumed %d times instead of being jumped to", len(rec.calls))
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/tmp/plain", `'/tmp/plain'`},
		{"", `''`},
		{"/tmp/it's here", `'/tmp/it'\''s here'`},
		{"a; rm -rf /", `'a; rm -rf /'`},
		{"$HOME/`x`", "'$HOME/`x`'"},
		// Control characters switch to ANSI-C quoting, where the escape is text
		// the shell decodes rather than a byte the terminal acts on.
		{"/tmp/a\x1b]0;pwned\x07b", `$'/tmp/a\x1b]0;pwned\x07b'`},
		{"/tmp/a\nb\tc\rd", `$'/tmp/a\nb\tc\rd'`},
		{"/tmp/a\x7fb", `$'/tmp/a\x7fb'`},
		// A C1 control is two bytes in UTF-8 and 0x9d is a one-byte OSC
		// introducer, so it is escaped by code point rather than passed through.
		{"/tmp/a\u009db", `$'/tmp/a\u009db'`},
		// Inside $'...' the quote and the backslash are the two characters that
		// still need escaping.
		{"/tmp/it's\x1b \\here", `$'/tmp/it\'s\x1b \\here'`},
		// Multibyte text is copied through byte for byte.
		{"/tmp/日本\x1b", `$'/tmp/日本\x1b'`},
	}
	for _, tt := range tests {
		if got := shellQuote(tt.in); got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// An id nobody can act on must say so and point at the listing, not fail
// silently or resume something else.
func TestOpenUnknownID(t *testing.T) {
	openFixture(t)
	rec := newClaudeRecorder(t, 0)

	var stdout, stderr bytes.Buffer
	if code := runOpen([]string{"99999"}, &stdout, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if len(rec.calls) != 0 {
		t.Errorf("claude was run %d times for an id that matches nothing", len(rec.calls))
	}
	for _, want := range []string{"99999", "osc8wrap sessions"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not mention %q:\n%s", want, stderr.String())
		}
	}
}

// Three ways to fail, three messages, and in two of them the surface must be
// left alone: Focus is what writes into the user's terminal.
func TestFocusLiveSession(t *testing.T) {
	live := testLiveSession("osc8wrap-74", "waiting", "/tmp/alpha", "0x1")
	// No recorded tty means the recycled-pid guard has nothing to contradict,
	// which is what lets the success and Focus-error cases run anywhere.
	live.Entry.TTY = ""

	t.Run("navigation unavailable", func(t *testing.T) {
		nav := &fakeOpenNavigator{reason: "not running under Ghostty (TERM_PROGRAM is unset)"}
		var stderr bytes.Buffer
		code := focusLiveSession(nav, live, &stderr)
		out := stderr.String()
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if len(nav.focused) != 0 {
			t.Error("Focus was called although navigation is unavailable")
		}
		for _, want := range []string{"TERM_PROGRAM", "Accessibility"} {
			if !strings.Contains(out, want) {
				t.Errorf("stderr does not mention %q:\n%s", want, out)
			}
		}
	})

	t.Run("the recycled-pid guard refuses", func(t *testing.T) {
		// pid 1 is the one process guaranteed to be running and to have no
		// controlling terminal, so an entry claiming it sits on a device is the
		// contradiction the guard exists to catch.
		if dev, ok := processTTYDevice(1); !ok || dev != 0 {
			t.Skip("pid 1's controlling terminal cannot serve as the guard's negative case here")
		}
		stale := live
		stale.Entry.WrapperPID = 1
		// The recorded device is quoted back in the refusal, so it is sanitized
		// there like every other value osc8wrap did not author.
		stale.Entry.TTY = filepath.Join(t.TempDir(), "ttys999\x1b]0;pwned\x07")

		nav := &fakeOpenNavigator{available: true}
		var stderr bytes.Buffer
		code := focusLiveSession(nav, stale, &stderr)
		out := stderr.String()
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if len(nav.focused) != 0 {
			t.Error("Focus was called on an entry whose pid no longer owns its terminal")
		}
		if !strings.Contains(out, "no longer the terminal of pid 1") {
			t.Errorf("stderr does not explain the refusal:\n%s", out)
		}
		if strings.ContainsAny(out, "\x1b\x07") {
			t.Errorf("the refusal printed a raw escape:\n%q", out)
		}
	})

	t.Run("focused", func(t *testing.T) {
		nav := &fakeOpenNavigator{available: true}
		var stderr bytes.Buffer
		code := focusLiveSession(nav, live, &stderr)
		out := stderr.String()
		if code != 0 {
			t.Errorf("exit code = %d, want 0; stderr:\n%s", code, out)
		}
		if len(nav.focused) != 1 {
			t.Fatalf("Focus was called %d times, want once", len(nav.focused))
		}
		if nav.focused[0].Entry.WrapperPID != live.Entry.WrapperPID {
			t.Errorf("focused pid %d, want %d", nav.focused[0].Entry.WrapperPID, live.Entry.WrapperPID)
		}
		if out != "" {
			t.Errorf("a successful jump printed %q", out)
		}
	})

	t.Run("Focus fails", func(t *testing.T) {
		nav := &fakeOpenNavigator{available: true, focusErr: errors.New("osascript: not authorized")}
		var stderr bytes.Buffer
		code := focusLiveSession(nav, live, &stderr)
		out := stderr.String()
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if len(nav.focused) != 1 {
			t.Errorf("Focus was called %d times, want once", len(nav.focused))
		}
		for _, want := range []string{"not authorized", "Accessibility"} {
			if !strings.Contains(out, want) {
				t.Errorf("stderr does not mention %q:\n%s", want, out)
			}
		}
	})
}

// "--" always introduces a command to wrap, and open(1) is a real program.
func TestOpenIsDispatched(t *testing.T) {
	if _, ok := lookupSubcommand([]string{"open", openTestUUID}, false); !ok {
		t.Error(`"open" is not registered as a subcommand`)
	}
	if _, ok := lookupSubcommand([]string{"open", "."}, true); ok {
		t.Error(`"osc8wrap -- open ." dispatched instead of wrapping open(1)`)
	}
}

// Two records can print the same token when a finished session's UUID is
// decimal and collides with a live wrapper's pid. The message cannot make that
// nameable, but it must at least stop claiming the two lines are telling the
// reader apart.
func TestResolveSessionAmbiguityDistinguishesCandidates(t *testing.T) {
	sessions := []Session{
		liveRow(4242, "", "/tmp/alpha", "busy"),
		doneRow("4242", "/tmp/beta", "the older one"),
	}

	_, err := resolveSession(sessions, "4242")
	if err == nil {
		t.Fatal("an ambiguous id resolved, want an error naming both candidates")
	}
	for _, want := range []string{"(live, pid 4242)", "(finished)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the candidate list never says %s:\n%v", want, err)
		}
	}
}
