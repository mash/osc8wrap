package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fsnotify/fsnotify"
)

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestLoadGitIgnoredDirs(t *testing.T) {
	tmp := t.TempDir()
	tmp, _ = filepath.EvalSymlinks(tmp)
	initGitRepo(t, tmp)

	// Create .gitignore
	os.WriteFile(filepath.Join(tmp, ".gitignore"), []byte("build/\nDerivedData/\n"), 0o644)

	// Create ignored directories with files
	os.MkdirAll(filepath.Join(tmp, "build", "out"), 0o755)
	os.WriteFile(filepath.Join(tmp, "build", "out", "app"), []byte("."), 0o644)
	os.MkdirAll(filepath.Join(tmp, "DerivedData", "index"), 0o755)
	os.WriteFile(filepath.Join(tmp, "DerivedData", "index", "db"), []byte("."), 0o644)

	// Create a tracked directory
	os.MkdirAll(filepath.Join(tmp, "src"), 0o755)
	os.WriteFile(filepath.Join(tmp, "src", "main.go"), []byte("package main"), 0o644)

	ctx := context.Background()
	dirs := loadGitIgnoredDirs(ctx, tmp)

	if dirs == nil {
		t.Fatal("loadGitIgnoredDirs returned nil")
	}
	if !dirs[filepath.Join(tmp, "build")] {
		t.Error("expected build/ to be ignored")
	}
	if !dirs[filepath.Join(tmp, "DerivedData")] {
		t.Error("expected DerivedData/ to be ignored")
	}
	if dirs[filepath.Join(tmp, "src")] {
		t.Error("src/ should not be ignored")
	}
}

func TestLoadGitIgnoredDirs_NotGitRepo(t *testing.T) {
	tmp := t.TempDir()
	ctx := context.Background()
	dirs := loadGitIgnoredDirs(ctx, tmp)
	if dirs != nil {
		t.Errorf("expected nil for non-git repo, got %v", dirs)
	}
}

