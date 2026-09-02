package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// scratchpadFixtureSlug is an arbitrary cwd slug: Resolve globs for the
// sessionID underneath it and never computes it, so the tests do not need a
// slug that matches any real directory.
const scratchpadFixtureSlug = "-Users-mash-src-github-com-mash-osc8wrap"

// setupScratchpadFixture builds a fake session root of the shape
// <root>/<cwd-slug>/<sessionID>/<name...>, writing each of files under it, and
// returns the root.
func setupScratchpadFixture(t *testing.T, sessionID string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	sessionDir := filepath.Join(root, scratchpadFixtureSlug, sessionID)
	for name, content := range files {
		full := filepath.Join(sessionDir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// newTestScratchpad points a sessionScratchpad's roots at root and, via
// setupClaudeSessions (see claudesession_test.go), points CLAUDE_CONFIG_DIR at
// a sessions/<pid>.json naming sessionID for pid.
func newTestScratchpad(t *testing.T, root string, pid int, sessionID string) *sessionScratchpad {
	t.Helper()
	setupClaudeSessions(t, map[string]string{
		fmt.Sprintf("%d.json", pid): fmt.Sprintf(`{"pid":%d,"sessionId":%q}`, pid, sessionID),
	})

	s := newSessionScratchpad()
	s.roots = []string{root}
	s.childPID = pid
	return s
}

func TestSessionScratchpadResolve(t *testing.T) {
	const pid = 4242
	const sessionID = "b2dae39b-7833-4a90-a6d2-de2edb061a7f"

	tests := []struct {
		name string
		rel  string
		want bool // whether the fixture file scratchpad/hello.txt should resolve
	}{
		{name: "scratchpad hit", rel: "scratchpad/hello.txt", want: true},
		{name: "leading dot-slash", rel: "./scratchpad/hello.txt", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := setupScratchpadFixture(t, sessionID, map[string]string{
				"scratchpad/hello.txt": "hi",
			})
			s := newTestScratchpad(t, root, pid, sessionID)

			want := ""
			if tt.want {
				full := filepath.Join(root, scratchpadFixtureSlug, sessionID, "scratchpad", "hello.txt")
				if resolved, err := filepath.EvalSymlinks(full); err == nil {
					want = resolved
				} else {
					want = full
				}
			}
			if got := s.Resolve(tt.rel); got != want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.rel, got, want)
			}
		})
	}
}

func TestSessionScratchpadResolveTasksHit(t *testing.T) {
	const pid = 4242
	const sessionID = "b2dae39b-7833-4a90-a6d2-de2edb061a7f"
	root := setupScratchpadFixture(t, sessionID, map[string]string{
		"tasks/abc123.output": "task output",
	})
	s := newTestScratchpad(t, root, pid, sessionID)

	full := filepath.Join(root, scratchpadFixtureSlug, sessionID, "tasks", "abc123.output")
	want, err := filepath.EvalSymlinks(full)
	if err != nil {
		want = full
	}
	if got := s.Resolve("tasks/abc123.output"); got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}
}

func TestSessionScratchpadResolveNonExistentFile(t *testing.T) {
	const pid = 4242
	const sessionID = "b2dae39b-7833-4a90-a6d2-de2edb061a7f"
	root := setupScratchpadFixture(t, sessionID, map[string]string{
		"scratchpad/hello.txt": "hi",
	})
	s := newTestScratchpad(t, root, pid, sessionID)

	if got := s.Resolve("scratchpad/does_not_exist.txt"); got != "" {
		t.Errorf("Resolve = %q, want empty for a missing file", got)
	}
}

func TestSessionScratchpadResolveWrongPrefix(t *testing.T) {
	const pid = 4242
	const sessionID = "b2dae39b-7833-4a90-a6d2-de2edb061a7f"
	root := setupScratchpadFixture(t, sessionID, map[string]string{
		"scratchpad/hello.txt": "hi",
	})
	s := newTestScratchpad(t, root, pid, sessionID)

	for _, rel := range []string{"src/hello.txt", "scratchpad", "tasks"} {
		if got := s.Resolve(rel); got != "" {
			t.Errorf("Resolve(%q) = %q, want empty: neither scratchpad/ nor tasks/ prefix should match", rel, got)
		}
	}
}

func TestSessionScratchpadResolveNoChildPID(t *testing.T) {
	const sessionID = "b2dae39b-7833-4a90-a6d2-de2edb061a7f"
	root := setupScratchpadFixture(t, sessionID, map[string]string{
		"scratchpad/hello.txt": "hi",
	})
	// No setChildPID call: childPID stays at its zero value.
	s := newSessionScratchpad()
	s.roots = []string{root}

	if got := s.Resolve("scratchpad/hello.txt"); got != "" {
		t.Errorf("Resolve = %q, want empty with no childPID set", got)
	}
}

func TestSessionScratchpadResolveEmptySessionID(t *testing.T) {
	const pid = 4242
	root := t.TempDir()
	// A session file with no sessionId, as if claude has not published its
	// UUID yet.
	s := newTestScratchpad(t, root, pid, "")

	if got := s.Resolve("scratchpad/hello.txt"); got != "" {
		t.Errorf("Resolve = %q, want empty when the session file has no sessionId", got)
	}
}

// TestLinkerScratchpadEndToEnd is the case the whole feature exists for: a
// path Claude Code prints as a bare relative path that does not exist under
// cwd, resolved through a Linker configured with LinkerOptions.Scratchpad.
func TestLinkerScratchpadEndToEnd(t *testing.T) {
	const pid = 4242
	const sessionID = "b2dae39b-7833-4a90-a6d2-de2edb061a7f"
	const rel = "scratchpad/tesla_beta_email.html"
	root := setupScratchpadFixture(t, sessionID, map[string]string{
		rel: "<html></html>",
	})
	s := newTestScratchpad(t, root, pid, sessionID)

	full := filepath.Join(root, scratchpadFixtureSlug, sessionID, rel)
	target, err := filepath.EvalSymlinks(full)
	if err != nil {
		target = full
	}

	cwd := t.TempDir()
	hostname := "testhost"
	var buf bytes.Buffer
	linker := NewLinker(LinkerOptions{
		Output:     &buf,
		Cwd:        cwd,
		Hostname:   hostname,
		Scheme:     "file",
		Domains:    []string{"github.com"},
		Scratchpad: s,
	})

	want := "\x1b]8;;file://" + hostname + target + "\x1b\\" + rel + "\x1b]8;;\x1b\\\n"
	assertWrite(t, linker, rel+"\n", want)
}
