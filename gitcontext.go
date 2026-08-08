package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// gitContext is the git identity of a directory: which branch it is on, which
// checkout it lives in, and which repository that checkout belongs to.
//
// Root is always the main repository root, and equals Worktree for a main
// checkout. A caller rendering "repo/worktree" must therefore compare the two
// rather than test Worktree for emptiness.
type gitContext struct {
	Branch   string // branch name, or a short sha when HEAD is detached
	Worktree string // root of the checkout containing the directory
	Root     string // main repository root; == Worktree for a main checkout
}

const (
	// Deep trees exist, but a .git that is 64 levels above the cwd means we
	// walked out of the project rather than found it.
	maxGitWalkDepth = 64
	// .git, HEAD and commondir are one short line each; anything larger is
	// not the file we think it is.
	maxGitFileSize = 8 << 10
	shortSHALen    = 7
)

// gitContextResolver caches resolution per directory. The session listing asks
// the same question for every session, and hundreds of sessions share a few
// dozen cwds; without the cache the walk-and-stat cost is paid per session.
type gitContextResolver struct {
	mu    sync.Mutex
	cache map[string]gitContext
}

func newGitContextResolver() *gitContextResolver {
	return &gitContextResolver{cache: make(map[string]gitContext)}
}

// Resolve never fails: a directory outside a repository, a deleted directory
// and a broken checkout all yield the zero gitContext, cached like any other
// answer. A listing must not lose a row because a worktree was removed.
func (r *gitContextResolver) Resolve(dir string) gitContext {
	// filepath.Abs("") yields the process's own working directory rather than
	// an error, so without this an unrecorded cwd would be answered with the
	// branch of wherever the user happened to run osc8wrap.
	if dir == "" {
		return gitContext{}
	}

	key := dir
	if abs, err := filepath.Abs(dir); err == nil {
		key = abs
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx, ok := r.cache[key]; ok {
		return ctx
	}
	ctx := resolveGitContext(key)
	if r.cache == nil {
		// The zero value is usable: a nil map reads fine but panics on write.
		r.cache = make(map[string]gitContext)
	}
	r.cache[key] = ctx
	return ctx
}

// resolveGitContext reads small files only — never git itself, which would cost
// a fork per session. Resolution is all-or-nothing: any step that fails yields
// the zero value, so a half-filled record can never reach the listing.
func resolveGitContext(dir string) gitContext {
	if dir == "" {
		return gitContext{}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return gitContext{}
	}
	// A deleted worktree still has ancestors that are checkouts; reporting
	// theirs would attribute the wrong branch to a session whose directory
	// is gone.
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return gitContext{}
	}

	cur := abs
	for range maxGitWalkDepth {
		dotGit := filepath.Join(cur, ".git")
		switch fi, err := os.Stat(dotGit); {
		case err != nil:
			// Not here; keep walking up.
		case fi.IsDir():
			return contextFromGitDir(dotGit, cur, cur)
		default:
			return linkedWorktreeContext(dotGit, cur)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return gitContext{}
}

func contextFromGitDir(gitDir, worktree, root string) gitContext {
	branch := readGitHead(gitDir)
	if branch == "" {
		return gitContext{}
	}
	return gitContext{Branch: branch, Worktree: worktree, Root: root}
}

// linkedWorktreeContext handles a .git that is a file: it points at the
// worktree's administrative directory under the main repository, and the
// commondir beside it names the main repository's own .git.
func linkedWorktreeContext(gitFile, worktree string) gitContext {
	content, ok := readSmallGitFile(gitFile)
	if !ok {
		return gitContext{}
	}
	gitDir, ok := parseGitdirPointer(content, worktree)
	if !ok {
		return gitContext{}
	}
	root, ok := mainRootFromCommondir(gitDir)
	if !ok {
		return gitContext{}
	}
	return contextFromGitDir(gitDir, worktree, root)
}

// parseGitdirPointer reads the "gitdir: <path>" line, whose path git may spell
// relative to the directory holding the .git file.
func parseGitdirPointer(content, base string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		rest, found := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
		if !found {
			continue
		}
		return existingGitDir(strings.TrimSpace(rest), base)
	}
	return "", false
}

// mainRootFromCommondir resolves the repository root: commondir names the main
// repository's .git directory, possibly relative to the worktree's gitdir, so
// the root is that directory's parent.
func mainRootFromCommondir(gitDir string) (string, bool) {
	content, ok := readSmallGitFile(filepath.Join(gitDir, "commondir"))
	if !ok {
		return "", false
	}
	commonDir, ok := existingGitDir(strings.TrimSpace(content), gitDir)
	if !ok {
		return "", false
	}
	return filepath.Dir(commonDir), true
}

func existingGitDir(path, base string) (string, bool) {
	if path == "" {
		return "", false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	path = filepath.Clean(path)
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return "", false
	}
	return path, true
}

// readGitHead returns the branch name, or the short sha of a detached HEAD.
// An empty result means "no usable HEAD" and fails the whole resolution.
func readGitHead(gitDir string) string {
	content, ok := readSmallGitFile(filepath.Join(gitDir, "HEAD"))
	if !ok {
		return ""
	}
	head, _, _ := strings.Cut(content, "\n")
	head = strings.TrimSpace(head)

	if ref, found := strings.CutPrefix(head, "ref:"); found {
		return strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
	}
	// Detached: git shows an abbreviated sha here, and so do we. Anything
	// that is not a sha is a HEAD we do not understand.
	if len(head) < shortSHALen || !isHexString(head) {
		return ""
	}
	return head[:shortSHALen]
}

func readSmallGitFile(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close() //nolint:errcheck

	b, err := io.ReadAll(io.LimitReader(f, maxGitFileSize))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func isHexString(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
