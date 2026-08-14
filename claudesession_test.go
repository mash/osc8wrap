package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// realSessionJSON is a verbatim file written by Claude Code 2.1.220.
const realSessionJSON = `{"pid":58515,"sessionId":"b2dae39b-7833-4a90-a6d2-de2edb061a7f","cwd":"/Users/mash/src/github.com/mash/osc8wrap","startedAt":1785026332793,"procStart":"Sun Jul 26 00:38:52 2026","version":"2.1.220","peerProtocol":1,"kind":"interactive","entrypoint":"cli","name":"osc8wrap-74","nameSource":"derived","status":"waiting","updatedAt":1785026541307,"statusUpdatedAt":1785026541307,"bridgeSessionId":"session_01TYsjVMBaH8XATQbRXpkLxS","waitingFor":"input needed"}`

// setupClaudeSessions points CLAUDE_CONFIG_DIR at a fresh directory holding
// the given files, keyed by filename.
func setupClaudeSessions(t *testing.T, files map[string]string) string {
	t.Helper()
	configDir := t.TempDir()
	sessionsDir := filepath.Join(configDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(sessionsDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	return sessionsDir
}

func TestClaudeSessionsDir(t *testing.T) {
	t.Run("uses CLAUDE_CONFIG_DIR when set", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "/custom/config")
		if got, want := claudeSessionsDir(), filepath.Join("/custom/config", "sessions"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to home", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		t.Setenv("HOME", "/home/tester")
		if got, want := claudeSessionsDir(), filepath.Join("/home/tester", ".claude", "sessions"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestLoadClaudeSessions(t *testing.T) {
	setupClaudeSessions(t, map[string]string{
		"58515.json": realSessionJSON,
		// Unknown fields must be ignored, not rejected.
		"101.json": `{"pid":101,"sessionId":"uuid-101","name":"future","status":"busy","updatedAt":1785026000000,"somethingNew":{"a":[1,2]},"anotherNew":"x"}`,
		// status/name absent: degrade to empty strings.
		"202.json": `{"pid":202,"sessionId":"uuid-202","updatedAt":1785025000000}`,
		// pid absent: fall back to the filename stem.
		"303.json": `{"sessionId":"uuid-303","status":"idle"}`,
		// Corrupt (truncated mid-write) files are skipped.
		"404.json": `{"pid":404,"sessionId":`,
		// Non-JSON files in the directory are ignored.
		"notes.txt": "not a session",
	})

	sessions, err := loadClaudeSessions()
	if err != nil {
		t.Fatal(err)
	}

	if len(sessions) != 4 {
		t.Fatalf("got %d sessions, want 4: %+v", len(sessions), sessions)
	}
	if _, ok := sessions[404]; ok {
		t.Error("corrupt file should have been skipped")
	}

	tests := []struct {
		pid  int
		want claudeSessionFile
	}{
		{
			pid: 58515,
			want: claudeSessionFile{
				PID:        58515,
				SessionID:  "b2dae39b-7833-4a90-a6d2-de2edb061a7f",
				Name:       "osc8wrap-74",
				Status:     "waiting",
				WaitingFor: "input needed",
				UpdatedAt:  1785026541307,
			},
		},
		{
			pid:  101,
			want: claudeSessionFile{PID: 101, SessionID: "uuid-101", Name: "future", Status: "busy", UpdatedAt: 1785026000000},
		},
		{
			pid:  202,
			want: claudeSessionFile{PID: 202, SessionID: "uuid-202", UpdatedAt: 1785025000000},
		},
		{
			pid:  303,
			want: claudeSessionFile{PID: 303, SessionID: "uuid-303", Status: "idle"},
		},
	}
	for _, tt := range tests {
		if got := sessions[tt.pid]; got != tt.want {
			t.Errorf("pid %d: got %+v, want %+v", tt.pid, got, tt.want)
		}
	}
}

func TestLoadClaudeSessionsMissingDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "does-not-exist"))

	sessions, err := loadClaudeSessions()
	if err != nil {
		t.Fatalf("missing directory should not be an error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions, want 0", len(sessions))
	}
}

// TestListSessionsUnreadableClaudeDir pins the degradation: claude's metadata
// is decoration, so a sessions directory osc8wrap is not allowed to read must
// cost the listing its names and statuses, never the listing itself.
func TestListSessionsUnreadableClaudeDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 directory regardless of its mode")
	}
	sessionsDir := setupClaudeSessions(t, map[string]string{
		"58515.json": realSessionJSON,
	})
	if err := os.Chmod(sessionsDir, 0o000); err != nil {
		t.Fatal(err)
	}
	// Restored so t.TempDir's own cleanup can still remove the tree.
	t.Cleanup(func() { _ = os.Chmod(sessionsDir, 0o755) })

	if _, err := os.ReadDir(sessionsDir); err == nil {
		t.Skip("this filesystem lets the owner read a 0000 directory")
	}

	claude, err := loadClaudeSessions()
	if err != nil {
		t.Fatalf("unreadable directory should not be an error: %v", err)
	}
	if len(claude) != 0 {
		t.Errorf("got %d claude sessions, want 0", len(claude))
	}

	dir := useTempRegistry(t)
	writeEntry(t, dir, RegistryEntry{
		WrapperPID: os.Getpid(), ChildPID: 58515,
		TTY: "/dev/ttys011", Command: []string{"claude"},
	})

	sessions, err := listSessions()
	if err != nil {
		t.Fatalf("listSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want the registry entry", len(sessions))
	}
	if sessions[0].Live == nil {
		t.Fatalf("the registry entry should still be a live session: %+v", sessions[0])
	}
	if got := sessions[0].Live.Entry.ChildPID; got != 58515 {
		t.Errorf("registry data lost: %+v", sessions[0].Live.Entry)
	}
	if sessions[0].Live.Name != "" || sessions[0].Live.Status != "" {
		t.Errorf("claude metadata should be absent: %+v", sessions[0].Live)
	}
}

