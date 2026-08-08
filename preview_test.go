package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// plainStyle renders without escapes, which is what most of these cases want:
// they are about which text reaches the pane, not about how it is attributed.
var plainStyle = previewStyle{}

// previewBase is an arbitrary fixed instant; the pane shows a date, never a
// duration, so the wall clock never enters into it.
var previewBase = time.Date(2026, 7, 26, 17, 40, 0, 0, time.UTC)

// previewTurns builds an alternating user/assistant transcript whose turns are
// individually recognisable, so a case can say which end of it was read.
func previewTurns(n int) []string {
	lines := make([]string, 0, n)
	for i := range n {
		text := fmt.Sprintf("turn-%d", i)
		if i%2 == 0 {
			lines = append(lines, userLine("/repo", "main", fmt.Sprintf("%q", text)))
			continue
		}
		lines = append(lines,
			fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":%q}}`, text))
	}
	return lines
}

// previewFinished is a finished record pointing at a transcript written for the
// case, the way mergeSessions would have left one.
func previewFinished(t *testing.T, lines ...string) Session {
	t.Helper()
	path := writeTranscriptFile(t, t.TempDir(), "uuid-done.jsonl", lines...)
	return Session{
		ID:             "uuid-done",
		TranscriptPath: path,
		ProjectDir:     filepath.Dir(path),
		Cwd:            "/Users/mash/src/osc8wrap",
		Branch:         "main",
		Root:           "/Users/mash/src/osc8wrap",
		Worktree:       "/Users/mash/src/osc8wrap",
		Prompt:         "turn-0",
		Modified:       previewBase,
	}
}

// previewLive is the same record with a wrapper still hosting it.
func previewLive(t *testing.T, status, waitingFor string, lines ...string) Session {
	t.Helper()
	s := previewFinished(t, lines...)
	live := testLiveSession("osc8wrap-200", status, s.Cwd, "0x1")
	live.SessionID = s.ID
	live.WaitingFor = waitingFor
	s.Live = &live
	return s
}

// You are asking "which session was this", and the answer is what the user
// asked for at the start -- so a finished session previews from the beginning,
// past the synthetic turns claude opens every session with.
func TestPreviewFinishedSessionShowsItsHead(t *testing.T) {
	lines := append(
		[]string{userLine("/repo", "main", `"<system-reminder>identical in every session</system-reminder>"`)},
		previewTurns(12)...)
	out := renderPreview(previewFinished(t, lines...), plainStyle)

	if !strings.Contains(out, "turn-0") {
		t.Errorf("preview does not show the opening turn:\n%s", out)
	}
	if strings.Contains(out, "turn-11") {
		t.Errorf("preview reached the end of the transcript, want only its head:\n%s", out)
	}
	if strings.Contains(out, "system-reminder") {
		t.Errorf("preview shows a synthetic turn that identifies no session:\n%s", out)
	}
	// The speakers are named, or the pane is a wall of unattributed text.
	for _, label := range []string{"you", "claude"} {
		if !strings.Contains(out, label) {
			t.Errorf("preview does not name the %q turns:\n%s", label, out)
		}
	}
}

// You are asking "what is it doing right now", and the present is at the end.
func TestPreviewLiveSessionShowsItsTail(t *testing.T) {
	out := renderPreview(previewLive(t, "waiting", "input needed", previewTurns(12)...), plainStyle)

	if !strings.Contains(out, "turn-11") {
		t.Errorf("preview does not show the latest turn:\n%s", out)
	}
	if strings.Contains(out, "turn-0") {
		t.Errorf("preview shows the start of the transcript, want its tail:\n%s", out)
	}
}

// The pane used to carry claude's status on a line of its own. It says nothing
// the reader does not already have: the header's first word is the tag the
// status produced (WAIT, BUSY, IDLE, LIVE), and the listing row the cursor is
// sitting on carries the "waiting for" text. A preview pane is a few dozen
// lines, so a line that repeats two things on screen is a line the conversation
// does not get.
func TestPreviewHasNoLiveStatusLine(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		waitingFor string
		wantTag    string
		notWant    []string
	}{
		{"blocked", "waiting", "input needed", "WAIT", []string{"waiting — input needed", "waiting"}},
		// A wrapper claude published no status for is still tagged, so there is
		// nothing left for a status line to add here either.
		{"no status published", "", "", "LIVE", []string{"live"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderPreview(previewLive(t, tt.status, tt.waitingFor, previewTurns(2)...), plainStyle)
			first, rest, _ := strings.Cut(out, "\n")
			if !strings.Contains(first, tt.wantTag) {
				t.Errorf("header does not tag the session %q:\n%s", tt.wantTag, out)
			}
			for _, unwanted := range tt.notWant {
				if strings.Contains(rest, unwanted) {
					t.Errorf("pane still carries the live status line (%q):\n%s", unwanted, out)
				}
			}
		})
	}
}

