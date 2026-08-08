package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// formatBase is an arbitrary fixed instant: the format cases care about the
// rendered shape of a timestamp, never about the wall clock.
var formatBase = time.Date(2026, 7, 26, 17, 40, 0, 0, time.UTC)

// liveRow and doneRow build unified records directly rather than through
// mergeSessions: the formatter's contract is with the shape of a Session, not
// with how one was assembled.
func liveRow(pid int, uuid, cwd, status string) Session {
	live := testLiveSession("osc8wrap-"+strconv.Itoa(pid), status, cwd, "0x1")
	live.Entry.WrapperPID = pid
	live.Entry.ChildPID = pid + 1
	live.SessionID = uuid
	return Session{ID: uuid, Live: &live, Cwd: cwd, Modified: formatBase}
}

func doneRow(uuid, cwd, prompt string) Session {
	return Session{ID: uuid, Cwd: cwd, Prompt: prompt, Modified: formatBase}
}

// render is the whole listing as a string, for the cases that expect success.
func render(t *testing.T, format string, sessions []Session) string {
	t.Helper()
	f, err := resolveFormat(format)
	if err != nil {
		t.Fatalf("resolveFormat(%q): %v", format, err)
	}
	var buf bytes.Buffer
	if err := f(&buf, sessions); err != nil {
		t.Fatalf("write: %v", err)
	}
	return buf.String()
}

// A format that cannot be resolved must fail before anything is written, so the
// resolver never touches a writer and a failed resolution cannot leave half a
// record behind for fzf to parse.
func TestResolveFormatRejectsBadFormats(t *testing.T) {
	for _, format := range []string{
		"nope",
		"peco",   // a ccsearch preset that does not exist here
		"--json", // the flag that --format replaced, typed as its value
		"{{.ID",  // an unterminated action
		"{{.ID | nosuchfunc}}",
	} {
		t.Run(format, func(t *testing.T) {
			f, err := resolveFormat(format)
			if err == nil {
				t.Fatalf("resolveFormat(%q) succeeded, want an error", format)
			}
			if f != nil {
				t.Error("a failed resolution must not hand back a writer")
			}
		})
	}
}

// The error for a value that is not a preset and not a template has to say what
// the fields are, since the user is being told their spelling is wrong.
func TestResolveFormatErrorNamesTheFields(t *testing.T) {
	_, err := resolveFormat("nope")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, field := range []string{".ID", ".Cwd", ".Tag", ".Summary", ".Date"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error does not mention %s: %v", field, err)
		}
	}
}