func TestJoinSessions(t *testing.T) {
	entries := []RegistryEntry{
		{WrapperPID: 1, ChildPID: 11},
		{WrapperPID: 2, ChildPID: 22},
		{WrapperPID: 3, ChildPID: 33},
	}
	claude := map[int]claudeSessionFile{
		11: {PID: 11, SessionID: "uuid-11", Name: "one", Status: "waiting", WaitingFor: "input needed", UpdatedAt: 1785026541307},
		22: {PID: 22, SessionID: "uuid-22", Name: "two", Status: "idle", UpdatedAt: 1785026000000},
	}

	sessions := joinSessions(entries, claude)
	if len(sessions) != 3 {
		t.Fatalf("got %d sessions, want 3", len(sessions))
	}

	first := sessions[0]
	if first.Entry.WrapperPID != 1 || first.SessionID != "uuid-11" || first.Name != "one" ||
		first.Status != "waiting" || first.WaitingFor != "input needed" ||
		!first.Modified.Equal(time.UnixMilli(1785026541307)) {
		t.Errorf("joined fields wrong: %+v", first)
	}

	// The unmatched entry keeps its registry data and nothing else.
	unmatched := sessions[2]
	if unmatched.Entry.WrapperPID != 3 {
		t.Fatalf("expected wrapper 3 last, got %+v", unmatched)
	}
	if unmatched.SessionID != "" || unmatched.Name != "" || unmatched.Status != "" ||
		unmatched.WaitingFor != "" || !unmatched.Modified.IsZero() {
		t.Errorf("unmatched entry should have empty claude fields: %+v", unmatched)
	}
}

func TestJoinSessionsEmpty(t *testing.T) {
	if got := joinSessions(nil, nil); len(got) != 0 {
		t.Errorf("got %d sessions, want 0", len(got))
	}
}

