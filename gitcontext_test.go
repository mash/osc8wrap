package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func gitTestMkdirAll(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func gitTestWriteFile(t *testing.T, path, content string) {
	t.Helper()
	gitTestMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeGitMainCheckout builds <root>/.git/HEAD, the shape of a plain clone.
func makeGitMainCheckout(t *testing.T, root, head string) string {
	t.Helper()
	gitTestMkdirAll(t, filepath.Join(root, ".git"))
	if head != "" {
		gitTestWriteFile(t, filepath.Join(root, ".git", "HEAD"), head)
	}
	return root
}

type gitWorktreeSpec struct {
	root         string // main checkout root
	name         string // admin directory name under .git/worktrees
	dir          string // the linked worktree's own directory
	head         string // contents of the worktree's HEAD; "" writes none
	relGitdir    bool   // spell "gitdir:" relative to dir
	relCommondir bool   // spell commondir relative to the gitdir
	noCommondir  bool   // omit commondir entirely
}

// makeGitLinkedWorktree builds a `git worktree add` layout: an admin directory
// under the main repository plus a .git *file* in the worktree pointing at it.
func makeGitLinkedWorktree(t *testing.T, s gitWorktreeSpec) string {
	t.Helper()
	admin := gitTestMkdirAll(t, filepath.Join(s.root, ".git", "worktrees", s.name))
	if s.head != "" {
		gitTestWriteFile(t, filepath.Join(admin, "HEAD"), s.head)
	}
	if !s.noCommondir {
		commondir := filepath.Join(s.root, ".git")
		if s.relCommondir {
			rel, err := filepath.Rel(admin, commondir)
			if err != nil {
				t.Fatal(err)
			}
			commondir = rel
		}
		gitTestWriteFile(t, filepath.Join(admin, "commondir"), commondir+"\n")
	}

	gitTestMkdirAll(t, s.dir)
	gitdir := admin
	if s.relGitdir {
		rel, err := filepath.Rel(s.dir, admin)
		if err != nil {
			t.Fatal(err)
		}
		gitdir = rel
	}
	gitTestWriteFile(t, filepath.Join(s.dir, ".git"), "gitdir: "+gitdir+"\n")
	return s.dir
}

func TestResolveGitContext(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, tmp string) (dir string, want gitContext)
	}{
		{
			name: "main checkout",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/main\n")
				return root, gitContext{Branch: "main", Worktree: root, Root: root}
			},
		},
		{
			name: "cwd below the checkout root",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/main\n")
				deep := gitTestMkdirAll(t, filepath.Join(root, "internal", "server", "handler"))
				return deep, gitContext{Branch: "main", Worktree: root, Root: root}
			},
		},
		{
			name: "branch name containing slashes",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/feat/a/b\n")
				return root, gitContext{Branch: "feat/a/b", Worktree: root, Root: root}
			},
		},
		{
			name: "detached head",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"),
					"9f1c0d3a5b7e2f4c6d8a0b1c2d3e4f5a6b7c8d9e\n")
				return root, gitContext{Branch: "9f1c0d3", Worktree: root, Root: root}
			},
		},
		{
			name: "linked worktree, absolute gitdir and commondir",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "nature-server"), "ref: refs/heads/develop\n")
				name := "drop-from-from-bulk-createmessage"
				dir := makeGitLinkedWorktree(t, gitWorktreeSpec{
					root: root,
					name: name,
					dir:  filepath.Join(root, ".claude", "worktrees", name),
					head: "ref: refs/heads/refactor/drop-from-from-bulk-createmessage\n",
				})
				return dir, gitContext{
					Branch:   "refactor/drop-from-from-bulk-createmessage",
					Worktree: dir,
					Root:     root,
				}
			},
		},
		{
			name: "linked worktree, relative gitdir and commondir",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "nature-server"), "ref: refs/heads/develop\n")
				// The directory name and the branch disagree in the real data.
				dir := makeGitLinkedWorktree(t, gitWorktreeSpec{
					root:         root,
					name:         "feature+dynamic-segment-pr2",
					dir:          filepath.Join(root, ".claude", "worktrees", "feature+dynamic-segment-pr2"),
					head:         "ref: refs/heads/feature/dynamic-segment-pr4\n",
					relGitdir:    true,
					relCommondir: true,
				})
				return dir, gitContext{
					Branch:   "feature/dynamic-segment-pr4",
					Worktree: dir,
					Root:     root,
				}
			},
		},
		{
			name: "linked worktree outside the main checkout",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/main\n")
				dir := makeGitLinkedWorktree(t, gitWorktreeSpec{
					root:      root,
					name:      "wt",
					dir:       filepath.Join(tmp, "elsewhere", "wt"),
					head:      "ref: refs/heads/topic\n",
					relGitdir: true,
				})
				return dir, gitContext{Branch: "topic", Worktree: dir, Root: root}
			},
		},
		{
			name: "cwd below a linked worktree",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/main\n")
				dir := makeGitLinkedWorktree(t, gitWorktreeSpec{
					root: root,
					name: "wt",
					dir:  filepath.Join(root, ".claude", "worktrees", "wt"),
					head: "ref: refs/heads/topic\n",
				})
				deep := gitTestMkdirAll(t, filepath.Join(dir, "cmd", "server"))
				return deep, gitContext{Branch: "topic", Worktree: dir, Root: root}
			},
		},
		{
			name: "linked worktree with detached head",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/main\n")
				dir := makeGitLinkedWorktree(t, gitWorktreeSpec{
					root: root,
					name: "wt",
					dir:  filepath.Join(root, ".claude", "worktrees", "wt"),
					head: "abcdef0123456789abcdef0123456789abcdef01\n",
				})
				return dir, gitContext{Branch: "abcdef0", Worktree: dir, Root: root}
			},
		},
		{
			name: "not in a repository",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				return gitTestMkdirAll(t, filepath.Join(tmp, "plain", "dir")), gitContext{}
			},
		},
		{
			name: "deleted worktree directory",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/main\n")
				dir := makeGitLinkedWorktree(t, gitWorktreeSpec{
					root: root,
					name: "wt",
					dir:  filepath.Join(root, ".claude", "worktrees", "wt"),
					head: "ref: refs/heads/topic\n",
				})
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				// The surviving ancestors are a checkout; reporting their
				// branch for a deleted worktree would be a lie.
				return dir, gitContext{}
			},
		},
		{
			name: "empty directory argument",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				return "", gitContext{}
			},
		},
		{
			name: "path is a file, not a directory",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/main\n")
				path := filepath.Join(root, "main.go")
				gitTestWriteFile(t, path, "package main\n")
				return path, gitContext{}
			},
		},
		{
			name: "malformed .git file",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				dir := gitTestMkdirAll(t, filepath.Join(tmp, "broken"))
				gitTestWriteFile(t, filepath.Join(dir, ".git"), "this is not a gitdir pointer\n")
				return dir, gitContext{}
			},
		},
		{
			name: "empty .git file",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				dir := gitTestMkdirAll(t, filepath.Join(tmp, "broken"))
				gitTestWriteFile(t, filepath.Join(dir, ".git"), "")
				return dir, gitContext{}
			},
		},
		{
			name: "gitdir points nowhere",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				dir := gitTestMkdirAll(t, filepath.Join(tmp, "stale"))
				gitTestWriteFile(t, filepath.Join(dir, ".git"),
					"gitdir: "+filepath.Join(tmp, "gone", ".git", "worktrees", "stale")+"\n")
				return dir, gitContext{}
			},
		},
		{
			name: "worktree without commondir",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/main\n")
				dir := makeGitLinkedWorktree(t, gitWorktreeSpec{
					root:        root,
					name:        "wt",
					dir:         filepath.Join(root, ".claude", "worktrees", "wt"),
					head:        "ref: refs/heads/topic\n",
					noCommondir: true,
				})
				return dir, gitContext{}
			},
		},
		{
			name: "missing HEAD in main checkout",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				return makeGitMainCheckout(t, filepath.Join(tmp, "repo"), ""), gitContext{}
			},
		},
		{
			name: "missing HEAD in linked worktree",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/main\n")
				dir := makeGitLinkedWorktree(t, gitWorktreeSpec{
					root: root,
					name: "wt",
					dir:  filepath.Join(root, ".claude", "worktrees", "wt"),
				})
				return dir, gitContext{}
			},
		},
		{
			name: "unparseable HEAD",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				return makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "garbage\n"), gitContext{}
			},
		},
		{
			name: "empty HEAD",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				return makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "\n"), gitContext{}
			},
		},
		{
			name: "HEAD naming an empty branch",
			setup: func(t *testing.T, tmp string) (string, gitContext) {
				return makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/\n"), gitContext{}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, want := tt.setup(t, t.TempDir())
			if got := resolveGitContext(dir); got != want {
				t.Errorf("resolveGitContext(%q) = %+v, want %+v", dir, got, want)
			}
		})
	}
}