// A typo costs the user an error message and not a truncated listing: nothing
// reaches stdout. The other half of that promise -- that the format is
// resolved before the listing is even read -- is pinned by
// TestRunSessionsResolvesFormatBeforeReading.
func TestRunSessionsBadFormatWritesNothing(t *testing.T) {
	useTempRegistry(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	var stdout, stderr bytes.Buffer
	if code := runSessions([]string{"--format", "nope"}, &stdout, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing at all", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown --format") {
		t.Errorf("stderr = %q, want the unknown-format error", stderr.String())
	}
}

// Three tab-separated fields, always: a picker recovers the id from field 2 and
// the cwd from field 3, so a record that renders any other number of fields is
// one the user cannot act on.
func TestFzfPresetAlwaysEmitsThreeFields(t *testing.T) {
	live := liveRow(200, "uuid-live", "/Users/mash/src/a", "waiting")
	live.Live.WaitingFor = "input needed"
	live.Branch = "feature/x"
	live.Root = "/Users/mash/src/a"
	live.Worktree = "/Users/mash/src/a"
	live.Prompt = "fix the parser"

	finished := doneRow("uuid-done", "/Users/mash/src/b", "write the README")
	finished.Branch = "main"
	finished.Root = "/Users/mash/src/b"
	finished.Worktree = "/Users/mash/src/b"

	// A wrapper around something claude has not claimed: no UUID, so the record
	// names itself by wrapper pid.
	noUUID := liveRow(4242, "", "/Users/mash/src/c", "")

	// The id and the cwd are not osc8wrap's own text either -- the UUID comes
	// out of a transcript filename and the cwd out of the transcript body --
	// and they are the two fields a picker acts on. A tab surviving in one of
	// them shifts every field after it, so `open` is handed a fragment of a
	// path and the user lands somewhere else without being told.
	hostile := liveRow(300, "uuid-\tforged", "/Users/mash/src/d\te\nf", "busy")

	// A --grep row carries an excerpt from the middle of a transcript. It lives
	// inside field 1 precisely so it cannot renumber the two fields a picker
	// acts on, which is only true while it is sanitized.
	matched := doneRow("uuid-match", "/Users/mash/src/e", "write the README")
	matched.Match = "you: ratchet\tthe\nmigration"

	tests := []struct {
		name            string
		s               Session
		wantID, wantCwd string
	}{
		{"live", live, "uuid-live", "/Users/mash/src/a"},
		{"finished", finished, "uuid-done", "/Users/mash/src/b"},
		{"live without a uuid", noUUID, "4242", "/Users/mash/src/c"},
		{"a tab in the id and a tab and a newline in the cwd", hostile,
			"uuid- forged", "/Users/mash/src/d e f"},
		{"a grep excerpt with a tab and a newline in it", matched,
			"uuid-match", "/Users/mash/src/e"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := render(t, "", []Session{tt.s})
			if !strings.HasSuffix(out, "\n") {
				t.Fatalf("record %q is not newline terminated", out)
			}
			if n := strings.Count(out, "\n"); n != 1 {
				t.Fatalf("record %q holds %d newlines, want 1", out, n)
			}
			fields := strings.Split(strings.TrimSuffix(out, "\n"), "\t")
			if len(fields) != 3 {
				t.Fatalf("record %q split into %d fields, want 3", out, len(fields))
			}
			if fields[1] != tt.wantID {
				t.Errorf("field 2 = %q, want the id %q", fields[1], tt.wantID)
			}
			if fields[2] != tt.wantCwd {
				t.Errorf("field 3 = %q, want the cwd %q", fields[2], tt.wantCwd)
			}
			// The excerpt is the reason the row is in a --grep listing, so it
			// has to be visible in the column the picker displays.
			if tt.s.Match != "" && !strings.Contains(fields[0], "ratchet the migration") {
				t.Errorf("field 1 = %q, want it to show the grep excerpt", fields[0])
			}
		})
	}
}

