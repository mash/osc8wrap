package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// headMaxLines is the knee of the measured curve: over the 588 transcripts on
	// the author's machine a 20/50/200 line window misses the first prompt in
	// 51/47/42 of them, so reading past 50 buys almost nothing.
	headMaxLines = 50
	// maxLineBytes caps what we keep of any one line. Pasted attachments produce
	// single lines of tens of megabytes; the remainder is discarded rather than
	// buffered, and the truncated prefix simply fails to parse.
	maxLineBytes = 1 << 20
	// readBufferSize is the bufio window, sized so a typical line needs one refill.
	readBufferSize = 64 << 10
	// tailBranchWindowSize bounds the read used to recover a finished session's
	// final branch. It must be big enough that at least one whole record
	// boundary falls inside the window even when the preceding record is a
	// large tool_result; 64<<10 is a reasonable default for that.
	tailBranchWindowSize = 64 << 10
)

// claudeDir is the root of claude's per-user state, governing both sessions/
// and projects/ so that CLAUDE_CONFIG_DIR moves them together.
func claudeDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	// An unresolvable home yields a relative path that will simply not exist,
	// which every caller here treats as "nothing to list".
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func claudeProjectsDir() string {
	return filepath.Join(claudeDir(), "projects")
}

// transcriptMeta is everything a transcript yields without opening it: the
// session UUID from the filename, the project directory from its parent, and
// recency from the directory entry.
type transcriptMeta struct {
	ID         string
	Path       string
	ProjectDir string
	ModTime    time.Time
}

// scanTranscripts lists every transcript under claude's projects directory with
// one ReadDir per project directory and no file reads at all: mtime comes from
// the directory entry, which is what keeps a full scan of hundreds of
// transcripts inside the cost of a single fzf redraw.
//
// ~/.claude/projects/*/sessions-index.json looks like it would make this
// cheaper. It was measured and rejected: it exists in only 20 of 63 project
// directories, its newest entry was 5.6 months old, and it listed none of the
// currently live sessions. Do not "optimize" into it.
func scanTranscripts() []transcriptMeta {
	root := claudeProjectsDir()
	projects, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	var metas []transcriptMeta
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		dir := filepath.Join(root, project.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			// Project directories really do contain uuid-named subdirectories.
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			// The .jsonl requirement is also what excludes sessions-index.json.
			if !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			id := strings.TrimSuffix(name, ".jsonl")
			if id == "" {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			metas = append(metas, transcriptMeta{
				ID:         id,
				Path:       filepath.Join(dir, name),
				ProjectDir: dir,
				ModTime:    info.ModTime(),
			})
		}
	}
	return metas
}

// turn is one transcript record reduced to the fields the listing and the
// preview pane need. Nothing here is sanitized; that is the formatter's job.
type turn struct {
	Type      string
	Role      string
	Text      string
	Cwd       string
	GitBranch string
}

// parseTurn decodes one transcript line. It reports false only when the line is
// not a JSON object — an unfamiliar record type is a valid turn with empty
// fields, because claude adds record types faster than we can enumerate them.
func parseTurn(line []byte) (turn, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return turn{}, false
	}

	var rec struct {
		Type      string          `json:"type"`
		Cwd       string          `json:"cwd"`
		GitBranch string          `json:"gitBranch"`
		Message   json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		return turn{}, false
	}

	t := turn{Type: rec.Type, Cwd: rec.Cwd, GitBranch: rec.GitBranch}

	// message is decoded separately so that a record whose message is not an
	// object still contributes its cwd and branch.
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(rec.Message, &msg) == nil {
		t.Role = msg.Role
		t.Text = contentText(msg.Content)
	}
	return t, true
}

// contentText flattens a message content field, which is either a plain string
// or an array of parts. Parts carrying no text — tool_use, tool_result,
// thinking, images — contribute nothing rather than aborting the extraction.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}

	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}

	var b strings.Builder
	for _, p := range parts {
		var text string
		if err := json.Unmarshal(p, &text); err != nil {
			var part struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(p, &part); err != nil || part.Type != "text" {
				continue
			}
			text = part.Text
		}
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	return strings.TrimSpace(b.String())
}

// transcriptHead is what a bounded read of the start of a transcript recovers.
// Every field is optional: roughly 47 of 588 real transcripts yield no prompt.
type transcriptHead struct {
	Cwd       string
	Prompt    string
	GitBranch string
}

