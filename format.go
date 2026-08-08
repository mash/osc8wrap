package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// dateLayout is a fixed-width, sortable timestamp: the display line is a column
// layout that fzf never re-measures, so a variable-width date would ragged it.
const dateLayout = "2006-01-02 15:04"

// fzfPreset is the default record: a human display column, then the id, then
// the cwd, tab separated. Field 2 being the id and field 3 the cwd is a
// contract shared with the author's ccsearch so that one shell function shape
// drives both tools; renumbering them breaks every rc file already written
// against either.
// The query excerpt goes at the END of field 1, not into a fourth field: field
// 2 is the id and field 3 the cwd for ccsearch's sake, and a new field would
// renumber nothing today and everything the moment someone adds another. Being
// inside the display column also means fzf searches the excerpt with
// --with-nth=1, which is what makes a second, narrowing query work at all. It
// is last because the columns before it are fixed-width and the excerpt is not.
const fzfPreset = `{{.Tag}}  {{.Date}}  {{.Repo}}{{if .Worktree}}/{{.Worktree}}{{end}}` +
	`{{if .Branch}} ({{.Branch}}){{end}}  {{.Summary}}{{if .WaitingFor}}  ({{.WaitingFor}}){{end}}` +
	`{{if .Match}}  » {{.Match}}{{end}}` +
	"\t{{.ID}}\t{{.Cwd}}\n"

// formatPresets maps a --format preset name to its template. "json" is a preset
// too but not a template: a JSON array needs real escaping, so it resolves to a
// second formatter behind the same resolver rather than to a template that
// would have to hand-quote its values.
var formatPresets = map[string]string{"fzf": fzfPreset}

// formatFields is the field list quoted back at a user whose --format did not
// resolve. Keep it in step with sessionView.
const formatFields = ".ID .Cwd .Tag .Live .Status .WaitingFor .Repo .Worktree .Branch .Root " +
	".Prompt .Summary .Name .Date .PID .Command .Match"

// sessionView is the flat, already-sanitized value a --format template sees.
//
// Flat is the point: handing the template a Session would put the *LiveSession
// in reach, and {{.Live.Status}} on a finished session fails halfway through a
// record — after the display column and before the id and cwd fields that make
// the record machine-readable. Every field here is a string or a bool, so no
// interpolation can fail on a nil.
type sessionView struct {
	// ID is the token `open` and `preview` accept: the claude UUID, or the
	// decimal wrapper pid for a live session that has none yet.
	ID string
	// Cwd is the directory the session started in, which is the only one
	// `claude --resume` works from.
	Cwd string
	// Tag is a fixed-width word rather than a glyph: fzf searches the display
	// line, so the user must be able to type `wait` as a filter.
	Tag string
	// Live is true while an osc8wrap wrapper is still hosting the session.
	Live       bool
	Status     string
	WaitingFor string
	// Repo and Worktree are basenames, not paths: the display line spends two
	// words on the location and renders them as "repo/worktree".
	Repo     string
	Worktree string
	Branch   string
	// Root is the absolute repository root, for templates that want the path.
	Root string
	// Prompt is the session's first real user turn; empty is normal.
	Prompt string
	// Summary is what identifies the session to a human: the prompt, else
	// claude's own name for it, else the wrapped argv — osc8wrap wraps
	// non-claude commands too and they must not render a blank column.
	Summary string
	Name    string
	Date    string
	// PID is the decimal wrapper pid, empty for a finished session.
	PID     string
	Command string
	// Match is the query excerpt, empty when nothing was searched for. It is
	// the one field whose text comes from the middle of a transcript rather than
	// from its head, so it is the likeliest to carry a tab or an escape — and it
	// goes through the same sanitizeField as everything else, once, here.
	Match string

	// modified is Session.Modified with an unrepresentable instant folded to
	// zero, and is the single source of both .Date and the json record's
	// "modified". Unexported so the template surface stays strings and bools.
	modified time.Time
}

// displayTime folds a timestamp osc8wrap cannot render into the zero value.
//
// Modified comes from claude's updatedAt or a file mtime, neither of which
// osc8wrap authors, and both have been seen wildly out of range. time.Time's
// MarshalJSON refuses a year outside [0,9999], which would fail the encode and
// take the WHOLE --format json listing to zero bytes over one bad file; the
// same year also formats wider than dateLayout's fixed 16 columns and ragged
// every row of the fzf preset after it. Claude's metadata is decoration: one
// unusable value costs its own date column and nothing else.
func displayTime(t time.Time) time.Time {
	if y := t.Year(); y < 0 || y > 9999 {
		return time.Time{}
	}
	return t
}