func TestJoinSessionsSortOrder(t *testing.T) {
	const base = 1785026000000

	entries := []RegistryEntry{
		{WrapperPID: 10, ChildPID: 110}, // idle, newest of the tail group
		{WrapperPID: 20, ChildPID: 220}, // busy, older
		{WrapperPID: 30, ChildPID: 330}, // waiting, older
		{WrapperPID: 40, ChildPID: 440}, // waiting, newest
		{WrapperPID: 50, ChildPID: 550}, // busy, newer
		{WrapperPID: 60, ChildPID: 660}, // no claude match: tail, zero Modified
		{WrapperPID: 5, ChildPID: 770},  // no claude match: ties with 60, lower pid wins
	}
	claude := map[int]claudeSessionFile{
		110: {PID: 110, Status: "idle", UpdatedAt: base + 9000},
		220: {PID: 220, Status: "busy", UpdatedAt: base + 1000},
		330: {PID: 330, Status: "waiting", UpdatedAt: base + 1000},
		440: {PID: 440, Status: "waiting", UpdatedAt: base + 2000},
		550: {PID: 550, Status: "busy", UpdatedAt: base + 3000},
	}

	want := []int{40, 30, 50, 20, 10, 5, 60}
	sessions := joinSessions(entries, claude)
	got := make([]int, len(sessions))
	for i, s := range sessions {
		got[i] = s.Entry.WrapperPID
	}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("order: got %v, want %v", got, want)
		}
	}
}

func TestJoinSessionsTieBreakIsDeterministic(t *testing.T) {
	// Same status and same Modified for every entry, fed in descending pid
	// order: only the WrapperPID tie-break can produce a stable result.
	entries := []RegistryEntry{
		{WrapperPID: 9, ChildPID: 90},
		{WrapperPID: 7, ChildPID: 70},
		{WrapperPID: 8, ChildPID: 80},
	}
	claude := map[int]claudeSessionFile{
		90: {PID: 90, Status: "busy", UpdatedAt: 1785026000000},
		70: {PID: 70, Status: "busy", UpdatedAt: 1785026000000},
		80: {PID: 80, Status: "busy", UpdatedAt: 1785026000000},
	}

	want := []int{7, 8, 9}
	for i := 0; i < 10; i++ {
		sessions := joinSessions(entries, claude)
		for j, w := range want {
			if sessions[j].Entry.WrapperPID != w {
				t.Fatalf("run %d: got pid %d at index %d, want %d", i, sessions[j].Entry.WrapperPID, j, w)
			}
		}
	}
}

func TestListSessions(t *testing.T) {
	setupClaudeSessions(t, map[string]string{
		"58515.json": realSessionJSON,
	})
	// The registry prunes dead wrapper pids, so the entries have to name
	// processes that really are alive: this test binary and its parent.
	dir := useTempRegistry(t)
	writeEntry(t, dir, RegistryEntry{
		WrapperPID: os.Getpid(), ChildPID: 58515,
		TTY: "/dev/ttys011", SurfaceID: "0x1552e82d3b500c1e",
	})
	writeEntry(t, dir, RegistryEntry{WrapperPID: os.Getppid(), ChildPID: 99999})

	sessions, err := listSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	if sessions[0].Live == nil || sessions[1].Live == nil {
		t.Fatalf("both registry entries should be live: %+v", sessions)
	}
	if sessions[0].Live.Name != "osc8wrap-74" || sessions[0].Live.Status != "waiting" {
		t.Errorf("first session not joined with claude metadata: %+v", sessions[0].Live)
	}
	if sessions[0].Live.Entry.TTY != "/dev/ttys011" {
		t.Errorf("registry fields lost: %+v", sessions[0].Live.Entry)
	}
	// The UUID reaches the unified record even with no transcript on disk.
	if sessions[0].ID != "b2dae39b-7833-4a90-a6d2-de2edb061a7f" {
		t.Errorf("session UUID lost: %q", sessions[0].ID)
	}
	if sessions[1].Live.Status != "" {
		t.Errorf("unmatched session should have empty status: %+v", sessions[1].Live)
	}
}