func TestResolveGitContextTrailingSeparator(t *testing.T) {
	root := makeGitMainCheckout(t, filepath.Join(t.TempDir(), "repo"), "ref: refs/heads/main\n")
	want := gitContext{Branch: "main", Worktree: root, Root: root}
	if got := resolveGitContext(root + string(filepath.Separator)); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestGitContextResolverCachesHits(t *testing.T) {
	tmp := t.TempDir()
	root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/main\n")
	want := gitContext{Branch: "main", Worktree: root, Root: root}

	r := newGitContextResolver()
	if got := r.Resolve(root); got != want {
		t.Fatalf("first Resolve = %+v, want %+v", got, want)
	}

	// Removing the checkout proves the second answer came from the cache and
	// not from the filesystem.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve(root); got != want {
		t.Errorf("cached Resolve = %+v, want %+v", got, want)
	}
	if got := newGitContextResolver().Resolve(root); got != (gitContext{}) {
		t.Errorf("uncached Resolve of a deleted checkout = %+v, want zero", got)
	}
}

// TestGitContextResolverEmptyDir pins totality at the caching layer, which
// resolveGitContext's own empty-string case does not cover: filepath.Abs("")
// returns the process's working directory, so an unguarded Resolve would walk
// up from wherever osc8wrap was run — a real repository, whose branch would
// then be attributed to a session that never recorded a cwd.
func TestGitContextResolverEmptyDir(t *testing.T) {
	// Standing in a checkout is what makes the unguarded answer non-zero, and
	// it is the normal case: the user runs osc8wrap from a repository.
	t.Chdir(makeGitMainCheckout(t, filepath.Join(t.TempDir(), "repo"), "ref: refs/heads/main\n"))

	r := newGitContextResolver()
	if got := r.Resolve(""); got != (gitContext{}) {
		t.Errorf(`Resolve("") = %+v, want zero`, got)
	}
	// Again, in case the first call cached something under the cwd's key.
	if got := r.Resolve(""); got != (gitContext{}) {
		t.Errorf(`second Resolve("") = %+v, want zero`, got)
	}
}

