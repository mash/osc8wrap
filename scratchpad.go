package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// scratchpadResolveThrottle bounds how often Resolve retries the session
// lookup while the UUID is still unknown. Claude Code publishes the session
// file a moment after launch, not at process start, so the first several
// writes of a fresh session arrive before it exists; retrying on every write
// would mean a Glob (and a sessions directory read) per byte chunk until then.
const scratchpadResolveThrottle = 2 * time.Second

// sessionScratchpad resolves bare "scratchpad/..." and "tasks/..." paths that
// Claude Code prints for files it writes outside the working tree, into the
// real file under the running session's per-session directory. The directory
// name is a UUID Claude Code only publishes a moment after it starts, so the
// lookup is lazy, retried, and must never block the byte stream in the
// meantime -- every failure mode here degrades to "" and leaves the text
// plain, exactly like any other unresolved path.
type sessionScratchpad struct {
	mu       sync.Mutex
	roots    []string // dirs to search; defaults to scratchpadRoots()
	childPID int
	dir      string // the session dir once found; empty until then
	lastTry  time.Time
}

func newSessionScratchpad() *sessionScratchpad {
	return &sessionScratchpad{roots: scratchpadRoots()}
}

// scratchpadRoots returns the directories under which Claude Code creates its
// per-session scratchpad. /tmp/claude-<uid> is what Claude Code uses on macOS
// today; os.TempDir() is added too, cheap insurance against a platform or a
// future version that honors TMPDIR instead, deduplicated against /tmp so a
// machine where they agree is not searched twice.
func scratchpadRoots() []string {
	uid := strconv.Itoa(os.Getuid())
	roots := []string{filepath.Join("/tmp", "claude-"+uid)}
	if tmp := os.TempDir(); tmp != "/tmp" {
		roots = append(roots, filepath.Join(tmp, "claude-"+uid))
	}
	return roots
}

// setChildPID records the pid of the wrapped command, which is how the
// running claude process's session file is found in loadClaudeSessions.
func (s *sessionScratchpad) setChildPID(pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.childPID = pid
}

// Resolve turns a bare "scratchpad/foo" or "tasks/foo" path -- what Claude
// Code prints, since the file does not exist relative to cwd -- into the real
// path under the session's directory, or "" if it cannot be resolved.
func (s *sessionScratchpad) Resolve(rel string) string {
	rel = strings.TrimPrefix(rel, "./")
	sub, name, ok := strings.Cut(rel, "/")
	if !ok || name == "" || (sub != "scratchpad" && sub != "tasks") {
		return ""
	}

	dir := s.sessionDir()
	if dir == "" {
		return ""
	}

	full := filepath.Join(dir, rel)
	if _, err := os.Stat(full); err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return full
	}
	return resolved
}

// sessionDir returns the session's scratchpad directory, finding and caching
// it on first success. Every step degrades to "" rather than erroring: this
// is a convenience resolver sitting in the hot Write path, not something
// allowed to interrupt the pty stream.
func (s *sessionScratchpad) sessionDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dir != "" {
		return s.dir
	}
	if s.childPID == 0 {
		return ""
	}
	// Claude has not published the session file yet, or never will (this
	// process is not claude). Either way, a Glob plus a sessions directory
	// read per Write call is too much, so retries are throttled.
	if time.Since(s.lastTry) < scratchpadResolveThrottle {
		return ""
	}
	s.lastTry = time.Now()

	sessions, err := loadClaudeSessions()
	if err != nil {
		return ""
	}
	f, ok := sessions[s.childPID]
	if !ok || f.SessionID == "" {
		return ""
	}

	// The UUID is unique on its own, so globbing the cwd-slug directory
	// underneath each root is enough; there is no need to compute the slug.
	for _, root := range s.roots {
		matches, err := filepath.Glob(filepath.Join(root, "*", f.SessionID))
		if err != nil {
			continue
		}
		for _, m := range matches {
			if info, err := os.Stat(m); err == nil && info.IsDir() {
				s.dir = m
				return s.dir
			}
		}
	}
	return ""
}
