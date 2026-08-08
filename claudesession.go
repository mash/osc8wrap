package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// claudeSessionFile is the subset of ~/.claude/sessions/<pid>.json that
// osc8wrap reads. Claude Code versions independently of osc8wrap and may add,
// drop or rename fields at any time, so every field here is optional: unknown
// keys are ignored by encoding/json and missing keys stay at their zero value.
type claudeSessionFile struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	WaitingFor string `json:"waitingFor"`
	// UpdatedAt is unix milliseconds, not seconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// claudeSessionsDir goes through claudeDir so that CLAUDE_CONFIG_DIR moves the
// live metadata and the transcripts under projects/ together; pointing one at a
// test fixture while the other still read the real home would silently mix them.
func claudeSessionsDir() string {
	return filepath.Join(claudeDir(), "sessions")
}

// loadClaudeSessions reads every session file claude has published, keyed by
// the pid of the claude process. Files that cannot be read or parsed are
// skipped rather than failing the whole listing, since claude rewrites them
// while we read and a partial write must not hide the other sessions.
//
// The directory itself is treated the same way: this metadata only decorates
// the registry listing, so an absent, unreadable or non-directory path yields
// no sessions instead of failing `osc8wrap sessions` outright. A directory that
// exists but cannot be read is worth a line on stderr, since the listing that
// follows is then quietly missing every name and status; an absent one is the
// normal case for anyone not running Claude Code and stays silent.
func loadClaudeSessions() (map[int]claudeSessionFile, error) {
	dir := claudeSessionsDir()
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "osc8wrap sessions: ignoring unreadable %s: %v\n", dir, err)
		}
		return map[int]claudeSessionFile{}, nil //nolint:nilerr // decorative metadata: degrade to none
	}

	sessions := make(map[int]claudeSessionFile, len(dirEntries))
	for _, de := range dirEntries {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var f claudeSessionFile
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}
		if f.PID == 0 {
			pid, err := strconv.Atoi(strings.TrimSuffix(name, ".json"))
			if err != nil {
				continue
			}
			f.PID = pid
		}
		sessions[f.PID] = f
	}
	return sessions, nil
}

// joinSessions pairs each registry entry with the claude metadata for its
// child pid. Entries without a match are kept: osc8wrap may wrap something
// that is not claude, or claude may not have published its file yet.
func joinSessions(entries []RegistryEntry, claude map[int]claudeSessionFile) []LiveSession {
	sessions := make([]LiveSession, 0, len(entries))
	for _, e := range entries {
		s := LiveSession{Entry: e}
		if f, ok := claude[e.ChildPID]; ok {
			s.SessionID = f.SessionID
			s.Name = f.Name
			s.Status = f.Status
			s.WaitingFor = f.WaitingFor
			if f.UpdatedAt != 0 {
				s.Modified = time.UnixMilli(f.UpdatedAt)
			}
		}
		sessions = append(sessions, s)
	}

	sort.Slice(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		if ra, rb := statusRank(a.Status), statusRank(b.Status); ra != rb {
			return ra < rb
		}
		if !a.Modified.Equal(b.Modified) {
			return a.Modified.After(b.Modified)
		}
		return a.Entry.WrapperPID < b.Entry.WrapperPID
	})
	return sessions
}

// statusRank orders the picker so the sessions that want the user's attention
// come first.
func statusRank(status string) int {
	switch status {
	case "waiting":
		return 0
	case "busy":
		return 1
	default:
		return 2
	}
}

// mergeSessions folds the running wrappers and the transcripts on disk into one
// row per session, keyed on the claude UUID so that a session and its own
// transcript are a single record rather than two rows the user has to recognise
// as the same thing. A live session with no UUID — the wrapped command is not
// claude, or claude has not published its file yet — becomes a standalone
// record identified by its wrapper pid.
//
// Accepted duplicate: for the sub-second window before a fresh claude writes
// ~/.claude/sessions/<pid>.json it has no UUID to merge on, so it appears twice,
// once as a pid-keyed live row and once as its own transcript. Pairing them
// would mean guessing from cwd and start time, which goes wrong in exactly the
// case that matters — several sessions started in the same directory at once.
//
// Two live wrappers can report the same UUID — the README's re-wrap variant
// runs `osc8wrap claude --resume <id>` on a row that is still live, and claude
// publishes the resumed session under the same UUID. Only the first row keeps
// it; the second falls back to its wrapper pid, which sessionToken renders and
// sessionMatchesID accepts. Leaving both rows on one UUID would print two
// identical-looking records and make `open <uuid>` ambiguous for ever, so
// neither could be reached.
//
// The only I/O is the bounded head read per transcript and the small file reads
// behind the git resolver; both degrade to empty fields rather than failing.
func mergeSessions(live []LiveSession, transcripts []transcriptMeta) []Session {
	sessions := make([]Session, 0, len(live)+len(transcripts))
	byID := make(map[string]int, len(live))

	for _, l := range live {
		id := l.SessionID
		if id != "" {
			if _, taken := byID[id]; taken && l.Entry.WrapperPID != 0 {
				id = ""
			}
		}
		sessions = append(sessions, Session{
			ID:       id,
			Live:     &l,
			Cwd:      l.Entry.Cwd,
			Modified: l.Modified,
		})
		if id != "" {
			byID[id] = len(sessions) - 1
		}
	}

	for _, tm := range newestTranscriptPerID(transcripts) {
		head := readTranscriptHead(tm.Path)
		if i, ok := byID[tm.ID]; ok {
			applyTranscript(&sessions[i], tm, head)
			continue
		}
		s := Session{ID: tm.ID}
		applyTranscript(&s, tm, head)
		sessions = append(sessions, s)
	}

	git := newGitContextResolver()
	for i := range sessions {
		applyGitContext(&sessions[i], git)
	}

	sortSessions(sessions)
	return sessions
}

