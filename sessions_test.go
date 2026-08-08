package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Dispatch reads the boundary parseArgs already found, so these cases pin both
// halves at once: where options stop, and whether what follows is the
// subcommand or a command to wrap. Arguments parseArgs rejects are covered by
// TestUnknownOptionBeforeSubcommand, which needs a subprocess because
// parseArgs exits.
func TestLookupSubcommand(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantCmdArgs []string
		want        bool
	}{
		{"no arguments", nil, nil, false},
		{"only options", []string{"--scheme=cursor", "--no-symbol-links"}, nil, false},
		{"bare subcommand", []string{"sessions"}, []string{"sessions"}, true},
		{"open", []string{"open", "abc"}, []string{"open", "abc"}, true},
		{"preview", []string{"preview", "abc"}, []string{"preview", "abc"}, true},
		{"subcommand after options", []string{"--scheme=cursor", "sessions"}, []string{"sessions"}, true},
		{"subcommand with its own flags", []string{"sessions", "--format", "json"}, []string{"sessions", "--format", "json"}, true},
		// The point of the "--" rule: a wrapped program may be called
		// "sessions" and must still be wrapped, not dispatched.
		{"after double dash", []string{"--", "sessions"}, []string{"sessions"}, false},
		{"double dash with options", []string{"--scheme=zed", "--", "sessions"}, []string{"sessions"}, false},
		{"wrapped command", []string{"go", "build", "./..."}, []string{"go", "build", "./..."}, false},
		// Not hypothetical: open(1) is a real macOS program, and the table is
		// meant to grow, so wrapping a command whose name it may later claim
		// has to keep working.
		{"double dash before open", []string{"--", "open", "."}, []string{"open", "."}, false},
		{"double dash before preview", []string{"--", "preview", "x"}, []string{"preview", "x"}, false},
		// A flag belonging to the wrapped command sits past the boundary and
		// must not be mistaken for one of osc8wrap's own.
		{"wrapped command with flags", []string{"grep", "-rn", "TODO"}, []string{"grep", "-rn", "TODO"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, cmdArgs, sawDoubleDash := parseArgs(tt.args)
			if !equalArgs(cmdArgs, tt.wantCmdArgs) {
				t.Errorf("parseArgs(%q) cmdArgs = %q, want %q", tt.args, cmdArgs, tt.wantCmdArgs)
			}
			if _, got := lookupSubcommand(cmdArgs, sawDoubleDash); got != tt.want {
				t.Errorf("lookupSubcommand(%q, %v) = %v, want %v", cmdArgs, sawDoubleDash, got, tt.want)
			}
		})
	}
}

// A mistyped option before the subcommand must reach parseArgs' "unknown
// option" error rather than being skipped on the way to dispatch. parseArgs
// exits the process to report it, so this runs run() in a subprocess.
func TestUnknownOptionBeforeSubcommand(t *testing.T) {
	if args := os.Getenv("OSC8WRAP_TEST_ARGS"); args != "" {
		os.Args = append([]string{"osc8wrap"}, strings.Fields(args)...)
		os.Exit(run())
	}

	for _, args := range []string{
		"--nope sessions",
		"-x sessions",
		// "--scheme" without "=" is not an option parseArgs knows.
		"--scheme cursor sessions",
		"--nope go build",
	} {
		t.Run(args, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestUnknownOptionBeforeSubcommand$")
			cmd.Env = append(os.Environ(), "OSC8WRAP_TEST_ARGS="+args)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("osc8wrap %s exited 0, want the unknown-option error; output:\n%s", args, out)
			}
			if !strings.Contains(string(out), "unknown option") {
				t.Errorf("osc8wrap %s did not report an unknown option; output:\n%s", args, out)
			}
		})
	}
}