func TestIsIgnoredDir(t *testing.T) {
	idx := &FileIndex{
		excludeSet:  map[string]bool{"vendor": true, ".git": true},
		ignoredDirs: map[string]bool{"/project/build": true, "/project/DerivedData": true},
	}

	tests := []struct {
		path string
		want bool
	}{
		{"/project/vendor", true},      // excludeSet match by basename
		{"/project/sub/vendor", true},  // excludeSet match by basename (nested)
		{"/project/.git", true},        // excludeSet match
		{"/project/build", true},       // ignoredDirs match
		{"/project/DerivedData", true}, // ignoredDirs match
		{"/project/src", false},        // not ignored
		{"/project/build/sub", false},  // child of ignored dir is not in map
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := idx.isIgnoredDir(tt.path); got != tt.want {
				t.Errorf("isIgnoredDir(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestNewFileIndex_SplitsNamesFromPathSuffixes(t *testing.T) {
	idx := NewFileIndex("/project", []string{"vendor", ".claude/worktrees", "/Pods/", "", "/"}, 0)

	if !idx.excludeSet["vendor"] || !idx.excludeSet["Pods"] {
		t.Errorf("plain names should land in excludeSet, got %v", idx.excludeSet)
	}
	if len(idx.excludePath) != 1 || idx.excludePath[0] != "/.claude/worktrees" {
		t.Errorf("path entries should be anchored with a leading slash, got %v", idx.excludePath)
	}
}

func TestIsIgnoredDir_PathSuffix(t *testing.T) {
	idx := NewFileIndex("/project", []string{"DerivedData", ".claude/worktrees"}, 0)

	tests := []struct {
		path string
		want bool
	}{
		// A name matches at any depth. This is what catches build output inside
		// a linked git worktree, whose .gitignore the outer repo never reads.
		{"/project/DerivedData", true},
		{"/project/.claude/worktrees/feat-x/ios/DerivedData", true},
		// A path entry matches a relative suffix, so the whole worktree subtree
		// is skipped while ".claude" itself stays indexed.
		{"/project/.claude/worktrees", true},
		{"/project/.claude/worktrees/feat-x/ios", true},
		{"/project/.claude", false},
		{"/project/.claude/skills", false},
		// A suffix must align on a separator, so a longer name does not match.
		{"/project/my.claude/worktrees", false},
		{"/project/worktrees", false},
		{"/project/src", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := idx.isIgnoredDir(tt.path); got != tt.want {
				t.Errorf("isIgnoredDir(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestDefaultExcludeDirs_CoversXcodeTrees(t *testing.T) {
	idx := NewFileIndex("/project", defaultExcludeDirs, 0)

	for _, path := range []string{
		"/project/ios/DerivedData",
		"/project/ios/Pods",
		"/project/.claude/worktrees/feat-x",
		"/project/.worktrees",
		"/project/nested/.worktrees",
		"/project/node_modules",
	} {
		if !idx.isIgnoredDir(path) {
			t.Errorf("default excludes should ignore %q", path)
		}
	}
	if idx.isIgnoredDir("/project/ios/Nature") {
		t.Error("default excludes should not ignore project sources")
	}
}

func TestNewFileIndex_MaxWatchesDefault(t *testing.T) {
	// A caller that has no opinion, and a bad env value that parseArgs left at
	// zero, both have to land on the default rather than on "no cap".
	for _, n := range []int{0, -1} {
		if idx := NewFileIndex("/project", nil, n); idx.maxWatches != defaultMaxWatches {
			t.Errorf("NewFileIndex(..., %d).maxWatches = %d, want %d", n, idx.maxWatches, defaultMaxWatches)
		}
	}
	if idx := NewFileIndex("/project", nil, 5); idx.maxWatches != 5 {
		t.Errorf("maxWatches = %d, want 5", idx.maxWatches)
	}
}

func TestWatchDirRecursive_StopsAtMaxWatches(t *testing.T) {
	tmp := t.TempDir()
	tmp, _ = filepath.EvalSymlinks(tmp)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		if err := os.MkdirAll(filepath.Join(tmp, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Six directories including the root, against a cap of three.
	idx := NewFileIndex(tmp, nil, 3)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Skipf("fsnotify unavailable: %v", err)
	}
	defer watcher.Close() //nolint:errcheck
	idx.watcher = watcher

	idx.watchDirRecursive(tmp)

	if idx.watchCount != 3 {
		t.Errorf("watchCount = %d, want 3", idx.watchCount)
	}
	if got := len(watcher.WatchList()); got != 3 {
		t.Errorf("watcher holds %d paths, want 3", got)
	}
	if !idx.watchWarned {
		t.Error("reaching the cap should have warned")
	}
}

func TestWatchDirRecursive_UnderMaxWatchesDoesNotWarn(t *testing.T) {
	tmp := t.TempDir()
	tmp, _ = filepath.EvalSymlinks(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	idx := NewFileIndex(tmp, nil, 100)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Skipf("fsnotify unavailable: %v", err)
	}
	defer watcher.Close() //nolint:errcheck
	idx.watcher = watcher

	idx.watchDirRecursive(tmp)

	if idx.watchCount != 2 {
		t.Errorf("watchCount = %d, want 2", idx.watchCount)
	}
	if idx.watchWarned {
		t.Error("a tree below the cap must not warn")
	}
}

func TestWarnWatchLimit_WarnsOnce(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w

	idx := NewFileIndex("/project", nil, 7)
	idx.warnWatchLimit()
	idx.warnWatchLimit()

	w.Close() //nolint:errcheck
	os.Stderr = orig
	out, _ := io.ReadAll(r)

	if n := strings.Count(string(out), "watch limit reached"); n != 1 {
		t.Errorf("warned %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(string(out), "7") {
		t.Errorf("the warning should name the cap it hit, got %q", out)
	}
}

func TestIsIgnoredDir_NilIgnoredDirs(t *testing.T) {
	idx := &FileIndex{
		excludeSet:  map[string]bool{"vendor": true},
		ignoredDirs: nil,
	}
	// Should not panic with nil ignoredDirs
	if idx.isIgnoredDir("/project/vendor") != true {
		t.Error("expected vendor to be ignored via excludeSet")
	}
	if idx.isIgnoredDir("/project/src") != false {
		t.Error("expected src to not be ignored")
	}
}

func TestFileIndex_GitignoreDirsExcludedFromIndex(t *testing.T) {
	tmp := t.TempDir()
	tmp, _ = filepath.EvalSymlinks(tmp)
	initGitRepo(t, tmp)

	os.WriteFile(filepath.Join(tmp, ".gitignore"), []byte("build/\n"), 0o644)

	// Ignored directory with a file
	os.MkdirAll(filepath.Join(tmp, "build"), 0o755)
	os.WriteFile(filepath.Join(tmp, "build", "app.go"), []byte("package build"), 0o644)

	// Tracked directory with a file
	os.MkdirAll(filepath.Join(tmp, "src"), 0o755)
	os.WriteFile(filepath.Join(tmp, "src", "main.go"), []byte("package main"), 0o644)

	idx := NewFileIndex(tmp, []string{}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go idx.Start(ctx)
	if err := idx.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	// main.go in src/ should be indexed
	if resolved := idx.Resolve("main.go"); resolved == "" {
		t.Error("expected main.go to be resolved from index")
	}

	// app.go in build/ should NOT be indexed (gitignored)
	if resolved := idx.Resolve("app.go"); resolved != "" {
		t.Errorf("expected app.go to not be in index, got %q", resolved)
	}
}