// Under a query the question the body answers changes: not "how did this start"
// or "what is it doing", but "why is this row in my filtered list". The region
// the query hit displaces the head and the tail -- and it is a region neither of
// them would have reached, which is what these two cases are chosen to prove.
func TestPreviewShowsTheMatchingRegion(t *testing.T) {
	tests := []struct {
		name    string
		s       Session
		query   string
		want    string
		notWant string
	}{
		// The head stops at turn-7, so turn-9 can only come from the match.
		{"finished", previewFinished(t, previewTurns(12)...), "turn-9", "turn-9", "turn-0"},
		// The tail starts at turn-4, so turn-0 can only come from the match.
		{"live", previewLive(t, "busy", "", previewTurns(12)...), "turn-0", "turn-0", "turn-11"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, ok := parseSearchQuery(tt.query)
			if !ok {
				t.Fatalf("parseSearchQuery(%q) found no terms", tt.query)
			}
			out := renderPreviewMatching(tt.s, plainStyle, q)
			if !strings.Contains(out, tt.want) {
				t.Errorf("preview does not show the matching region %q:\n%s", tt.want, out)
			}
			if strings.Contains(out, tt.notWant) {
				t.Errorf("preview still shows %q, so the query displaced nothing:\n%s", tt.notWant, out)
			}
			// The header answers a question the query did not replace.
			if !strings.Contains(out, "uuid-done") {
				t.Errorf("preview lost the header under a query:\n%s", out)
			}
		})
	}
}

// fzf sends {q} on every keystroke, so most of the queries this command sees
// match nothing -- including every prefix of one that eventually will. Falling
// back to the usual turns is what keeps the pane from blinking empty while the
// user types.
func TestPreviewFallsBackWhenTheQueryMissed(t *testing.T) {
	q, ok := parseSearchQuery("nothinginthistranscript")
	if !ok {
		t.Fatal("parseSearchQuery found no terms in a single word")
	}
	out := renderPreviewMatching(previewFinished(t, previewTurns(12)...), plainStyle, q)
	if !strings.Contains(out, "turn-0") {
		t.Errorf("a query that matched nothing cost the pane its usual turns:\n%s", out)
	}
}