// testLiveSession builds the live half of a record the way joinSessions would
// have. It survives sessionui.go: the picker it was written for is gone, but
// the shape of a running session is still what the formatter is fed.
func testLiveSession(name, status, cwd, surfaceID string) LiveSession {
	return LiveSession{
		Entry: RegistryEntry{
			WrapperPID: 100,
			ChildPID:   101,
			TTY:        "/dev/ttys001",
			SurfaceID:  surfaceID,
			Cwd:        cwd,
			Command:    []string{"claude"},
		},
		Name:   name,
		Status: status,
	}
}

// Both spellings of every valued flag are pinned: osc8wrap's own option table
// is "="-only and ccsearch is space-only, so users type whichever they know.
func TestParseSessionsArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    sessionsOptions
		wantErr bool
	}{
		{name: "no arguments"},
		{name: "format with a space", args: []string{"--format", "json"}, want: sessionsOptions{format: "json"}},
		{name: "format with an equals", args: []string{"--format=json"}, want: sessionsOptions{format: "json"}},
		{name: "template as a value", args: []string{"--format", "{{.ID}}"}, want: sessionsOptions{format: "{{.ID}}"}},
		{name: "one query word", args: []string{"migration"}, want: sessionsOptions{query: "migration"}},
		// The two spellings of a multi-word query have to arrive as the same
		// string, because the terms are ANDed either way: the shell splitting
		// them is not a different request from quoting them.
		{name: "several query words", args: []string{"raw", "sql"}, want: sessionsOptions{query: "raw sql"}},
		{name: "one quoted query", args: []string{"raw sql"}, want: sessionsOptions{query: "raw sql"}},
		{name: "format before the query", args: []string{"--format=fzf", "sql"}, want: sessionsOptions{format: "fzf", query: "sql"}},
		{name: "format after the query", args: []string{"sql", "--format", "fzf"}, want: sessionsOptions{format: "fzf", query: "sql"}},
		{name: "format between query words", args: []string{"raw", "--format=json", "sql"}, want: sessionsOptions{format: "json", query: "raw sql"}},
		// "--" is the escape hatch for a term that looks like an option; after
		// it even --format is text, which is the whole point of a boundary.
		{name: "double dash makes a dash a term", args: []string{"--", "-x"}, want: sessionsOptions{query: "-x"}},
		{name: "double dash ends the options", args: []string{"--format", "json", "--", "--format"}, want: sessionsOptions{format: "json", query: "--format"}},
		{name: "a bare double dash is not a term", args: []string{"--"}, want: sessionsOptions{}},
		{name: "help", args: []string{"--help"}, want: sessionsOptions{help: true}},
		{name: "short help", args: []string{"-h"}, want: sessionsOptions{help: true}},
		{name: "unknown option", args: []string{"--nope"}, wantErr: true},
		{name: "the removed json flag", args: []string{"--json"}, wantErr: true},
		// --live and --limit were removed outright rather than accepted and
		// ignored, so they are unknown options like any other typo. Pinned here so
		// nobody quietly restores them as a permissive default.
		{name: "the removed live flag", args: []string{"--live"}, wantErr: true},
		{name: "the removed limit flag", args: []string{"--limit", "5"}, wantErr: true},
		{name: "the removed limit flag with an equals", args: []string{"--limit=5"}, wantErr: true},
		// --grep became the positional query and was deleted rather than aliased:
		// it has to fail like any other unknown option, so that a picker still
		// passing it is told, not quietly handed an unfiltered listing.
		{name: "the removed grep flag", args: []string{"--grep", "sql"}, wantErr: true},
		{name: "the removed grep flag with an equals", args: []string{"--grep=sql"}, wantErr: true},
		{name: "format without a value", args: []string{"--format"}, wantErr: true},
		{name: "empty format", args: []string{"--format="}, wantErr: true},
		// flagValue exists so both spellings behave identically; an empty value
		// written with a space must not slip through to the default preset
		// while the same value written with "=" is rejected.
		{name: "empty format as a separate word", args: []string{"--format", ""}, wantErr: true},
		// Whitespace is not a term, and no longer an error either: it is the
		// keystroke-by-keystroke picker's starting state, which lists everything.
		{name: "whitespace-only query", args: []string{"   "}, want: sessionsOptions{query: "   "}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSessionsArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSessionsArgs(%q) succeeded, want an error", tt.args)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("parseSessionsArgs(%q) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

// runSessionsFixture stands up a listing of a known shape: two live sessions --
// one waiting, one busy -- and three finished ones an hour apart. The live half
// has to name processes that really are running, since loadRegistry prunes an
// entry whose wrapper pid is gone.
func runSessionsFixture(t *testing.T) {
	t.Helper()
	sessionsDir := setupClaudeSessions(t, map[string]string{
		"58515.json": realSessionJSON,
		"99999.json": `{"pid":99999,"sessionId":"uuid-live-2","status":"busy","updatedAt":1785026000000}`,
	})
	dir := useTempRegistry(t)
	writeEntry(t, dir, RegistryEntry{
		WrapperPID: os.Getpid(), ChildPID: 58515,
		TTY: "/dev/ttys011", Command: []string{"claude"},
	})
	writeEntry(t, dir, RegistryEntry{
		WrapperPID: os.Getppid(), ChildPID: 99999,
		TTY: "/dev/ttys012", Command: []string{"claude"},
	})

	projects := filepath.Join(filepath.Dir(sessionsDir), "projects")
	now := time.Now()
	for i, id := range []string{"uuid-done-1", "uuid-done-2", "uuid-done-3"} {
		path := writeTranscriptFile(t, filepath.Join(projects, "-src-a"), id+".jsonl",
			userLine("/src/a", "main", strconv.Quote("hello "+id)))
		// A finished session is dated by its mtime, so the three are spaced an
		// hour apart to make "newest first" an order rather than a coin flip.
		mtime := now.Add(-time.Duration(3-i) * time.Hour)
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

// sessionRows renders the listing and splits each record into its tag and its
// id: the fzf preset opens every finished record with "done" and every live one
// with an upper-case tag, and field 2 is the id.
func sessionRows(t *testing.T, args ...string) (tags, ids []string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := runSessions(args, &stdout, &stderr); code != 0 {
		t.Fatalf("runSessions(%q) = %d, stderr %q", args, code, stderr.String())
	}
	out := stdout.String()
	if out == "" {
		return nil, nil
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("listing %q is not newline terminated", out)
	}
	for _, row := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		fields := strings.Split(row, "\t")
		if len(fields) != 3 {
			t.Fatalf("record %q has %d tab-separated fields, want 3", row, len(fields))
		}
		tag, _, _ := strings.Cut(fields[0], " ")
		tags = append(tags, tag)
		ids = append(ids, fields[1])
	}
	return tags, ids
}

// The listing with no flags at all is the whole product: both halves merged,
// live before finished, and newest first inside each half. Nothing narrows it,
// so this is also the only place the merge order is pinned.
func TestRunSessionsListsBothHalvesLiveFirst(t *testing.T) {
	runSessionsFixture(t)

	tags, ids := sessionRows(t)
	wantTags := []string{"WAIT", "BUSY", "done", "done", "done"}
	if !equalArgs(tags, wantTags) {
		t.Fatalf("osc8wrap sessions listed tags %q, want %q (live first, waiting before busy)",
			tags, wantTags)
	}
	// The live ids come from the claude session files; the finished ones are
	// newest first, which is the reverse of the order the fixture wrote them in.
	wantIDs := []string{
		"b2dae39b-7833-4a90-a6d2-de2edb061a7f", "uuid-live-2",
		"uuid-done-3", "uuid-done-2", "uuid-done-1",
	}
	if !equalArgs(ids, wantIDs) {
		t.Errorf("osc8wrap sessions listed ids %q, want %q", ids, wantIDs)
	}
}

// --live, --limit and --grep were deleted, not deprecated. Passing any of them
// has to reach parseSessionsArgs' unknown-option error, print the usage text,
// and leave stdout untouched -- a picker reading the listing must not receive
// prose. It is also the general rule for a mistyped flag: an unknown "-"
// argument before "--" is an error rather than a search term, so a typo cannot
// come back as a confident empty listing.
func TestRunSessionsRejectsTheRemovedFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--live"}, {"--limit", "5"}, {"--limit=0"},
		{"--grep", "ratchet"}, {"--grep=ratchet"},
		{"--fromat", "json", "ratchet"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runSessions(args, &stdout, &stderr); code != 1 {
				t.Fatalf("runSessions(%q) = %d, want 1", args, code)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", stdout.String())
			}
			if !strings.Contains(stderr.String(), "unknown option") {
				t.Errorf("stderr = %q, want the unknown-option error", stderr.String())
			}
			if !strings.Contains(stderr.String(), "Usage: osc8wrap sessions") {
				t.Errorf("stderr = %q, want the usage text", stderr.String())
			}
		})
	}
}