// readTranscriptHead reads at most headMaxLines logical lines and returns what
// it found. Missing, truncated and unparseable transcripts yield empty fields.
func readTranscriptHead(path string) transcriptHead {
	var head transcriptHead

	f, err := os.Open(path)
	if err != nil {
		return head
	}
	defer f.Close() //nolint:errcheck

	r := bufio.NewReaderSize(f, readBufferSize)
	for i := 0; i < headMaxLines; i++ {
		line, eof := readBoundedLine(r, maxLineBytes)
		if t, ok := parseTurn(line); ok {
			// The FIRST cwd, never a later one. Claude files the transcript under
			// the project folder derived from the cwd the session started in, so
			// `claude --resume` only finds it when run from there; an in-session
			// `cd` appears on later lines and would resume nothing.
			if head.Cwd == "" {
				head.Cwd = t.Cwd
			}
			if head.GitBranch == "" {
				head.GitBranch = t.GitBranch
			}
			if head.Prompt == "" && t.Type == "user" && isRealPrompt(t.Text) {
				head.Prompt = t.Text
			}
			if head.Cwd != "" && head.GitBranch != "" && head.Prompt != "" {
				break
			}
		}
		if eof {
			break
		}
	}
	return head
}

// isRealPrompt rejects the synthetic user turns claude injects — system
// reminders, command stubs, caveat blocks — which all open with a tag.
func isRealPrompt(text string) bool {
	return text != "" && !strings.HasPrefix(text, "<")
}

// seekTranscriptTail positions f at the start of its last maxBytes and returns
// a reader over the rest of the file, or ok=false if that setup failed or the
// window held nothing but a partial record. Shared by readTranscriptTail and
// readTranscriptTailBranch, which differ only in what they do with the turns.
func seekTranscriptTail(f *os.File, maxBytes int64) (*bufio.Reader, bool) {
	info, err := f.Stat()
	if err != nil {
		return nil, false
	}
	offset := info.Size() - maxBytes
	if offset < 0 {
		offset = 0
	}

	// The byte before the window says whether the seek landed inside a line or
	// exactly on a record boundary. Discarding the "partial" first line in the
	// latter case would eat a whole record.
	partial := false
	if offset > 0 {
		var prev [1]byte
		if _, err := f.ReadAt(prev[:], offset-1); err != nil {
			return nil, false
		}
		partial = prev[0] != '\n'
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, false
	}

	r := bufio.NewReaderSize(f, readBufferSize)
	if partial {
		// The seek landed mid-line; that fragment is not a record on its own.
		if _, eof := readBoundedLine(r, 0); eof {
			return nil, false
		}
	}
	return r, true
}

// readTranscriptTail returns the turns in the last maxBytes of a transcript.
// Cost is bounded by maxBytes, not by the file, which matters because the
// preview pane runs once per highlighted row over files up to hundreds of MB.
func readTranscriptTail(path string, maxBytes int64) []turn {
	if maxBytes <= 0 {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close() //nolint:errcheck

	r, ok := seekTranscriptTail(f, maxBytes)
	if !ok {
		return nil
	}

	var turns []turn
	for {
		line, eof := readBoundedLine(r, maxLineBytes)
		if t, ok := parseTurn(line); ok && t.Role != "" && t.Text != "" {
			turns = append(turns, t)
		}
		if eof {
			return turns
		}
	}
}

// readTranscriptTailBranch returns the LAST non-empty gitBranch in the final
// tailBranchWindowSize bytes of a transcript, or "" if none was found or the
// file could not be read. This is what makes a finished session's branch
// track a mid-session `git checkout` instead of freezing on the first one.
//
// Unlike readTranscriptTail, records are not filtered on Role/Text: a record
// can carry gitBranch with no message text at all, and skipping those would
// silently fall back to a stale branch.
func readTranscriptTailBranch(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close() //nolint:errcheck

	r, ok := seekTranscriptTail(f, tailBranchWindowSize)
	if !ok {
		return ""
	}

	var branch string
	for {
		line, eof := readBoundedLine(r, maxLineBytes)
		if t, ok := parseTurn(line); ok && t.GitBranch != "" {
			branch = t.GitBranch
		}
		if eof {
			return branch
		}
	}
}

// readBoundedLine consumes one logical line, keeping at most maxBytes of it and
// discarding the remainder, and reports whether the input is exhausted. A line
// longer than maxBytes therefore costs a scan but not memory, and the kept
// prefix fails to parse instead of aborting the surrounding read.
func readBoundedLine(r *bufio.Reader, maxBytes int) ([]byte, bool) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if room := maxBytes - len(line); room > 0 {
			if room > len(chunk) {
				room = len(chunk)
			}
			line = append(line, chunk[:room]...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, err != nil
	}
}
