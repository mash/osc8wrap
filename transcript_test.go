package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newProjectsDir points CLAUDE_CONFIG_DIR at a fresh temp dir and returns its
// projects/ subdirectory.
func newProjectsDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	projects := filepath.Join(root, "projects")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	return projects
}

// writeTranscriptFile writes lines joined by newlines, with a trailing newline
// unless raw already ends without one.
func writeTranscriptFile(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func userLine(cwd, branch, content string) string {
	return fmt.Sprintf(`{"type":"user","cwd":%q,"gitBranch":%q,"message":{"role":"user","content":%s}}`,
		cwd, branch, content)
}

func TestClaudeDirHonoursConfigEnv(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/somewhere/else")
	if got, want := claudeDir(), "/somewhere/else"; got != want {
		t.Errorf("claudeDir() = %q, want %q", got, want)
	}
	if got, want := claudeProjectsDir(), filepath.Join("/somewhere/else", "projects"); got != want {
		t.Errorf("claudeProjectsDir() = %q, want %q", got, want)
	}
}

func TestClaudeDirFallsBackToHome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got, want := claudeDir(), filepath.Join(home, ".claude"); got != want {
		t.Errorf("claudeDir() = %q, want %q", got, want)
	}
}

func TestScanTranscripts(t *testing.T) {
	projects := newProjectsDir(t)

	projectA := filepath.Join(projects, "-Users-mash-src-a")
	writeTranscriptFile(t, projectA, "aaaa-1111.jsonl", `{"type":"user"}`)
	// Rejected: not .jsonl, which is also how sessions-index.json is excluded.
	writeTranscriptFile(t, projectA, "sessions-index.json", `{}`)
	writeTranscriptFile(t, projectA, "notes.txt", "hello")
	// Rejected: empty stem.
	writeTranscriptFile(t, projectA, ".jsonl", "{}")
	// Rejected: project dirs really do contain uuid-named subdirectories.
	if err := os.MkdirAll(filepath.Join(projectA, "bbbb-2222.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}

	projectB := filepath.Join(projects, "-Users-mash-src-b")
	writeTranscriptFile(t, projectB, "cccc-3333.jsonl", `{"type":"user"}`)

	// A regular file sitting where a project directory would be.
	writeTranscriptFile(t, projects, "stray.jsonl", "{}")

	metas := scanTranscripts()
	got := make(map[string]transcriptMeta, len(metas))
	for _, m := range metas {
		got[m.ID] = m
	}

	if len(got) != 2 {
		t.Fatalf("scanTranscripts() returned %d entries, want 2: %+v", len(got), metas)
	}
	for id, wantDir := range map[string]string{"aaaa-1111": projectA, "cccc-3333": projectB} {
		m, ok := got[id]
		if !ok {
			t.Fatalf("missing transcript %q in %+v", id, metas)
		}
		if m.ProjectDir != wantDir {
			t.Errorf("%s ProjectDir = %q, want %q", id, m.ProjectDir, wantDir)
		}
		if want := filepath.Join(wantDir, id+".jsonl"); m.Path != want {
			t.Errorf("%s Path = %q, want %q", id, m.Path, want)
		}
		info, err := os.Stat(m.Path)
		if err != nil {
			t.Fatal(err)
		}
		if !m.ModTime.Equal(info.ModTime()) {
			t.Errorf("%s ModTime = %v, want %v", id, m.ModTime, info.ModTime())
		}
	}
}

func TestScanTranscriptsMissingProjectsDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if metas := scanTranscripts(); metas != nil {
		t.Errorf("scanTranscripts() = %+v, want nil", metas)
	}
}

func TestScanTranscriptsUnreadableProjectsDir(t *testing.T) {
	// A config dir that is a regular file makes projects/ unreadable on every OS.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", file)
	if metas := scanTranscripts(); metas != nil {
		t.Errorf("scanTranscripts() = %+v, want nil", metas)
	}
}

func TestParseTurn(t *testing.T) {
	tests := []struct {
		name string
		line string
		want turn
		ok   bool
	}{
		{
			name: "user with string content",
			line: userLine("/repo", "main", `"hello world"`),
			want: turn{Type: "user", Role: "user", Text: "hello world", Cwd: "/repo", GitBranch: "main"},
			ok:   true,
		},
		{
			name: "array of text parts is joined",
			line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"one"},{"type":"text","text":"two"}]}}`,
			want: turn{Type: "assistant", Role: "assistant", Text: "one\ntwo"},
			ok:   true,
		},
		{
			name: "array of only tool_result yields no text",
			line: `{"type":"user","cwd":"/repo","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"output"}]}}`,
			want: turn{Type: "user", Role: "user", Cwd: "/repo"},
			ok:   true,
		},
		{
			name: "tool_use and thinking parts are skipped",
			line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"tool_use","name":"Read","input":{}},{"type":"text","text":"done"}]}}`,
			want: turn{Type: "assistant", Role: "assistant", Text: "done"},
			ok:   true,
		},
		{
			name: "bare string part",
			line: `{"type":"user","message":{"role":"user","content":["plain"]}}`,
			want: turn{Type: "user", Role: "user", Text: "plain"},
			ok:   true,
		},
		{
			name: "unfamiliar record still yields cwd and branch",
			line: `{"type":"queue-operation","cwd":"/repo","gitBranch":"topic","operation":"enqueue"}`,
			want: turn{Type: "queue-operation", Cwd: "/repo", GitBranch: "topic"},
			ok:   true,
		},
		{
			name: "message that is not an object keeps cwd",
			line: `{"type":"summary","cwd":"/repo","message":"a string"}`,
			want: turn{Type: "summary", Cwd: "/repo"},
			ok:   true,
		},
		{
			name: "content absent",
			line: `{"type":"user","message":{"role":"user"}}`,
			want: turn{Type: "user", Role: "user"},
			ok:   true,
		},
		{name: "empty line", line: "", ok: false},
		{name: "blank line", line: "   \t ", ok: false},
		{name: "not json", line: "not json at all", ok: false},
		{name: "truncated json", line: `{"type":"user","message":{"role":"user","content":"hel`, ok: false},
		{name: "json but not an object", line: `["user"]`, ok: false},
		// The sole reason for the leading-'{' check: null unmarshals happily
		// into a struct, so only the check rejects it.
		{name: "bare null", line: "null", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseTurn([]byte(tt.line))
			if ok != tt.ok {
				t.Fatalf("parseTurn() ok = %v, want %v", ok, tt.ok)
			}
			if !tt.ok {
				return
			}
			if got != tt.want {
				t.Errorf("parseTurn() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadTranscriptHead(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  transcriptHead
	}{
		{
			// The scan must still be running when the second cwd arrives, so
			// the first record deliberately carries no prompt: were it complete
			// the loop would break and the rule would never be exercised.
			// Taking the later cwd is what makes `claude --resume` fail with
			// "No conversation found".
			name: "first cwd wins over a later cd",
			lines: []string{
				`{"type":"queue-operation","cwd":"/start/here","gitBranch":"main","operation":"enqueue"}`,
				userLine("/moved/away", "other", `"second prompt"`),
			},
			want: transcriptHead{Cwd: "/start/here", Prompt: "second prompt", GitBranch: "main"},
		},
		{
			name: "first non-empty cwd wins when the first record has none",
			lines: []string{
				`{"type":"queue-operation","operation":"enqueue"}`,
				userLine("/start/here", "", `"go"`),
				userLine("/moved/away", "later", `"stop"`),
			},
			want: transcriptHead{Cwd: "/start/here", Prompt: "go", GitBranch: "later"},
		},
		{
			name: "synthetic first user turn is skipped",
			lines: []string{
				userLine("/repo", "main", `"<system-reminder>be nice</system-reminder>"`),
				userLine("/repo", "main", `"the real question"`),
			},
			want: transcriptHead{Cwd: "/repo", Prompt: "the real question", GitBranch: "main"},
		},
		{
			name: "array of text parts",
			lines: []string{
				userLine("/repo", "main", `[{"type":"text","text":"from an array"}]`),
			},
			want: transcriptHead{Cwd: "/repo", Prompt: "from an array", GitBranch: "main"},
		},
		{
			name: "array of only tool_result yields no prompt but keeps cwd",
			lines: []string{
				userLine("/repo", "main", `[{"type":"tool_result","tool_use_id":"t1","content":"out"}]`),
			},
			want: transcriptHead{Cwd: "/repo", GitBranch: "main"},
		},
		{
			name: "assistant turns are not prompts",
			lines: []string{
				`{"type":"assistant","cwd":"/repo","message":{"role":"assistant","content":"hi there"}}`,
			},
			want: transcriptHead{Cwd: "/repo"},
		},
		{
			name:  "empty file",
			lines: nil,
			want:  transcriptHead{},
		},
		{
			name:  "all lines invalid",
			lines: []string{"not json", "{{{", ""},
			want:  transcriptHead{},
		},
		{
			name: "truncated final line is skipped",
			lines: []string{
				`{"type":"queue-operation","cwd":"/repo"}`,
				`{"type":"user","message":{"role":"user","content":"never finish`,
			},
			want: transcriptHead{Cwd: "/repo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeTranscriptFile(t, dir, "t.jsonl", tt.lines...)
			if got := readTranscriptHead(path); got != tt.want {
				t.Errorf("readTranscriptHead() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadTranscriptHeadMissingFile(t *testing.T) {
	got := readTranscriptHead(filepath.Join(t.TempDir(), "absent.jsonl"))
	if got != (transcriptHead{}) {
		t.Errorf("readTranscriptHead() = %+v, want zero value", got)
	}
}

func TestReadTranscriptHeadStopsAtLineLimit(t *testing.T) {
	lines := []string{`{"type":"queue-operation","cwd":"/repo","gitBranch":"main"}`}
	for len(lines) < 3999 {
		lines = append(lines, `{"type":"assistant","message":{"role":"assistant","content":"filler"}}`)
	}
	lines = append(lines, userLine("/repo", "main", `"the prompt nobody reads"`))

	dir := t.TempDir()
	path := writeTranscriptFile(t, dir, "deep.jsonl", lines...)

	got := readTranscriptHead(path)
	if got.Prompt != "" {
		t.Errorf("Prompt = %q, want empty for a prompt beyond line %d", got.Prompt, headMaxLines)
	}
	if got.Cwd != "/repo" {
		t.Errorf("Cwd = %q, want /repo", got.Cwd)
	}
}

func TestReadTranscriptHeadSkipsOverlongLine(t *testing.T) {
	// A pasted attachment on an early line must cost the line, not the prompt.
	huge := strings.Repeat("a", 2<<20)
	lines := []string{
		fmt.Sprintf(`{"type":"user","cwd":"/repo","gitBranch":"main","message":{"role":"user","content":%q}}`, huge),
		userLine("/repo", "main", `"the prompt after the attachment"`),
	}

	dir := t.TempDir()
	path := writeTranscriptFile(t, dir, "huge.jsonl", lines...)

	got := readTranscriptHead(path)
	want := transcriptHead{Cwd: "/repo", Prompt: "the prompt after the attachment", GitBranch: "main"}
	if got != want {
		t.Errorf("readTranscriptHead() = %+v, want %+v", got, want)
	}
}

func TestReadTranscriptTail(t *testing.T) {
	lines := []string{
		userLine("/repo", "main", `"first"`),
		`{"type":"assistant","message":{"role":"assistant","content":"second"}}`,
		userLine("/repo", "main", `"third"`),
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{}}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"fourth"}}`,
	}
	dir := t.TempDir()
	path := writeTranscriptFile(t, dir, "tail.jsonl", lines...)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("window larger than the file returns every turn", func(t *testing.T) {
		got := readTranscriptTail(path, info.Size()*2)
		want := []string{"first", "second", "third", "fourth"}
		assertTurnTexts(t, got, want)
	})

	t.Run("window landing mid-line resumes at the next record", func(t *testing.T) {
		// Two whole lines plus part of the one before them. This pins the seek
		// arithmetic rather than the discard itself: a truncated fragment is
		// invalid JSON, so parseTurn would drop it even undiscarded. See
		// TestReadTranscriptTailDiscardsPartialLine for the discard.
		window := int64(len(lines[3]) + len(lines[4]) + 2 + 10)
		got := readTranscriptTail(path, window)
		assertTurnTexts(t, got, []string{"fourth"})
	})

	t.Run("window landing on a record boundary keeps that record", func(t *testing.T) {
		// Exactly the last line and its newline: nothing is partial here, and
		// discarding a "partial" first line would eat the whole record.
		window := int64(len(lines[4]) + 1)
		got := readTranscriptTail(path, window)
		assertTurnTexts(t, got, []string{"fourth"})
	})

	t.Run("window smaller than the last line yields nothing", func(t *testing.T) {
		if got := readTranscriptTail(path, 4); len(got) != 0 {
			t.Errorf("readTranscriptTail() = %+v, want none", got)
		}
	})

	t.Run("non-positive window yields nothing", func(t *testing.T) {
		if got := readTranscriptTail(path, 0); got != nil {
			t.Errorf("readTranscriptTail() = %+v, want nil", got)
		}
	})

	t.Run("missing file yields nothing", func(t *testing.T) {
		if got := readTranscriptTail(filepath.Join(dir, "absent.jsonl"), 1<<20); got != nil {
			t.Errorf("readTranscriptTail() = %+v, want nil", got)
		}
	})
}

// TestReadTranscriptTailDiscardsPartialLine pins the discard itself. A tail of
// a well-formed JSONL line is never valid JSON, so a normal file cannot tell a
// working discard from a missing one; this file's first line therefore ends
// with a complete record, making the fragment parseable on purpose. A record
// the window only half-covers must not reach the preview pane.
func TestReadTranscriptTailDiscardsPartialLine(t *testing.T) {
	ghost := `{"type":"user","message":{"role":"user","content":"ghost"}}`
	lines := []string{
		"xx" + ghost,
		`{"type":"assistant","message":{"role":"assistant","content":"real"}}`,
	}
	dir := t.TempDir()
	path := writeTranscriptFile(t, dir, "partial.jsonl", lines...)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// Land the window on the '{' two bytes into the first line: mid-line, yet
	// on something that parses.
	got := readTranscriptTail(path, info.Size()-2)
	assertTurnTexts(t, got, []string{"real"})
}

func TestReadTranscriptTailRolesAndInvalidLines(t *testing.T) {
	dir := t.TempDir()
	path := writeTranscriptFile(t, dir, "mixed.jsonl",
		"not json",
		userLine("/repo", "main", `"ask"`),
		`{"type":"queue-operation","operation":"dequeue"}`,
		`{"type":"assistant","message":{"role":"assistant","content":"answer"}}`,
	)

	got := readTranscriptTail(path, 1<<20)
	if len(got) != 2 {
		t.Fatalf("readTranscriptTail() = %+v, want 2 turns", got)
	}
	if got[0].Role != "user" || got[0].Text != "ask" {
		t.Errorf("turn 0 = %+v, want user/ask", got[0])
	}
	if got[1].Role != "assistant" || got[1].Text != "answer" {
		t.Errorf("turn 1 = %+v, want assistant/answer", got[1])
	}
}

func TestReadTranscriptTailBranch(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name: "last branch wins over an earlier one",
			lines: []string{
				userLine("/repo", "main", `"first"`),
				userLine("/repo", "feature/x", `"second"`),
			},
			want: "feature/x",
		},
		{
			name: "a later record with no branch does not erase the earlier one",
			lines: []string{
				userLine("/repo", "feature/x", `"first"`),
				`{"type":"assistant","message":{"role":"assistant","content":"no branch here"}}`,
			},
			want: "feature/x",
		},
		{
			name: "a record with gitBranch but no message text still counts",
			lines: []string{
				userLine("/repo", "main", `"first"`),
				`{"type":"queue-operation","gitBranch":"feature/x","operation":"enqueue"}`,
			},
			want: "feature/x",
		},
		{
			name: "no gitBranch anywhere yields empty",
			lines: []string{
				`{"type":"assistant","message":{"role":"assistant","content":"hi"}}`,
			},
			want: "",
		},
		{
			name:  "empty file",
			lines: nil,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeTranscriptFile(t, dir, "t.jsonl", tt.lines...)
			if got := readTranscriptTailBranch(path); got != tt.want {
				t.Errorf("readTranscriptTailBranch() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadTranscriptTailBranchMissingFile(t *testing.T) {
	got := readTranscriptTailBranch(filepath.Join(t.TempDir(), "absent.jsonl"))
	if got != "" {
		t.Errorf("readTranscriptTailBranch() = %q, want empty", got)
	}
}

// TestReadTranscriptTailBranchWindowMidRecord pins the window against a real
// tailBranchWindowSize-sized file rather than a small one, so the seek and
// partial-line discard exercised here are the ones production actually runs.
func TestReadTranscriptTailBranchWindowMidRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.jsonl")

	// Pad the front of the file past the window with an oversized filler line
	// carrying a branch that must not surface, then two real records that the
	// window is guaranteed to contain whole.
	filler := userLine("/repo", "stale", fmt.Sprintf("%q", strings.Repeat("x", 2*tailBranchWindowSize)))
	last := []string{
		userLine("/repo", "feature/x", `"second to last"`),
		userLine("/repo", "feature/y", `"last"`),
	}
	body := filler + "\n" + strings.Join(last, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, want := readTranscriptTailBranch(path), "feature/y"; got != want {
		t.Errorf("readTranscriptTailBranch() = %q, want %q", got, want)
	}
}

func assertTurnTexts(t *testing.T, got []turn, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d turns %+v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i].Text != want[i] {
			t.Errorf("turn %d text = %q, want %q", i, got[i].Text, want[i])
		}
	}
}