// The turn a query hits is usually a long assistant answer with the term
// buried somewhere inside it, which tells the reader what was said and not what
// was asked. The prompt above it is what identifies the exchange.
func TestPreviewShowsThePromptAboveTheHit(t *testing.T) {
	// Synthetic user turns are what a session opens with and what claude keeps
	// injecting mid-conversation; none of them is a question anybody asked.
	synthetic := []string{
		userLine("/repo", "main", `"<system-reminder>the file changed</system-reminder>"`),
		userLine("/repo", "main", `"<command-name>/compact</command-name>"`),
		userLine("/repo", "main", `"<caveat>the messages below were generated by a local command</caveat>"`),
	}
	huge := strings.Repeat("あ", previewTurnRunes*2)

	tests := []struct {
		name  string
		lines []string
		query string
		// want is what the pane must show, in this order, top to bottom.
		want []string
		// once is text that must appear exactly one time in the body.
		once string
	}{
		{
			name: "the prompt that led to the answer",
			lines: []string{
				userLine("/repo", "main", `"how do I wire the ratchet"`),
				textLine("assistant", "the ratchet is wired in linker.go"),
			},
			query: "wired",
			want:  []string{"you", "how do I wire the ratchet", "claude", "the ratchet is wired in linker.go"},
		},
		{
			// The hit is its own question, so prepending it would print the
			// same turn twice under two headings.
			name:  "a hit on the prompt itself is not doubled",
			lines: []string{userLine("/repo", "main", `"how do I wire the ratchet"`)},
			query: "ratchet",
			want:  []string{"you", "how do I wire the ratchet"},
			once:  "how do I wire the ratchet",
		},
		{
			name: "synthetic turns are skipped to reach the real prompt",
			lines: append(append(
				[]string{userLine("/repo", "main", `"how do I wire the ratchet"`)},
				synthetic...),
				textLine("assistant", "the ratchet is wired in linker.go")),
			query: "wired",
			want:  []string{"you", "how do I wire the ratchet", "claude", "the ratchet is wired in linker.go"},
		},
		{
			// Nothing preceded the hit, and an empty "you" heading over an
			// empty turn would be worse than no heading at all.
			name:  "no prompt to show",
			lines: []string{textLine("assistant", "the ratchet is wired in linker.go")},
			query: "wired",
			want:  []string{"claude", "the ratchet is wired in linker.go"},
			once:  "the ratchet is wired in linker.go",
		},
		{
			// A pasted prompt must not be carried out of the scan whole, nor
			// cut mid-rune on the way.
			name: "an over-long prompt is clamped",
			lines: []string{
				userLine("/repo", "main", fmt.Sprintf("%q", huge)),
				textLine("assistant", "the ratchet is wired in linker.go"),
			},
			query: "wired",
			want:  []string{"you", "claude", "the ratchet is wired in linker.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderPreviewMatching(previewFinished(t, tt.lines...), plainStyle, mustQuery(t, tt.query))
			at := 0
			for _, want := range tt.want {
				i := strings.Index(out[at:], want)
				if i < 0 {
					t.Fatalf("pane is missing %q below what precedes it:\n%s", want, out)
				}
				at += i + len(want)
			}
			if tt.once != "" && strings.Count(out, tt.once) != 1 {
				t.Errorf("%q appears %d times, want once:\n%s", tt.once, strings.Count(out, tt.once), out)
			}
		})
	}

	t.Run("the clamped prompt is whole runes", func(t *testing.T) {
		out := renderPreviewMatching(previewFinished(t,
			userLine("/repo", "main", fmt.Sprintf("%q", huge)),
			textLine("assistant", "the ratchet is wired in linker.go"),
		), plainStyle, mustQuery(t, "wired"))

		if n := strings.Count(out, "あ"); n != previewTurnRunes {
			t.Errorf("kept %d runes of the prompt, want %d", n, previewTurnRunes)
		}
		if !utf8.ValidString(out) {
			t.Errorf("the clamp split a rune:\n%q", out)
		}
	})
}

// The no-query pane is the one the user sees on every cursor movement, so it is
// pinned whole rather than by the phrases a case happens to care about: the
// prompt above a hit is a change to the query path and to nothing else.
func TestPreviewWithoutAQueryIsUnchanged(t *testing.T) {
	out := renderPreview(previewFinished(t, previewTurns(4)...), plainStyle)
	want := "done  osc8wrap (main)\n" +
		"2026-07-26 17:40  /Users/mash/src/osc8wrap\n" +
		"uuid-done\n" +
		"\n" +
		"you\n  turn-0\n" +
		"\nclaude\n  turn-1\n" +
		"\nyou\n  turn-2\n" +
		"\nclaude\n  turn-3\n"
	if out != want {
		t.Errorf("pane = %q\nwant   %q", out, want)
	}
}

// The header answers the question the row was selected on, so everything the
// listing showed has to survive into it.
func TestPreviewHeaderNamesTheSession(t *testing.T) {
	s := previewFinished(t, previewTurns(2)...)
	s.Worktree = "/Users/mash/src/osc8wrap/.worktrees/feat-preview"
	s.Branch = "feat/preview"

	out := renderPreview(s, plainStyle)
	header, _, _ := strings.Cut(out, "\n\n")
	for _, want := range []string{
		"done",                     // the tag
		"osc8wrap/feat-preview",    // repo and worktree
		"(feat/preview)",           // branch
		"2026-07-26 17:40",         // date
		"/Users/mash/src/osc8wrap", // cwd
		"uuid-done",                // id
	} {
		if !strings.Contains(header, want) {
			t.Errorf("header is missing %q:\n%s", want, header)
		}
	}
}

// NO_COLOR is a correctness requirement here rather than a courtesy: a user who
// asked for no escapes must get an output with no escape byte in it at all.
func TestPreviewRespectsNoColor(t *testing.T) {
	s := previewFinished(t, previewTurns(4)...)

	t.Setenv("NO_COLOR", "")
	if out := renderPreview(s, newPreviewStyle()); !strings.Contains(out, "\x1b") {
		t.Fatalf("nothing is styled even with NO_COLOR unset, so the case below proves nothing:\n%s", out)
	}

	t.Setenv("NO_COLOR", "1")
	if out := renderPreview(s, newPreviewStyle()); strings.ContainsRune(out, 0x1b) {
		t.Errorf("preview still carries an escape byte under NO_COLOR:\n%q", out)
	}
}