// Sanitization is not a property of the descriptive columns: every string in a
// record reaches it from a transcript, a claude session file or an argv, and
// each is pinned here by name so that dropping sanitizeField from any single
// one of them fails a test rather than quietly forging a field.
func TestEveryInterpolatedFieldIsSanitized(t *testing.T) {
	// A tab, a newline and an OSC title sequence: the first two forge the TSV
	// structure, the third repaints the terminal the record is printed to.
	const hostile = "a\tb\nc\x1b]0;x\x07d"
	const clean = "a b c]0;xd"

	s := liveRow(200, "id-"+hostile, "/src/cwd-"+hostile, "busy")
	s.Live.Status = "status-" + hostile
	s.Live.WaitingFor = "wf-" + hostile
	s.Live.Name = "name-" + hostile
	s.Live.Entry.Command = []string{"cmd-" + hostile}
	s.Branch = "branch-" + hostile
	s.Root = "/src/root-" + hostile
	s.Worktree = "/src/root-" + hostile + "/wt-" + hostile
	s.Prompt = "prompt-" + hostile
	// The excerpt is the worst case of the lot: the other transcript-sourced
	// fields come from the head of a file, this one from wherever the query
	// happened to hit, so it is the likeliest to be sitting in pasted output.
	s.Match = "match-" + hostile

	tests := []struct{ field, want string }{
		{".ID", "id-" + clean},
		{".Cwd", "/src/cwd-" + clean},
		{".Status", "status-" + clean},
		{".WaitingFor", "wf-" + clean},
		{".Repo", "root-" + clean},
		{".Worktree", "wt-" + clean},
		{".Branch", "branch-" + clean},
		{".Root", "/src/root-" + clean},
		{".Prompt", "prompt-" + clean},
		{".Match", "match-" + clean},
		{".Summary", "prompt-" + clean},
		{".Name", "name-" + clean},
		{".Command", "cmd-" + clean},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			if got := render(t, "{{"+tt.field+"}}", []Session{s}); got != tt.want {
				t.Errorf("%s rendered %q, want %q", tt.field, got, tt.want)
			}
		})
	}

	// And the same record through the json preset, whose strings are the same
	// view's but whose keys are spelled separately.
	var records []map[string]any
	if err := json.Unmarshal([]byte(render(t, "json", []Session{s})), &records); err != nil {
		t.Fatalf("the json preset did not produce valid JSON: %v", err)
	}
	for key, want := range map[string]string{
		"id": "id-" + clean, "cwd": "/src/cwd-" + clean, "repo": "root-" + clean,
		"worktree": "wt-" + clean, "branch": "branch-" + clean, "root": "/src/root-" + clean,
		"name": "name-" + clean, "match": "match-" + clean,
	} {
		if got := records[0][key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
}

// A timestamp osc8wrap did not author must cost its own record a date column
// and nothing more. time.Time refuses to marshal a year outside [0,9999], so
// one corrupt updatedAt or one bad transcript mtime would otherwise take the
// ENTIRE --format json listing to zero bytes and exit 1; the same year formats
// 17 columns wide and shifts every field of the fzf preset's fixed layout.
func TestOutOfRangeModifiedDegradesToNoDate(t *testing.T) {
	future := doneRow("uuid-future", "/src/a", "hello")
	future.Modified = time.UnixMilli(999999999999999) // year 33658
	past := doneRow("uuid-past", "/src/b", "hello")
	past.Modified = time.Unix(-100000000000000, 0) // a year far below zero
	fine := doneRow("uuid-fine", "/src/c", "hello")

	sessions := []Session{future, past, fine}

	var records []map[string]any
	if err := json.Unmarshal([]byte(render(t, "json", sessions)), &records); err != nil {
		t.Fatalf("the json preset did not produce valid JSON: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records, want all 3", len(records))
	}
	for _, i := range []int{0, 1} {
		if got := records[i]["modified"]; got != "0001-01-01T00:00:00Z" {
			t.Errorf("record %d modified = %#v, want the zero time", i, got)
		}
	}
	if got := records[2]["modified"]; got != "2026-07-26T17:40:00Z" {
		t.Errorf("the usable timestamp was folded too: %#v", got)
	}

	// The date column is fixed width by contract, so an unrenderable instant
	// leaves it empty rather than widening it.
	for _, s := range sessions {
		date := render(t, "{{.Date}}", []Session{s})
		if len(date) != 0 && len(date) != len(dateLayout) {
			t.Errorf("%s rendered a %d-column date %q, want 0 or %d",
				s.ID, len(date), date, len(dateLayout))
		}
	}
	if got := render(t, "{{.Date}}", []Session{future}); got != "" {
		t.Errorf(".Date = %q, want nothing at all", got)
	}
	if got := render(t, "{{.Date}}", []Session{fine}); got != "2026-07-26 17:40" {
		t.Errorf(".Date = %q, want the usable timestamp rendered", got)
	}
}

// The format is resolved before the listing is READ, not merely before it is
// written. A typo has to be reported as a typo even when the read behind it
// would have failed too, or the user is sent to debug their registry.
func TestRunSessionsResolvesFormatBeforeReading(t *testing.T) {
	// XDG_STATE_HOME pointed at a regular file makes the registry directory
	// impossible to create or read, so listSessions fails outright.
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", file)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	// The fixture is load-bearing: without a bad format the run fails on the
	// registry, which is the error the bad-format run must NOT produce.
	var stdout, stderr bytes.Buffer
	if code := runSessions(nil, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d with an unusable registry, want 1", code)
	}
	if !strings.Contains(stderr.String(), "registry") {
		t.Fatalf("stderr = %q, want the registry failure", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runSessions([]string{"--format", "nope"}, &stdout, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing at all", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown --format") {
		t.Errorf("stderr = %q, want the unknown-format error", stderr.String())
	}
	if strings.Contains(stderr.String(), "registry") {
		t.Errorf("stderr = %q: the listing was read before the format was resolved", stderr.String())
	}
}

// A transcript is other people's text: a prompt containing a tab would forge a
// fourth field, a newline a second record, and an OSC sequence would repaint
// the pane the record is displayed in.
func TestFzfPresetSurvivesAHostilePrompt(t *testing.T) {
	s := doneRow("uuid-done", "/Users/mash/src/a", "before\t\n\x1b]0;x\x07after")

	out := render(t, "", []Session{s})
	if n := strings.Count(out, "\n"); n != 1 {
		t.Fatalf("record %q holds %d newlines, want 1", out, n)
	}
	fields := strings.Split(strings.TrimSuffix(out, "\n"), "\t")
	if len(fields) != 3 {
		t.Fatalf("record %q split into %d fields, want 3", out, len(fields))
	}
	// The escape and the BEL are gone; what the sequence carried between them
	// stays as inert text, which is all it can be once nothing introduces it.
	if !strings.Contains(fields[0], "before  ]0;xafter") {
		t.Errorf("display field = %q, want the tab and newline folded to spaces", fields[0])
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("record %q still carries an escape sequence", out)
	}
}

// A record is rendered into a buffer and written only once it is whole, so a
// field that fails costs that record and nothing else. The record before it is
// correct and the picker is waiting on it; the record that failed must be
// absent rather than half-written, since a display column with no id behind it
// is a row the user can select and not act on.
func TestTemplateErrorLeavesEarlierRecordsIntact(t *testing.T) {
	f, err := resolveFormat(`{{.ID}}{{if .Live}}{{.Nope}}{{end}}` + "\n")
	if err != nil {
		t.Fatalf("resolveFormat: %v", err)
	}

	var buf bytes.Buffer
	sessions := []Session{
		doneRow("uuid-done", "/Users/mash/src/a", "hello"),
		liveRow(200, "uuid-live", "/Users/mash/src/b", "busy"),
		doneRow("uuid-third", "/Users/mash/src/c", "never reached"),
	}
	if err := f(&buf, sessions); err == nil {
		t.Fatal("write succeeded, want the failing record to report an error")
	} else if !strings.Contains(err.Error(), "uuid-live") {
		t.Errorf("error %v does not name the record that failed", err)
	}
	if got := buf.String(); got != "uuid-done\n" {
		t.Errorf("wrote %q, want only the record that rendered whole", got)
	}
}

// Words rather than glyphs, because fzf searches the display line: the user
// filters the list by typing "wait".
func TestSessionTag(t *testing.T) {
	tests := []struct {
		name string
		s    Session
		want string
	}{
		{"waiting", liveRow(1, "u", "/tmp", "waiting"), "WAIT"},
		{"busy", liveRow(1, "u", "/tmp", "busy"), "BUSY"},
		{"idle", liveRow(1, "u", "/tmp", "idle"), "IDLE"},
		{"live with no status", liveRow(1, "u", "/tmp", ""), "LIVE"},
		{"live with a status claude has not taught us", liveRow(1, "u", "/tmp", "compacting"), "LIVE"},
		{"finished", doneRow("u", "/tmp", "hello"), "done"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionTag(tt.s); got != tt.want {
				t.Errorf("sessionTag = %q, want %q", got, tt.want)
			}
			if len(tt.want) != 4 {
				t.Errorf("tag %q is not four columns wide", tt.want)
			}
			if !strings.HasPrefix(render(t, "", []Session{tt.s}), tt.want) {
				t.Errorf("the display line does not start with the tag %q", tt.want)
			}
		})
	}
}

// Programs consume the json preset, so an empty listing is still an array.
func TestJSONPresetEmptyList(t *testing.T) {
	if got := render(t, "json", nil); got != "[]\n" {
		t.Errorf("empty listing = %q, want %q", got, "[]\n")
	}
}

func TestJSONPreset(t *testing.T) {
	live := liveRow(200, "uuid-live", "/Users/mash/src/a", "waiting")
	live.Live.WaitingFor = "input\tneeded"
	live.Branch = "feature/x"
	live.Root = "/Users/mash/src/a"
	live.Worktree = "/Users/mash/src/a/.worktrees/x"
	live.Prompt = "fix\tthe parser"

	var records []map[string]any
	if err := json.Unmarshal([]byte(render(t, "json", []Session{live, doneRow("uuid-done", "/tmp/b", "")})), &records); err != nil {
		t.Fatalf("the json preset did not produce valid JSON: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}

	want := map[string]any{
		"id":          "uuid-live",
		"live":        true,
		"tag":         "WAIT",
		"status":      "waiting",
		"waiting_for": "input needed",
		"cwd":         "/Users/mash/src/a",
		"repo":        "a",
		"worktree":    "x",
		"branch":      "feature/x",
		"prompt":      "fix the parser",
		"summary":     "fix the parser",
		"wrapper_pid": float64(200),
		"navigable":   true,
	}
	for key, value := range want {
		if got := records[0][key]; got != value {
			t.Errorf("%s = %#v, want %#v", key, got, value)
		}
	}
	if records[1]["live"] != false || records[1]["tag"] != "done" {
		t.Errorf("finished record = %#v", records[1])
	}
}

// osc8wrap wraps commands that are not claude, and a session that never
// produced a prompt is normal, so the descriptive column falls back twice
// rather than rendering blank.
func TestSummaryFallsBack(t *testing.T) {
	prompted := liveRow(200, "uuid", "/tmp", "busy")
	prompted.Prompt = "fix the parser"

	named := liveRow(200, "uuid", "/tmp", "busy")

	argv := liveRow(200, "uuid", "/tmp", "busy")
	argv.Live.Name = ""
	argv.Live.Entry.Command = []string{"go", "test", "./..."}

	tests := []struct {
		name string
		s    Session
		want string
	}{
		{"prompt", prompted, "fix the parser"},
		{"claude's own name", named, "osc8wrap-200"},
		{"the wrapped argv", argv, "go test ./..."},
		{"a finished session that never prompted", doneRow("uuid", "/tmp", ""), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summaryOf(tt.s); got != tt.want {
				t.Errorf("summaryOf = %q, want %q", got, tt.want)
			}
		})
	}
}

// The worktree basename alone is useless -- of which repo? -- so the display
// pairs it with the repository, and a directory git can no longer resolve still
// gets a name rather than an empty column.
func TestRepoAndWorktree(t *testing.T) {
	tests := []struct {
		name         string
		s            Session
		repo, worktr string
	}{
		{
			"main checkout",
			Session{Cwd: "/src/nature-server", Root: "/src/nature-server", Worktree: "/src/nature-server"},
			"nature-server", "",
		},
		{
			"subdirectory of a main checkout",
			Session{Cwd: "/src/nature-server/models", Root: "/src/nature-server", Worktree: "/src/nature-server"},
			"nature-server", "",
		},
		{
			"linked worktree",
			Session{
				Cwd:      "/src/nature-server/.worktrees/feat-ev-charge",
				Root:     "/src/nature-server",
				Worktree: "/src/nature-server/.worktrees/feat-ev-charge",
			},
			"nature-server", "feat-ev-charge",
		},
		{
			"deleted worktree, nothing left to resolve",
			Session{Cwd: "/src/nature-server/.worktrees/gone"},
			"gone", "",
		},
		{"no cwd at all", Session{}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, worktree := repoAndWorktree(tt.s)
			if repo != tt.repo || worktree != tt.worktr {
				t.Errorf("repoAndWorktree = (%q, %q), want (%q, %q)", repo, worktree, tt.repo, tt.worktr)
			}
		})
	}
}

// The picker's sanitizeCell substituted "?" for a control character. These
// records are searched by fzf rather than laid out in columns, so the character
// is dropped instead -- a run of question marks is noise a query has to match
// past -- and only the tab and the newline, which carry the record's structure,
// are folded to a space.
func TestSanitizeField(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text is untouched", "fix the parser", "fix the parser"},
		{"tabs fold to spaces", "a\tb", "a b"},
		{"newlines fold to spaces", "a\nb", "a b"},
		{"escape sequences are dropped", "a\x1b[2Jb", "a[2Jb"},
		{"an OSC title sequence cannot survive", "\x1b]0;title\x07x", "]0;titlex"},
		{"DEL is dropped", "a\x7fb", "ab"},
		{"C1 controls are dropped", "a\u009db", "ab"},
		{"multibyte text is kept whole", "プロジェクト管理", "プロジェクト管理"},
		{"nothing is substituted for what was dropped", "a\x00\x01\x02b", "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeField(tt.in); got != tt.want {
				t.Errorf("sanitizeField(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// stripControl keeps the two characters sanitizeField folds, because the
// preview pane wants them: a transcript turn is displayed over several lines.
func TestStripControlKeepsTabsAndNewlines(t *testing.T) {
	if got := stripControl("a\tb\nc\x1bd"); got != "a\tb\ncd" {
		t.Errorf("stripControl = %q, want %q", got, "a\tb\ncd")
	}
}

// Every field the usage text advertises has to exist on the view, or the first
// thing a user copies out of --help fails at the second record.
func TestEveryDocumentedFieldRenders(t *testing.T) {
	s := liveRow(200, "uuid-live", "/Users/mash/src/a", "waiting")
	s.Root = "/Users/mash/src/a"
	s.Worktree = "/Users/mash/src/a"
	s.Branch = "main"
	s.Prompt = "fix the parser"

	for _, field := range strings.Fields(formatFields) {
		t.Run(field, func(t *testing.T) {
			render(t, "{{"+field+"}}"+"\n", []Session{s})
		})
		if !strings.Contains(sessionsUsage, field) {
			t.Errorf("%s is missing from the sessions usage text", field)
		}
	}
}
