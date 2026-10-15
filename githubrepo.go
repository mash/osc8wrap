package main

import (
	"regexp"
	"strings"
	"sync"
)

// maxGitConfigSize caps how much of .git/config githubRepo reads. A config
// file holds every remote and branch a checkout has accumulated, so it is
// bigger than the one-line files gitcontext.go reads, but 64 KiB is still
// far more than any hand-written config needs.
const maxGitConfigSize = 64 << 10

// githubOrgPattern accepts a GitHub organization or user login.
var githubOrgPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

// githubRepoURLPattern matches the three origin url forms git writes for a
// GitHub remote:
//
//	git@github.com:owner/repo.git
//	ssh://git@github.com/owner/repo.git
//	https://github.com/owner/repo.git
//
// The ".git" suffix and a trailing slash are both optional, and only
// github.com is recognized -- a GitHub Enterprise or non-GitHub host must not
// produce a link to the wrong place.
var githubRepoURLPattern = regexp.MustCompile(
	`^(?:git@github\.com:|(?:ssh://git@|https://)github\.com/)([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+?)(?:\.git)?/?$`,
)

// githubRepo is a lazily-resolved, cached "owner/repo" slug for a working
// directory's github.com origin remote. Resolution touches the filesystem,
// so it happens at most once per process even though every "#123" reference
// the linker sees asks for it.
type githubRepo struct {
	once sync.Once
	slug string // "owner/repo", empty when there is no github origin
	cwd  string
}

func newGitHubRepo(cwd string) *githubRepo {
	return &githubRepo{cwd: cwd}
}

// Slug returns the "owner/repo" of the origin remote, or "" when it cannot be
// determined -- no repository, no origin, a non-github host, or a url this
// parser does not recognize all degrade to "", which the linker reads as
// "leave the reference unlinked", the same way gitcontext.go degrades.
func (g *githubRepo) Slug() string {
	g.once.Do(func() {
		g.slug = resolveGitHubSlug(g.cwd)
	})
	return g.slug
}

func resolveGitHubSlug(cwd string) string {
	// resolveGitContext(gitcontext.go) returns the *main* repository root: a
	// linked worktree has no config of its own, and its origin remote lives
	// in the main checkout's .git/config.
	ctx := resolveGitContext(cwd)
	if ctx == (gitContext{}) {
		return ""
	}
	url := originURL(ctx.Root)
	if url == "" {
		return ""
	}
	return parseGitHubSlug(url)
}

// originURL finds the "url" set under "[remote \"origin\"]" in
// <root>/.git/config, or "" when there is no such remote.
func originURL(root string) string {
	content, ok := readSmallGitFile(root+"/.git/config", maxGitConfigSize)
	if !ok {
		return ""
	}

	inOrigin := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inOrigin = line == `[remote "origin"]`
			continue
		}
		if !inOrigin {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "url"); ok {
			rest = strings.TrimSpace(rest)
			if v, ok := strings.CutPrefix(rest, "="); ok {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// parseGitHubSlug extracts "owner/repo" from a github.com remote url, or ""
// when url does not name one.
func parseGitHubSlug(url string) string {
	m := githubRepoURLPattern.FindStringSubmatch(url)
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}