// grepFixture stands up the four rows the query has to tell apart: a live and a
// finished session whose conversation holds the term, and one of each whose
// conversation does not. The live half has to name processes that really are
// running, since loadRegistry prunes an entry whose wrapper pid is gone.
func grepFixture(t *testing.T) {
	t.Helper()
	sessionsDir := setupClaudeSessions(t, map[string]string{
		"58515.json": `{"pid":58515,"sessionId":"uuid-live-hit","status":"waiting","updatedAt":1785026541307}`,
		"99999.json": `{"pid":99999,"sessionId":"uuid-live-miss","status":"busy","updatedAt":1785026000000}`,
	})
	dir := useTempRegistry(t)
	writeEntry(t, dir, RegistryEntry{
		WrapperPID: os.Getpid(), ChildPID: 58515,
		TTY: "/dev/ttys011", Command: []string{"claude"},
	})
	writeEntry(t, dir, RegistryEntry{
		WrapperPID: os.Getppid(), ChildPID: 99999,
		TTY: "/dev/ttys012", Command: []string{"claude"},
	})

	projects := filepath.Join(filepath.Dir(sessionsDir), "projects")
	for _, tc := range []struct{ id, text string }{
		{"uuid-live-hit", "port the ratchet migration"},
		{"uuid-live-miss", "something else entirely"},
		{"uuid-done-hit", "revert the ratchet migration"},
		{"uuid-done-miss", "nothing of the sort"},
	} {
		writeTranscriptFile(t, filepath.Join(projects, "-src-a"), tc.id+".jsonl",
			userLine("/src/a", "main", strconv.Quote(tc.text)))
	}
}