// mergeBase is an arbitrary fixed instant; the merge cases care about the
// ordering of their timestamps, never about the wall clock.
var mergeBase = time.UnixMilli(1785026000000)

// mergeLive builds a live session the way joinSessions would have. An empty
// uuid is the real case of a wrapper around something that is not claude, or
// around a claude that has not published its session file yet.
func mergeLive(pid int, uuid, cwd, status string, offset time.Duration) LiveSession {
	return LiveSession{
		Entry: RegistryEntry{
			WrapperPID: pid,
			ChildPID:   pid + 1,
			TTY:        "/dev/ttys001",
			Cwd:        cwd,
			Command:    []string{"claude"},
		},
		SessionID: uuid,
		Status:    status,
		Modified:  mergeBase.Add(offset),
	}
}

// mergeTranscript writes a one-turn transcript and returns the meta
// scanTranscripts would have produced for it. ModTime is supplied rather than
// read back so the ordering cases do not depend on file timestamps.
func mergeTranscript(t *testing.T, projects, project, id, cwd, branch, prompt string, offset time.Duration) transcriptMeta {
	t.Helper()
	dir := filepath.Join(projects, project)
	path := writeTranscriptFile(t, dir, id+".jsonl", userLine(cwd, branch, strconv.Quote(prompt)))
	return transcriptMeta{ID: id, Path: path, ProjectDir: dir, ModTime: mergeBase.Add(offset)}
}

// sessionTokens names each record the way `open` will: the UUID, or the wrapper
// pid when the session has none.
func sessionTokens(sessions []Session) []string {
	tokens := make([]string, len(sessions))
	for i, s := range sessions {
		tokens[i] = s.ID
		if tokens[i] == "" {
			tokens[i] = fmt.Sprintf("pid%d", s.wrapperPID())
		}
	}
	return tokens
}

// A running session and the transcript it is writing are one session, so the
// user must see one row -- and that row must carry both halves.
func TestMergeSessionsPairsLiveWithItsTranscript(t *testing.T) {
	projects := newProjectsDir(t)
	tm := mergeTranscript(t, projects, "-Users-mash-src-a", "uuid-a", "/Users/mash/src/a", "feature/x", "fix the parser", 0)
	live := mergeLive(10, "uuid-a", "/Users/mash/src/a", "busy", time.Second)

	got := mergeSessions([]LiveSession{live}, []transcriptMeta{tm})
	if len(got) != 1 {
		t.Fatalf("got %d records %v, want the live session and its transcript merged into one", len(got), sessionTokens(got))
	}
	s := got[0]
	if s.Live == nil || s.Live.Entry.WrapperPID != 10 {
		t.Errorf("live half lost: %+v", s.Live)
	}
	if s.TranscriptPath != tm.Path || s.ProjectDir != tm.ProjectDir {
		t.Errorf("transcript half lost: path %q dir %q", s.TranscriptPath, s.ProjectDir)
	}
	if s.Prompt != "fix the parser" {
		t.Errorf("Prompt = %q, want the transcript's first user turn", s.Prompt)
	}
	// Live metadata is the fresher clock and keeps the Modified column.
	if !s.Modified.Equal(mergeBase.Add(time.Second)) {
		t.Errorf("Modified = %v, want claude's updatedAt %v", s.Modified, mergeBase.Add(time.Second))
	}
}

// The registry records where osc8wrap was launched and the transcript records
// where the session belongs; only the latter is a directory --resume works
// from, so it wins whenever the two disagree.
func TestMergeSessionsPrefersTranscriptCwd(t *testing.T) {
	projects := newProjectsDir(t)
	tm := mergeTranscript(t, projects, "-Users-mash-src-a", "uuid-a", "/Users/mash/src/a", "main", "hello", 0)
	live := mergeLive(10, "uuid-a", "/Users/mash/somewhere/else", "idle", 0)

	got := mergeSessions([]LiveSession{live}, []transcriptMeta{tm})
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].Cwd != "/Users/mash/src/a" {
		t.Errorf("Cwd = %q, want the transcript's first cwd", got[0].Cwd)
	}
}