// TestGitContextResolverZeroValue guards the nil cache map: the type is usable
// without its constructor, and a write to a nil map panics.
func TestGitContextResolverZeroValue(t *testing.T) {
	root := makeGitMainCheckout(t, filepath.Join(t.TempDir(), "repo"), "ref: refs/heads/main\n")
	want := gitContext{Branch: "main", Worktree: root, Root: root}

	var r gitContextResolver
	if got := r.Resolve(root); got != want {
		t.Fatalf("zero-value Resolve = %+v, want %+v", got, want)
	}
	if got := r.Resolve(root); got != want {
		t.Errorf("second Resolve = %+v, want %+v", got, want)
	}
}

func TestGitContextResolverCachesMisses(t *testing.T) {
	tmp := t.TempDir()
	dir := gitTestMkdirAll(t, filepath.Join(tmp, "plain"))

	r := newGitContextResolver()
	if got := r.Resolve(dir); got != (gitContext{}) {
		t.Fatalf("first Resolve = %+v, want zero", got)
	}

	makeGitMainCheckout(t, dir, "ref: refs/heads/main\n")
	if got := r.Resolve(dir); got != (gitContext{}) {
		t.Errorf("cached Resolve = %+v, want zero", got)
	}
}

func TestGitContextResolverConcurrent(t *testing.T) {
	tmp := t.TempDir()
	root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/main\n")
	deep := gitTestMkdirAll(t, filepath.Join(root, "a", "b"))
	want := gitContext{Branch: "main", Worktree: root, Root: root}

	r := newGitContextResolver()
	var wg sync.WaitGroup
	for _, dir := range []string{root, deep, root, deep} {
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if got := r.Resolve(dir); got != want {
					t.Errorf("Resolve(%q) = %+v, want %+v", dir, got, want)
				}
			}()
		}
	}
	wg.Wait()
}