// newSessionView flattens and sanitizes one record. Everything a transcript,
// a claude session file or an argv contributed passes through sanitizeField
// here and nowhere else, so no later caller has to remember to do it.
func newSessionView(s Session) sessionView {
	repo, worktree := repoAndWorktree(s)

	v := sessionView{
		ID:       sanitizeField(sessionToken(s)),
		Cwd:      sanitizeField(s.Cwd),
		Tag:      sessionTag(s),
		Live:     s.Live != nil,
		Repo:     sanitizeField(repo),
		Worktree: sanitizeField(worktree),
		Branch:   sanitizeField(s.Branch),
		Root:     sanitizeField(s.Root),
		Prompt:   sanitizeField(s.Prompt),
		Summary:  sanitizeField(summaryOf(s)),
		Match:    sanitizeField(s.Match),
	}
	v.modified = displayTime(s.Modified)
	if !v.modified.IsZero() {
		v.Date = v.modified.Format(dateLayout)
	}
	if s.Live != nil {
		v.Status = sanitizeField(s.Live.Status)
		v.WaitingFor = sanitizeField(s.Live.WaitingFor)
		v.Name = sanitizeField(s.Live.Name)
		v.Command = sanitizeField(strings.Join(s.Live.Entry.Command, " "))
		if pid := s.wrapperPID(); pid != 0 {
			v.PID = strconv.Itoa(pid)
		}
	}
	return v
}

// sessionToken is how a record names itself to `open` and `preview`. A live
// session that has no UUID — the wrapped command is not claude, or claude has
// not published its session file yet — is addressed by its wrapper pid instead.
func sessionToken(s Session) string {
	if s.ID != "" {
		return s.ID
	}
	if pid := s.wrapperPID(); pid != 0 {
		return strconv.Itoa(pid)
	}
	return ""
}

// sessionTag is the leading word of the display line. Four columns wide in
// every case so the columns after it line up, and lower case for the finished
// majority so the live rows stand out among them.
func sessionTag(s Session) string {
	if s.Live == nil {
		return "done"
	}
	switch s.Live.Status {
	case "waiting":
		return "WAIT"
	case "busy":
		return "BUSY"
	case "idle":
		return "IDLE"
	default:
		// Live, but claude published no status — or nothing claude-shaped is
		// running here at all.
		return "LIVE"
	}
}

// summaryOf is the descriptive column, in the order of how much it says about
// the session: what the user asked, then claude's own label, then the argv.
func summaryOf(s Session) string {
	if s.Prompt != "" {
		return s.Prompt
	}
	if s.Live == nil {
		return ""
	}
	if s.Live.Name != "" {
		return s.Live.Name
	}
	return strings.Join(s.Live.Entry.Command, " ")
}

// repoAndWorktree reduces the git context to the two names the display line has
// room for. Root is the main repository and Worktree the checkout inside it,
// equal in a main checkout, so a linked worktree renders "repo/worktree" and a
// main checkout just "repo".
//
// Git resolution yields nothing at all for a directory that no longer exists,
// which is the common case for finished sessions — nine of the 35 distinct cwds
// measured on the author's machine were deleted worktrees. The cwd's own
// basename stands in there, since a row with no location at all is one the user
// cannot recognise.
func repoAndWorktree(s Session) (repo, worktree string) {
	if s.Root == "" {
		if s.Cwd == "" {
			return "", ""
		}
		return filepath.Base(s.Cwd), ""
	}
	if s.Worktree != "" && s.Worktree != s.Root {
		worktree = filepath.Base(s.Worktree)
	}
	return filepath.Base(s.Root), worktree
}

// sessionWriter is a resolved --format: the writer for the whole listing.
type sessionWriter func(w io.Writer, sessions []Session) error

// resolveFormat turns a --format value into a writer, and is deliberately
// separate from writing: a format that cannot be resolved must fail with stdout
// untouched rather than emit half a stream that fzf would then try to parse.
//
// A non-preset value is taken as the template itself, but one containing no
// action is almost always a mistyped preset, so it is rejected with the field
// list rather than printed literally once per session.
func resolveFormat(format string) (sessionWriter, error) {
	if format == "" {
		format = "fzf"
	}
	if format == "json" {
		return writeSessionsJSON, nil
	}

	src, ok := formatPresets[format]
	if !ok {
		if !strings.Contains(format, "{{") {
			return nil, fmt.Errorf(
				"unknown --format %q: use a preset (fzf, json) or a Go template over fields %s "+
					`(a template supplies its own record terminator, e.g. {{"\n"}})`,
				format, formatFields)
		}
		src = format
	}
	tmpl, err := template.New("sessions").Parse(src)
	if err != nil {
		return nil, fmt.Errorf("invalid --format template: %w", err)
	}
	return func(w io.Writer, sessions []Session) error {
		return writeSessionsTemplate(w, sessions, tmpl)
	}, nil
}