// newestTranscriptPerID keeps one transcript per UUID. The same session can be
// filed under two project directories once its directory has been renamed, and
// the newer file is the one that was still being appended to.
func newestTranscriptPerID(transcripts []transcriptMeta) []transcriptMeta {
	byID := make(map[string]transcriptMeta, len(transcripts))
	for _, tm := range transcripts {
		if prev, ok := byID[tm.ID]; ok && !tm.ModTime.After(prev.ModTime) {
			continue
		}
		byID[tm.ID] = tm
	}

	out := make([]transcriptMeta, 0, len(byID))
	for _, tm := range byID {
		out = append(out, tm)
	}
	return out
}

// applyTranscript folds one transcript into a record, whether that record is a
// live session being decorated or a finished session being created.
func applyTranscript(s *Session, tm transcriptMeta, head transcriptHead) {
	s.TranscriptPath = tm.Path
	s.ProjectDir = tm.ProjectDir
	if head.Cwd != "" {
		// The transcript's first cwd beats the registry's. osc8wrap records the
		// directory it was launched in and claude records the directory the
		// session belongs to; only the latter is where --resume finds it.
		s.Cwd = head.Cwd
	}
	if s.Prompt == "" {
		s.Prompt = head.Prompt
	}
	if s.Live == nil {
		s.Branch = head.GitBranch
		s.Modified = tm.ModTime
		return
	}
	// A live session whose claude file carries no updatedAt still has a
	// transcript being written, so its mtime is the better answer than zero.
	if s.Modified.IsZero() {
		s.Modified = tm.ModTime
	}
}

func applyGitContext(s *Session, git *gitContextResolver) {
	ctx := git.Resolve(s.Cwd)
	// A live session may have switched branches since it started, so disk wins
	// over what the transcript recorded at the top of the file. A finished one
	// cannot have, and its directory is often a worktree that has since been
	// deleted, so it keeps the recorded branch and takes only what the disk can
	// still confirm.
	if s.Live != nil {
		s.Branch = ctx.Branch
	}
	s.Worktree = ctx.Worktree
	s.Root = ctx.Root
}

// sortSessions puts what needs the user now at the top: live before finished,
// and within live the sessions that are blocked on them before the ones still
// working. Recency breaks the rest, and ID then wrapper pid make the order
// total so that two runs a second apart do not reshuffle the list.
func sortSessions(sessions []Session) {
	sort.Slice(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		if la, lb := a.Live != nil, b.Live != nil; la != lb {
			return la
		}
		if a.Live != nil {
			if ra, rb := statusRank(a.Live.Status), statusRank(b.Live.Status); ra != rb {
				return ra < rb
			}
		}
		if !a.Modified.Equal(b.Modified) {
			return a.Modified.After(b.Modified)
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.wrapperPID() < b.wrapperPID()
	})
}

func listSessions() ([]Session, error) {
	return listSessionsMatching(searchQuery{})
}

// listSessionsMatching is listSessions restricted to the sessions matching q. An
// empty query lists everything, which is what listSessions is: the search is a
// filter over the same listing rather than a second listing that could disagree
// with it about what a row is.
//
// The filter runs AFTER mergeSessions, over whole Session records. It has to:
// branch, worktree and repository root do not exist before the merge and the git
// resolution, so a filter running earlier could only ever search conversations,
// which is what lost the ability to narrow by location when fzf's own matching
// was turned off.
//
// The order costs the head read of every transcript, including the rows about to
// be dropped, so it was measured on the author's corpus (476 transcripts,
// 930 MB, warm cache): the full plain listing is ~90 ms and a body-only grep
// ~82 ms, so the combined path is ~170 ms — still well inside one fzf reload per
// keystroke. grepSessions then gives most of it back on the queries that name a
// place, since those open no transcript bodies at all.
//
// Live sessions go through the same filter as finished ones: a session running
// right now that does not match is not what the user asked for, and live rows
// sort to the top, so keeping them would put the answers the user did not want
// above the ones they did. The live session with no UUID — the wrapped command
// is not claude, or claude has not published its session file yet — is no longer
// a special case: it has no transcript, but it does have a cwd, a repository and
// a branch, so it matches on those like any other row and is dropped only when a
// term needs a conversation it does not have.
func listSessionsMatching(q searchQuery) ([]Session, error) {
	entries, err := loadRegistry()
	if err != nil {
		return nil, err
	}
	claude, err := loadClaudeSessions()
	if err != nil {
		return nil, err
	}

	sessions := mergeSessions(joinSessions(entries, claude), scanTranscripts())
	return grepSessions(sessions, q), nil
}