// Before claude publishes ~/.claude/sessions/<pid>.json there is no UUID to
// merge on, so the wrapper and the transcript stay two rows. This documents the
// accepted duplicate rather than asserting against it.
func TestMergeSessionsLiveWithoutUUIDStaysSeparate(t *testing.T) {
	projects := newProjectsDir(t)
	tm := mergeTranscript(t, projects, "-Users-mash-src-a", "uuid-a", "/Users/mash/src/a", "main", "hello", 0)
	live := mergeLive(10, "", "/Users/mash/src/a", "busy", time.Second)

	got := mergeSessions([]LiveSession{live}, []transcriptMeta{tm})
	if want := []string{"pid10", "uuid-a"}; !equalArgs(sessionTokens(got), want) {
		t.Fatalf("got %v, want %v", sessionTokens(got), want)
	}
	if got[0].TranscriptPath != "" {
		t.Errorf("the pid-keyed record must not claim a transcript: %q", got[0].TranscriptPath)
	}
	if got[1].Live != nil {
		t.Errorf("the transcript record must not claim a wrapper: %+v", got[1].Live)
	}
}

// Two live wrappers can report the same UUID: the README's re-wrap variant
// runs `osc8wrap claude --resume <id>` and nothing stops it doing so on a row
// that is still live. Both rows must keep a token of their own -- two records
// printing the same id are two records `open` can only call ambiguous, and the
// wrapper pid that distinguishes them appears nowhere the user can type it.
func TestMergeSessionsDuplicateLiveUUID(t *testing.T) {
	first := mergeLive(10, "uuid-a", "/nonexistent/src/a", "busy", time.Second)
	second := mergeLive(20, "uuid-a", "/nonexistent/src/a", "busy", 0)

	got := mergeSessions([]LiveSession{first, second}, nil)
	if len(got) != 2 {
		t.Fatalf("got %d records %v, want both wrappers listed", len(got), sessionTokens(got))
	}

	seen := make(map[string]bool, 2)
	for _, s := range got {
		token := sessionToken(s)
		if token == "" {
			t.Fatalf("record %+v names itself with nothing", s)
		}
		if seen[token] {
			t.Fatalf("two records both answer to %q: %v", token, sessionTokens(got))
		}
		seen[token] = true
		if _, err := resolveSession(got, token); err != nil {
			t.Errorf("osc8wrap open %s: %v", token, err)
		}
	}
	if !seen["uuid-a"] {
		t.Errorf("no record kept the uuid; tokens %v", got)
	}
	if !seen["20"] {
		t.Errorf("the duplicate did not fall back to its wrapper pid; tokens %v", seen)
	}
}

func TestMergeSessionsFinishedTranscript(t *testing.T) {
	projects := newProjectsDir(t)
	tm := mergeTranscript(t, projects, "-Users-mash-src-a", "uuid-a", "/Users/mash/src/a", "feature/x", "hello", 7*time.Second)

	got := mergeSessions(nil, []transcriptMeta{tm})
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	s := got[0]
	if s.Live != nil {
		t.Errorf("a transcript with no wrapper is finished: %+v", s.Live)
	}
	if s.ID != "uuid-a" || s.Cwd != "/Users/mash/src/a" || s.Prompt != "hello" {
		t.Errorf("transcript fields wrong: %+v", s)
	}
	// The directory does not exist, so only the recorded branch survives.
	if s.Branch != "feature/x" || s.Worktree != "" || s.Root != "" {
		t.Errorf("git context wrong: branch %q worktree %q root %q", s.Branch, s.Worktree, s.Root)
	}
	if !s.Modified.Equal(tm.ModTime) {
		t.Errorf("Modified = %v, want the transcript mtime %v", s.Modified, tm.ModTime)
	}
}