// writeSessionsTemplate renders one record per session. Each is rendered into a
// buffer and written only once it is whole: template.Execute writes
// incrementally, so rendering straight to w would leak a half-written record —
// a display column with no id and no cwd behind it — into the stream whenever a
// field failed. Records already written stay written, since they are correct
// and the user's picker is waiting on them.
func writeSessionsTemplate(w io.Writer, sessions []Session, tmpl *template.Template) error {
	bw := bufio.NewWriter(w)
	var buf bytes.Buffer
	for _, s := range sessions {
		v := newSessionView(s)
		buf.Reset()
		if err := tmpl.Execute(&buf, v); err != nil {
			if ferr := bw.Flush(); ferr != nil {
				return ferr
			}
			return fmt.Errorf("format session %s: %w", v.ID, err)
		}
		if _, err := bw.Write(buf.Bytes()); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// sessionJSON is one record of the json preset. The keys are snake_case and
// spelled out here so they do not follow Go field renames, and every string
// comes from the same sanitized view the templates see.
type sessionJSON struct {
	ID             string    `json:"id"`
	Live           bool      `json:"live"`
	Tag            string    `json:"tag"`
	Status         string    `json:"status"`
	WaitingFor     string    `json:"waiting_for"`
	Name           string    `json:"name"`
	Summary        string    `json:"summary"`
	Prompt         string    `json:"prompt"`
	Match          string    `json:"match"`
	Cwd            string    `json:"cwd"`
	Repo           string    `json:"repo"`
	Worktree       string    `json:"worktree"`
	Branch         string    `json:"branch"`
	Root           string    `json:"root"`
	Command        string    `json:"command"`
	WrapperPID     int       `json:"wrapper_pid"`
	ChildPID       int       `json:"child_pid"`
	TTY            string    `json:"tty"`
	SurfaceID      string    `json:"surface_id"`
	TranscriptPath string    `json:"transcript_path"`
	ProjectDir     string    `json:"project_dir"`
	Modified       time.Time `json:"modified"`
	// Navigable reports whether a Ghostty surface was recorded, i.e. whether
	// this session can be focused rather than only resumed.
	Navigable bool `json:"navigable"`
}

// writeSessionsJSON emits the array a program consumes. An empty listing is
// still "[]": a consumer that has to special-case null gets it wrong once.
func writeSessionsJSON(w io.Writer, sessions []Session) error {
	out := make([]sessionJSON, 0, len(sessions))
	for _, s := range sessions {
		v := newSessionView(s)
		rec := sessionJSON{
			ID:             v.ID,
			Live:           v.Live,
			Tag:            v.Tag,
			Status:         v.Status,
			WaitingFor:     v.WaitingFor,
			Name:           v.Name,
			Summary:        v.Summary,
			Prompt:         v.Prompt,
			Match:          v.Match,
			Cwd:            v.Cwd,
			Repo:           v.Repo,
			Worktree:       v.Worktree,
			Branch:         v.Branch,
			Root:           v.Root,
			Command:        v.Command,
			TranscriptPath: sanitizeField(s.TranscriptPath),
			ProjectDir:     sanitizeField(s.ProjectDir),
			Modified:       v.modified,
		}
		if s.Live != nil {
			rec.WrapperPID = s.Live.Entry.WrapperPID
			rec.ChildPID = s.Live.Entry.ChildPID
			rec.TTY = sanitizeField(s.Live.Entry.TTY)
			rec.SurfaceID = sanitizeField(s.Live.Entry.SurfaceID)
			rec.Navigable = s.Live.Entry.SurfaceID != ""
		}
		out = append(out, rec)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// sanitizeField makes a value osc8wrap did not author safe to interpolate into
// a record: tabs and newlines fold to spaces so they cannot forge the
// tab-delimited id and cwd columns or split one session across two lines.
//
// This is the picker's old sanitizeCell with a deliberate change of semantics,
// following ccsearch: a control character is dropped rather than replaced with
// "?", because these strings are now searched by fzf rather than laid out in
// fixed columns, and a run of question marks is noise a query has to match past.
func sanitizeField(s string) string {
	s = stripControl(s)
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// stripControl removes the control characters that would let transcript text
// drive the terminal it is printed to: C0 (below 0x20, keeping \n and \t for
// sanitizeField to fold and for the preview pane to keep), DEL, and the C1
// range 0x80-0x9f, whose 0x9d is a one-byte OSC introducer.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}