// grepRows renders the listing as "id<TAB>match" pairs, which is the pair the
// filter is judged on: which rows survived, and whether each says why.
func grepRows(t *testing.T, args ...string) map[string]string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args = append(args, "--format", "{{.ID}}\t{{.Match}}{{\"\\n\"}}")
	if code := runSessions(args, &stdout, &stderr); code != 0 {
		t.Fatalf("runSessions(%q) = %d, stderr %q", args, code, stderr.String())
	}
	rows := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		id, match, _ := strings.Cut(line, "\t")
		rows[id] = match
	}
	return rows
}

// The query has to reach the listing, drop what did not match on BOTH halves of
// it, and say why each survivor is there. Dropping a live row is the deliberate
// part: live rows sort to the top, so keeping the ones that did not match would
// put exactly the sessions the user filtered out above the ones they wanted.
func TestRunSessionsGrepFiltersBothHalves(t *testing.T) {
	grepFixture(t)

	all := grepRows(t)
	if len(all) != 4 {
		t.Fatalf("unfiltered listing has %d rows, want 4: %v", len(all), all)
	}
	for id, match := range all {
		if match != "" {
			t.Errorf("%s carries a match %q with nothing searched for", id, match)
		}
	}

	got := grepRows(t, "ratchet migration")
	if len(got) != 2 {
		t.Fatalf("the query listed %d rows, want the 2 that match: %v", len(got), got)
	}
	for _, id := range []string{"uuid-live-hit", "uuid-done-hit"} {
		match, ok := got[id]
		if !ok {
			t.Fatalf("the query dropped %s, which matches: %v", id, got)
		}
		if !strings.Contains(match, "ratchet") {
			t.Errorf("%s .Match = %q, want the excerpt that explains the hit", id, match)
		}
	}
}