// TestMergeSessionsFinishedTranscriptBranchSwitch is the bug this fix targets:
// a session that starts on main and switches to a feature branch mid-session
// must be recorded under the branch it ended on, not the one it started on,
// or the picker's branch search can never find it.
func TestMergeSessionsFinishedTranscriptBranchSwitch(t *testing.T) {
	projects := newProjectsDir(t)
	dir := filepath.Join(projects, "p")
	path := writeTranscriptFile(t, dir, "uuid-switch.jsonl",
		userLine("/Users/mash/src/a", "main", `"start here"`),
		userLine("/Users/mash/src/a", "feature/x", `"switched branches"`),
	)
	tm := transcriptMeta{ID: "uuid-switch", Path: path, ProjectDir: dir, ModTime: mergeBase}

	got := mergeSessions(nil, []transcriptMeta{tm})
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if s := got[0]; s.Branch != "feature/x" {
		t.Errorf("Branch = %q, want %q", s.Branch, "feature/x")
	}
}

// TestMergeSessionsFinishedTranscriptNoBranchFallsBackToHead covers a
// transcript whose tail window (bounded by tailBranchWindowSize) never finds
// a gitBranch at all -- here because the only gitBranch-carrying record is
// pushed out of the window by a huge filler record after it, while still
// within headMaxLines so the head scan finds it. applyTranscript must fall
// back to the head's branch rather than leave the branch empty.
func TestMergeSessionsFinishedTranscriptNoBranchFallsBackToHead(t *testing.T) {
	projects := newProjectsDir(t)
	dir := filepath.Join(projects, "p")
	filler := fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":%q}}`,
		strings.Repeat("x", 2*tailBranchWindowSize))
	path := writeTranscriptFile(t, dir, "uuid-nobranch.jsonl",
		userLine("/Users/mash/src/a", "main", `"only the head carries a branch"`),
		filler,
	)
	tm := transcriptMeta{ID: "uuid-nobranch", Path: path, ProjectDir: dir, ModTime: mergeBase}

	got := mergeSessions(nil, []transcriptMeta{tm})
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if s := got[0]; s.Branch != "main" {
		t.Errorf("Branch = %q, want the head's %q", s.Branch, "main")
	}
}

func TestMergeSessionsOrder(t *testing.T) {
	projects := newProjectsDir(t)

	live := []LiveSession{
		// Fed in an order that no single field would sort correctly.
		mergeLive(30, "", "", "", 300*time.Millisecond),
		mergeLive(20, "u-busy", "", "busy", 200*time.Millisecond),
		mergeLive(40, "", "", "", 300*time.Millisecond),
		mergeLive(10, "u-wait", "", "waiting", 100*time.Millisecond),
	}
	transcripts := []transcriptMeta{
		mergeTranscript(t, projects, "p", "t-bbb", "", "", "", time.Second),
		mergeTranscript(t, projects, "p", "t-new", "", "", "", 5*time.Second),
		mergeTranscript(t, projects, "p", "t-aaa", "", "", "", time.Second),
	}

	// Live first whatever their timestamps say, waiting before busy before the
	// rest, then recency, then the total-order tie-breaks: pid for the two
	// live records that agree on everything, ID for the two finished ones.
	want := []string{"u-wait", "u-busy", "pid30", "pid40", "t-new", "t-aaa", "t-bbb"}
	for i := 0; i < 5; i++ {
		if got := sessionTokens(mergeSessions(live, transcripts)); !equalArgs(got, want) {
			t.Fatalf("run %d: got %v, want %v", i, got, want)
		}
	}
}