// A transcript is other people's text. A turn carrying an OSC sequence must not
// be able to repaint the pane it is displayed in -- or, since fzf spawns this
// per highlighted row, the user's whole screen on every keystroke.
func TestPreviewStripsEscapesFromTurns(t *testing.T) {
	hostile := `"\u001b]0;pwned\u0007\u001b[2Jgone"`
	lines := []string{
		userLine("/repo", "main", hostile),
		fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":%s}}`, hostile),
	}

	for _, tt := range []struct {
		name string
		s    Session
	}{
		{"finished", previewFinished(t, lines...)},
		{"live", previewLive(t, "busy", "", lines...)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := renderPreview(tt.s, plainStyle)
			if strings.ContainsAny(out, "\x1b\x07") {
				t.Fatalf("preview passed a raw escape through:\n%q", out)
			}
			// What the sequence carried survives as inert text, which is all it
			// can be once nothing introduces it.
			if !strings.Contains(out, "]0;pwned") || !strings.Contains(out, "[2Jgone") {
				t.Errorf("preview dropped the payload as well as the introducer:\n%s", out)
			}
		})
	}
}

// One pasted file must not spend the whole pane, and a Japanese prompt must not
// be cut to a third of the intended length, so the budget counts runes.
func TestPreviewClampsAHugeTurn(t *testing.T) {
	huge := strings.Repeat("あ", previewTurnRunes*2)
	lines := []string{
		userLine("/repo", "main", fmt.Sprintf("%q", huge)),
		userLine("/repo", "main", `"the turn after it"`),
	}
	out := renderPreview(previewFinished(t, lines...), plainStyle)

	if n := strings.Count(out, "あ"); n != previewTurnRunes {
		t.Errorf("kept %d runes of the huge turn, want %d", n, previewTurnRunes)
	}
	if !strings.Contains(out, "the turn after it") {
		t.Errorf("the huge turn crowded out the one after it:\n%s", out)
	}
}

func TestPreviewClampRunes(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		limit int
		want  string
		used  int
	}{
		{"under the limit", "hello", 10, "hello", 5},
		{"exactly the limit", "hello", 5, "hello", 5},
		{"over the limit", "hello", 3, "hel …", 3},
		{"multibyte is counted by rune", "ありがとう", 3, "ありが …", 3},
		{"empty", "", 3, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, used := clampRunes(tt.text, tt.limit)
			if got != tt.want || used != tt.used {
				t.Errorf("clampRunes(%q, %d) = (%q, %d), want (%q, %d)",
					tt.text, tt.limit, got, used, tt.want, tt.used)
			}
		})
	}
}

// A transcript that is missing, empty or unreadable is not an error: the pane
// keeps the header, which is most of what the row was selected for. An fzf
// preview command that exits non-zero replaces all of that with fzf's own
// error text.
func TestPreviewWithoutATranscript(t *testing.T) {
	live := previewLive(t, "busy", "")
	live.TranscriptPath = ""
	live.ProjectDir = ""

	tests := []struct {
		name string
		s    Session
		note string
	}{
		{"nothing recorded yet", live, "(no transcript yet)"},
		{"a file that is gone", func() Session {
			s := previewFinished(t)
			s.TranscriptPath = filepath.Join(t.TempDir(), "absent.jsonl")
			return s
		}(), "(nothing readable in "},
		{"a file with nothing parseable in it", previewFinished(t, "not json", "{{{"), "(nothing readable in "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderPreview(tt.s, plainStyle)
			if !strings.Contains(out, "uuid-done") {
				t.Errorf("preview lost the header it could still show:\n%s", out)
			}
			if !strings.Contains(out, tt.note) {
				t.Errorf("preview does not explain the missing transcript:\n%s", out)
			}
		})
	}
}

// End to end, through the id: the command must find the session from one glob
// rather than from a scan of every transcript on the machine, and an empty
// transcript must still leave the pane with a header and a zero exit.
func TestPreviewCommandResolvesAnID(t *testing.T) {
	// The candidate set is built from the live registry as well as the glob, so
	// the registry has to be this test's own or a session the developer happens
	// to be running joins the fixture.
	useTempRegistry(t)
	projects := newProjectsDir(t)
	writeTranscriptFile(t, filepath.Join(projects, "-Users-mash-src-osc8wrap"),
		"uuid-done.jsonl", previewTurns(4)...)
	writeTranscriptFile(t, filepath.Join(projects, "-Users-mash-src-other"), "uuid-empty.jsonl")
	t.Setenv("NO_COLOR", "1")

	tests := []struct {
		name string
		id   string
		want string
	}{
		{"a transcript with turns", "uuid-done", "turn-0"},
		{"a transcript with nothing in it", "uuid-empty", "(nothing readable in "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			// The trailing words are fzf's {q}: accepted, ignored, and never a
			// reason to fail.
			if code := runPreview([]string{tt.id, "fix", "the"}, &stdout, &stderr); code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
			}
			out := stdout.String()
			if !strings.Contains(out, tt.id) {
				t.Errorf("stdout does not name the session:\n%s", out)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("stdout is missing %q:\n%s", tt.want, out)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", stderr.String())
			}
		})
	}
}