// A live session with no UUID -- the wrapped command is not claude, or claude
// has not published its session file yet -- has no transcript to search, but it
// does have a location, so it is a normal row of the filter: kept when a term
// names where it runs, dropped when a term needs a conversation it does not
// have. It carries no excerpt either way, because there is no body to quote.
func TestRunSessionsGrepMatchesTheLiveSessionWithNoUUIDOnMetadata(t *testing.T) {
	// No claude session file at all, so joinSessions leaves SessionID empty.
	sessionsDir := setupClaudeSessions(t, nil)
	dir := useTempRegistry(t)
	writeEntry(t, dir, RegistryEntry{
		WrapperPID: os.Getpid(), ChildPID: 58515, Cwd: "/src/toolbox",
		TTY: "/dev/ttys011", Command: []string{"go", "test", "./..."},
	})
	writeTranscriptFile(t, filepath.Join(filepath.Dir(sessionsDir), "projects", "-src-a"),
		"uuid-done-hit.jsonl", userLine("/src/a", "main", strconv.Quote("revert the ratchet migration")))

	pid := strconv.Itoa(os.Getpid())
	all := grepRows(t)
	if _, ok := all[pid]; !ok {
		t.Fatalf("the unfiltered listing lost the pid-named live row: %v", all)
	}

	got := grepRows(t, "ratchet")
	if _, ok := got[pid]; ok {
		t.Errorf("the query kept a live row with neither the term nor a transcript: %v", got)
	}
	if _, ok := got["uuid-done-hit"]; !ok {
		t.Errorf("the query dropped the row that does match: %v", got)
	}

	got = grepRows(t, "toolbox")
	match, ok := got[pid]
	if !ok {
		t.Fatalf("the query dropped the live row its own cwd names: %v", got)
	}
	if match != "" {
		t.Errorf("%s .Match = %q, want empty: nothing in a conversation matched", pid, match)
	}
	if _, ok := got["uuid-done-hit"]; ok {
		t.Errorf("the query 'toolbox' kept a row from somewhere else: %v", got)
	}
}

// The headline case of the metadata half: one term names the repository and the
// other names what was said in it. Neither half of the session answers it alone.
func TestRunSessionsGrepMixesMetadataAndBodyTerms(t *testing.T) {
	grepFixture(t)

	got := grepRows(t, "src ratchet")
	if len(got) != 2 {
		t.Fatalf("the query listed %d rows, want the 2 whose cwd and conversation both match: %v",
			len(got), got)
	}
	for _, id := range []string{"uuid-live-hit", "uuid-done-hit"} {
		match, ok := got[id]
		if !ok {
			t.Fatalf("the query dropped %s, which matches: %v", id, got)
		}
		// The excerpt is anchored on the term the body actually supplied, not on
		// the first term of the query -- "src" is nowhere in the conversation.
		if !strings.Contains(match, "ratchet") {
			t.Errorf("%s .Match = %q, want the excerpt around the body term", id, match)
		}
	}
}

// sameIDs compares two id lists as sets. The listing's order is pinned by
// TestRunSessionsListsBothHalvesLiveFirst; what a query is judged on is which
// rows survived it, and the fixture's finished transcripts share an mtime, so
// ordering them here would be pinning a tie-break rather than the filter.
func sameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	count := map[string]int{}
	for _, id := range got {
		count[id]++
	}
	for _, id := range want {
		count[id]--
	}
	for _, n := range count {
		if n != 0 {
			return false
		}
	}
	return true
}