// The branch a live session shows must be the branch it is on now; the branch a
// finished session shows is the one it ran on, because nothing on disk can
// still testify to it -- nine of the 35 cwds measured on the author's machine
// are worktrees that have since been deleted.
func TestMergeSessionsGitContext(t *testing.T) {
	projects := newProjectsDir(t)
	repo := makeGitMainCheckout(t, t.TempDir(), "ref: refs/heads/current\n")
	deleted := filepath.Join(t.TempDir(), "removed-worktree")

	transcripts := []transcriptMeta{
		mergeTranscript(t, projects, "p", "u-live", repo, "recorded", "", 0),
		mergeTranscript(t, projects, "p", "u-done", repo, "recorded", "", 0),
		mergeTranscript(t, projects, "p", "u-gone", deleted, "recorded", "", 0),
		mergeTranscript(t, projects, "p", "u-live-gone", deleted, "recorded", "", 0),
	}
	live := []LiveSession{
		mergeLive(10, "u-live", repo, "busy", 0),
		mergeLive(20, "u-live-gone", deleted, "busy", 0),
	}

	byID := make(map[string]Session)
	for _, s := range mergeSessions(live, transcripts) {
		byID[s.ID] = s
	}

	tests := []struct {
		id                                 string
		wantBranch, wantWorktree, wantRoot string
	}{
		{"u-live", "current", repo, repo},
		{"u-done", "recorded", repo, repo},
		{"u-gone", "recorded", "", ""},
		// Live and its directory gone: disk is authoritative and disk has
		// nothing, so the row degrades to no git context at all.
		{"u-live-gone", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			s, ok := byID[tt.id]
			if !ok {
				t.Fatalf("record %q missing", tt.id)
			}
			if s.Branch != tt.wantBranch || s.Worktree != tt.wantWorktree || s.Root != tt.wantRoot {
				t.Errorf("branch %q worktree %q root %q, want %q %q %q",
					s.Branch, s.Worktree, s.Root, tt.wantBranch, tt.wantWorktree, tt.wantRoot)
			}
		})
	}
}

// A renamed project directory leaves the same UUID filed twice. One session is
// one row, and the file still being appended to is the one to show.
func TestMergeSessionsSameUUIDInTwoProjects(t *testing.T) {
	projects := newProjectsDir(t)
	old := mergeTranscript(t, projects, "-Users-mash-src-old", "uuid-a", "/Users/mash/src/old", "main", "stale", 0)
	fresh := mergeTranscript(t, projects, "-Users-mash-src-new", "uuid-a", "/Users/mash/src/new", "main", "current", time.Second)

	got := mergeSessions(nil, []transcriptMeta{old, fresh})
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].TranscriptPath != fresh.Path {
		t.Errorf("TranscriptPath = %q, want the newer %q", got[0].TranscriptPath, fresh.Path)
	}
}

func TestMergeSessionsEmpty(t *testing.T) {
	if got := mergeSessions(nil, nil); len(got) != 0 {
		t.Errorf("got %d records, want 0", len(got))
	}
}

// Anyone who has never run Claude Code has no projects directory, and the live
// listing must still work.
func TestListSessionsWithoutProjectsDir(t *testing.T) {
	setupClaudeSessions(t, map[string]string{"58515.json": realSessionJSON})
	if _, err := os.Stat(claudeProjectsDir()); !os.IsNotExist(err) {
		t.Fatalf("the fixture is supposed to have no projects directory: %v", err)
	}
	dir := useTempRegistry(t)
	writeEntry(t, dir, RegistryEntry{
		WrapperPID: os.Getpid(), ChildPID: 58515,
		TTY: "/dev/ttys011", Command: []string{"claude"},
	})

	sessions, err := listSessions()
	if err != nil {
		t.Fatalf("a missing projects directory must not fail the listing: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions %v, want the live one", len(sessions), sessionTokens(sessions))
	}
	if sessions[0].Live == nil || sessions[0].TranscriptPath != "" {
		t.Errorf("want a live record with no transcript, got %+v", sessions[0])
	}
}
