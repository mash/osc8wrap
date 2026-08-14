package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
	idx := NewFileIndex("/project", []string{"vendor", ".claude/worktrees", "/Pods/", "", "/"})

	if !idx.excludeSet["vendor"] || !idx.excludeSet["Pods"] {
		t.Errorf("plain names should land in excludeSet, got %v", idx.excludeSet)
	}
	if len(idx.excludePath) != 1 || idx.excludePath[0] != "/.claude/worktrees" {
		t.Errorf("path entries should be anchored with a leading slash, got %v", idx.excludePath)
	}
}

func TestIsIgnoredDir_PathSuffix(t *testing.T) {
	idx := NewFileIndex("/project", []string{"DerivedData", ".claude/worktrees"})

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
	idx := NewFileIndex("/project", defaultExcludeDirs)

	for _, path := range []string{
		"/project/ios/DerivedData",
		"/project/ios/Pods",
		"/project/.claude/worktrees/feat-x",
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

	idx := NewFileIndex(tmp, []string{})
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
