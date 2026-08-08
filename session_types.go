package main

import "time"

// RegistryEntry is what an osc8wrap process records about itself while it
// wraps a command. One JSON file per wrapper pid lives in the registry
// directory; the file is removed when the wrapper exits.
type RegistryEntry struct {
	// WrapperPID is the pid of the osc8wrap process itself.
	WrapperPID int `json:"wrapper_pid"`
	// ChildPID is the pid of the wrapped command (e.g. claude).
	ChildPID int `json:"child_pid"`
	// TTY is the absolute path of osc8wrap's controlling terminal, e.g.
	// "/dev/ttys011". This is the terminal emulator's pty, not the inner pty
	// osc8wrap creates for the child, so writing to it reaches Ghostty.
	TTY string `json:"tty"`
	// SurfaceID is GHOSTTY_SURFACE_ID as inherited from the environment, e.g.
	// "0x1552e82d3b500c1e". Empty when not running under Ghostty.
	SurfaceID string `json:"surface_id"`
	// Cwd is the working directory osc8wrap was started in.
	Cwd string `json:"cwd"`
	// Command is the wrapped argv.
	Command []string `json:"command"`
	// StartedAt is when the wrapper registered itself.
	StartedAt time.Time `json:"started_at"`
}

// LiveSession is a session running under an osc8wrap wrapper right now: a
// registry entry joined with the metadata Claude Code publishes in
// ~/.claude/sessions/<pid>.json. Being live is what makes it navigable, so
// this — not a finished session — is what Navigator accepts. Fields sourced
// from Claude Code are empty when no matching file was found.
type LiveSession struct {
	Entry RegistryEntry

	// SessionID is the claude session UUID, suitable for `claude --resume`.
	SessionID string
	// Name is claude's own label for the session, e.g. "osc8wrap-74".
	Name string
	// Status is claude's reported state: "busy", "idle", "waiting" or "".
	Status string
	// WaitingFor describes why the session is blocked, e.g. "input needed".
	WaitingFor string
	// Modified is the last time claude updated its session metadata.
	Modified time.Time
}

// Session is one row of the unified listing: a Claude Code session, running or
// long finished. "Which session was that, and where is it" has one answer while
// it runs (go to its window) and another once it has stopped (claude --resume),
// but it is one question, so it is one type.
//
// Everything outside Live is what survives the process: the transcript on disk
// and the git context of the directory it was started in.
type Session struct {
	// ID is the claude session UUID. It is empty only when there is no
	// transcript to take it from: the wrapped command is not claude, or claude
	// has not published its session file yet.
	ID string
	// Live is the running wrapper, or nil when nothing is running. A nil Live
	// is what makes a session unfocusable, which is why Navigator takes a
	// LiveSession and this field is a pointer rather than a bool plus a value.
	Live *LiveSession

	// TranscriptPath is the .jsonl claude appends to, and ProjectDir its parent
	// under ~/.claude/projects. Both empty for a live session with no
	// transcript yet.
	TranscriptPath string
	ProjectDir     string

	// Cwd is the directory the session was STARTED in — not wherever it has
	// since cd'd, and not necessarily where osc8wrap was launched. Claude files
	// the transcript under the project folder derived from this directory and
	// `claude --resume` only finds the session when run from it, so this is the
	// single field the resume path depends on.
	Cwd string

	// Branch, Worktree and Root are the git identity of Cwd. Root is the main
	// repository root and equals Worktree in a main checkout, so a renderer
	// wanting "repo/worktree" compares them rather than testing Worktree for
	// emptiness. All three are empty when Cwd is not in a repository or no
	// longer exists — deleted worktrees are common and must not drop the row.
	Branch   string
	Worktree string
	Root     string

	// Prompt is the first real user prompt. Empty is normal, not an error:
	// roughly one transcript in twelve never yields one.
	Prompt string
	// Modified is claude's own updatedAt while live and the transcript's mtime
	// once finished. The two clocks are not comparable, which is harmless
	// because live rows sort above finished ones unconditionally.
	Modified time.Time

	// Match is why this row survived the query: an excerpt of the conversation
	// around the first hit, raw as the transcript wrote it. Empty when nothing
	// was searched for, which is the only reason a renderer may treat it as
	// optional — under a query every listed row has one, because a row that
	// matched nothing is not listed at all. It is not sanitized here; like
	// Prompt it reaches the terminal only through sanitizeField.
	Match string
}

// wrapperPID is the pid of the osc8wrap process hosting this session, or 0 when
// it is finished. It is the identity of a live session that has no UUID yet.
func (s Session) wrapperPID() int {
	if s.Live == nil {
		return 0
	}
	return s.Live.Entry.WrapperPID
}

// Navigator brings the terminal surface hosting a session to the foreground.
type Navigator interface {
	// Focus makes the given session's surface the focused one. It returns an
	// error describing what was unreachable rather than partially navigating.
	Focus(s LiveSession) error
	// Available reports whether navigation can work at all in this
	// environment, with a human-readable reason when it cannot.
	Available() (bool, string)
}