// The query is positional, so the argument vector is what is under test: every
// non-option word joins into one query, --format may sit anywhere among them,
// and "--" hands a word starting with "-" to the search instead of to the
// option table. None of these is an error -- not the absent query, not the
// whitespace one, not a term that matches nothing -- because a picker bound to
// every keystroke sends all three on the way to a real query, and the empty
// case is exactly what let it drop its "|| osc8wrap sessions" fallback.
func TestRunSessionsQueryIsPositional(t *testing.T) {
	grepFixture(t)

	everything := []string{"uuid-live-hit", "uuid-live-miss", "uuid-done-hit", "uuid-done-miss"}
	hits := []string{"uuid-live-hit", "uuid-done-hit"}
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"no arguments", nil, everything},
		{"one word", []string{"ratchet"}, hits},
		// Splitting on the shell and quoting for it are the same request: the
		// words are joined and split again rather than counted.
		{"several words", []string{"ratchet", "migration"}, hits},
		{"the same query quoted", []string{"ratchet migration"}, hits},
		// AND, not OR: "entirely" is only in the row "ratchet" is not in, so
		// the query naming both matches nothing rather than both rows.
		{"terms are ANDed", []string{"ratchet", "entirely"}, nil},
		{"a term nothing holds", []string{"nonesuch"}, nil},
		// Whitespace holds no term, so it is the unfiltered listing rather than
		// the error the flag used to raise: this is the picker's first frame.
		{"whitespace only", []string{"   "}, everything},
		{"whitespace between words", []string{" ", "ratchet", "\t"}, hits},
		// A term beginning with "-" is unreachable without a boundary, so "--"
		// is the one that makes it text. Nothing matches it here; the point is
		// that it was searched for rather than rejected as an option.
		{"double dash makes a dash a term", []string{"--", "-x"}, nil},
		{"a bare double dash filters nothing", []string{"--"}, everything},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ids := sessionRows(t, tt.args...)
			if !sameIDs(ids, tt.want) {
				t.Errorf("osc8wrap sessions %q listed %q, want %q", tt.args, ids, tt.want)
			}
		})
	}
}

// --format is still an option among the query words wherever it lands, so the
// picker may append it, prepend it, or leave it in the middle of a query the
// user is typing into and get one listing back.
func TestRunSessionsFormatAnywhereAmongTheQuery(t *testing.T) {
	grepFixture(t)

	run := func(args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := runSessions(args, &stdout, &stderr); code != 0 {
			t.Fatalf("runSessions(%q) = %d, stderr %q", args, code, stderr.String())
		}
		return stdout.String()
	}

	before := run("--format", "json", "ratchet", "migration")
	if !strings.HasPrefix(before, "[") {
		t.Fatalf("--format json before the query produced %q, want the json array", before)
	}
	if n := strings.Count(before, `"id"`); n != 2 {
		t.Errorf("--format json before the query listed %d rows, want the 2 that match:\n%s", n, before)
	}
	for _, args := range [][]string{
		{"ratchet", "migration", "--format", "json"},
		{"ratchet", "--format=json", "migration"},
	} {
		if got := run(args...); got != before {
			t.Errorf("osc8wrap sessions %q produced\n%s\nwant the same listing as --format first:\n%s",
				args, got, before)
		}
	}
}

// A machine with nothing to list still succeeds, and says so in the format it
// was asked for and nowhere else: prose on stdout would be offered to `fzf` as
// a candidate row, and a program reading the json preset would find it in front
// of the array. Empty is not an error either -- there is simply nothing yet.
func TestEmptyListingIsSilentAndSucceeds(t *testing.T) {
	useTempRegistry(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	var stdout, stderr bytes.Buffer
	if code := runSessions(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want an empty listing rather than prose", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing: an empty machine has nothing to explain", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runSessions([]string{"--format", "json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := stdout.String(); got != "[]\n" {
		t.Errorf("stdout = %q, want the empty array on its own", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing alongside a machine-readable listing", stderr.String())
	}
}