// fzf respawns the preview on every cursor movement, so which lookup this
// command uses is the difference between a usable picker and an unusable one --
// candidatesFor is one glob and one bounded read, while the listing's own scan
// reads every transcript on the machine, 588 of them here. The two are
// indistinguishable in the output, so the call itself is what gets pinned: the
// fake below is the only thing that can answer for this id, and a runPreview
// that resolved the id any other way would find nothing and fail.
// A query is the case that most invites widening -- searching is what the
// listing does over the whole corpus -- so it is pinned alongside the plain one.
func TestPreviewCommandUsesTheNarrowLookup(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no query", []string{"uuid-narrow"}},
		{"a query that could have been searched for corpus-wide", []string{"uuid-narrow", "ratchet", "migration"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useTempRegistry(t)
			newProjectsDir(t)
			t.Setenv("NO_COLOR", "1")

			var asked []string
			orig := previewCandidates
			previewCandidates = func(id string) ([]Session, error) {
				asked = append(asked, id)
				return []Session{{ID: "uuid-narrow", Cwd: "/Users/mash/src/osc8wrap", Modified: previewBase}}, nil
			}
			t.Cleanup(func() { previewCandidates = orig })

			var stdout, stderr bytes.Buffer
			if code := runPreview(tt.args, &stdout, &stderr); code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
			}
			if len(asked) != 1 || asked[0] != "uuid-narrow" {
				t.Fatalf("the narrow lookup was asked %v, want it asked once for %q", asked, "uuid-narrow")
			}
			if !strings.Contains(stdout.String(), "uuid-narrow") {
				t.Errorf("the pane does not name the session the lookup returned:\n%s", stdout.String())
			}
		})
	}
}

// The same invariant seen from the corpus rather than from the seam: another
// session's transcript holds the query too, and a preview that searched the
// corpus instead of the one row would show it. The decoy is what fails this
// test if the query ever reaches the listing's scan.
func TestPreviewCommandSearchesOnlyTheSelectedSession(t *testing.T) {
	useTempRegistry(t)
	projects := newProjectsDir(t)
	writeTranscriptFile(t, filepath.Join(projects, "-Users-mash-src-osc8wrap"), "uuid-done.jsonl",
		userLine("/repo", "main", `"the ratchet migration, ours"`))
	writeTranscriptFile(t, filepath.Join(projects, "-Users-mash-src-other"), "uuid-decoy.jsonl",
		userLine("/other", "main", `"the ratchet migration, someone else's"`))
	t.Setenv("NO_COLOR", "1")

	var stdout, stderr bytes.Buffer
	if code := runPreview([]string{"uuid-done", "ratchet", "migration"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "ours") {
		t.Errorf("the pane does not show the selected session's match:\n%s", out)
	}
	if strings.Contains(out, "someone else's") || strings.Contains(out, "uuid-decoy") {
		t.Errorf("the pane reached a transcript that was not the selected one:\n%s", out)
	}
}

// An id that resolves to nothing is the one failure, and it has to name the id:
// this line is all the user sees in place of the pane.
func TestPreviewCommandRejectsAnUnknownID(t *testing.T) {
	useTempRegistry(t)
	newProjectsDir(t)

	tests := []struct {
		name string
		args []string
	}{
		{"no such session", []string{"uuid-nobody-has"}},
		{"a path pretending to be an id", []string{"../../etc/passwd"}},
		{"an empty id", []string{""}},
		{"no id at all", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runPreview(tt.args, &stdout, &stderr); code == 0 {
				t.Fatalf("exit code = 0, want a failure; stdout:\n%s", stdout.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", stdout.String())
			}
			if len(tt.args) > 0 && tt.args[0] != "" && !strings.Contains(stderr.String(), tt.args[0]) {
				t.Errorf("stderr = %q, want it to name the id %q", stderr.String(), tt.args[0])
			}
		})
	}
}
