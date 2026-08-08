package main

import (
	"bufio"
	"io"
	"os"
	"strings"
)

const previewUsage = `Usage: osc8wrap preview <id> [query...]

Renders one Claude Code session for a picker's preview pane: a header naming
the session, then its first turns if it has finished or its last ones if it is
still running. The id is field 2 of an "osc8wrap sessions" record.

Trailing words are the picker's current query (fzf's {q}). When they match
somewhere in the conversation the pane shows that region instead of the head
or the tail -- the matching turn, and above it the prompt that led to it -- so
the row explains why it was listed; when they match nothing it shows the usual
turns rather than an empty pane.

A session with no readable transcript prints the header and a note, and still
succeeds: a preview pane showing nothing is worse than one showing what little
is known. Only an id that resolves to no session at all is an error.

Colour is suppressed when NO_COLOR is set.
`

const (
	// previewMaxTurns is how many turns either end of a transcript contributes.
	// The pane is a few dozen lines tall; more than this is scrolled past
	// rather than read, and each one costs the rune budget below.
	previewMaxTurns = 8
	// previewRuneBudget caps the transcript text in one preview, and
	// previewTurnRunes caps any single turn so that one pasted file cannot
	// spend the whole budget before the turns around it are seen.
	previewRuneBudget = 4000
	previewTurnRunes  = 1200
	// previewHeadLines bounds the scan for the opening turns of a finished
	// session. Wider than the listing's headMaxLines because a preview wants
	// the first exchange rather than just the first prompt, and this runs for
	// one session rather than for all 588.
	previewHeadLines = 200
	// previewTailBytes is the window read back from the end of a live
	// transcript. Bounding by bytes rather than by lines is what keeps a 40 MB
	// transcript to one seek, which matters because a picker respawns this
	// process on every keystroke that moves the highlight.
	previewTailBytes = 64 << 10
)

// previewCandidates is the lookup runPreview resolves an id against. It is a
// variable so a test can pin which lookup that is: the whole reason `preview`
// is a separate subcommand is that fzf respawns it on every cursor movement, so
// reaching for the listing's own full scan here -- hundreds of transcripts per
// keystroke -- is the difference between a usable picker and an unusable one,
// and it is invisible in the output.
var previewCandidates = candidatesFor

// runPreviewCommand implements `osc8wrap preview`. It returns the process exit
// code.
func runPreviewCommand(args []string) int {
	return runPreview(args, os.Stdout, os.Stderr)
}

// runPreview is runPreviewCommand with its streams injected.
//
// It must never trigger the full transcript scan: previewCandidates turns the
// id into one glob and one bounded read, and the same pure resolveSession the
// listing uses then runs over that small set.
func runPreview(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printMessage(stderr, "osc8wrap preview: needs a session id\n")
		printMessage(stderr, "%s", previewUsage)
		return 1
	}
	id := args[0]
	if id == "--help" || id == "-h" {
		printMessage(stdout, "%s", previewUsage)
		return 0
	}
	// args[1:] is the picker's current query, arriving pre-split by the shell.
	// A query that parses to no terms is not an error: fzf passes {q} on every
	// keystroke, punctuation included, and the pane must keep rendering.
	var query searchQuery
	if len(args) > 1 {
		if q, ok := parseSearchQuery(strings.Join(args[1:], " ")); ok {
			query = q
		}
	}

	candidates, err := previewCandidates(id)
	if err != nil {
		printMessage(stderr, "osc8wrap preview %s: %v\n", sanitizeField(id), err)
		return 1
	}
	session, err := resolveSession(candidates, id)
	if err != nil {
		// The id is repeated here rather than trusted to the error text: this
		// one line replaces the pane the user was looking at, so it has to say
		// which row failed even if the wording of the error changes.
		printMessage(stderr, "osc8wrap preview %s: %v\n", sanitizeField(id), err)
		return 1
	}

	printMessage(stdout, "%s", renderPreviewMatching(session, newPreviewStyle(), query))
	return 0
}

// renderPreview is the pane with nothing searched for.
func renderPreview(s Session, style previewStyle) string {
	return renderPreviewMatching(s, style, searchQuery{})
}

// renderPreviewMatching builds the whole pane as one string. Being pure is the
// point: the pane is the product here, so the tests read it directly rather
// than through a process, and stdout is written exactly once.
//
// A matching query displaces the turns and nothing else. The header answers
// "which session is this", which the user still needs; only the body changes,
// because under a query the question the body answers is no longer "how did
// this start" but "why is this row in my list".
func renderPreviewMatching(s Session, style previewStyle, q searchQuery) string {
	// The view is where the header's fields get sanitized. Nothing else in
	// this file gets that for free: every string taken from a transcript below
	// goes through stripControl on its own.
	v := newSessionView(s)

	var b strings.Builder
	writePreviewHeader(&b, v, style)

	if turns, ok := matchingTurns(s, q); ok {
		writePreviewTurns(&b, s, turns, style)
		return b.String()
	}
	if s.Live == nil {
		// A finished session is previewed from the beginning: the question
		// being asked of it is "which session was this", and the answer is in
		// what the user asked for at the start.
		writePreviewTurns(&b, s, readPreviewHead(s.TranscriptPath, previewMaxTurns), style)
		return b.String()
	}

	// A live one is previewed from the end, because the question is "what is
	// it doing right now".
	writePreviewTurns(&b, s, lastTurns(readTranscriptTail(s.TranscriptPath, previewTailBytes)), style)
	return b.String()
}

// matchingTurns is the region of the conversation the query hit, as the turns
// the pane renders in place of the head or the tail. It reports false for an
// empty query, an unreadable transcript and a query that matches nothing — all
// three mean "show the usual turns", because a pane that goes blank while the
// user is still typing a query is worse than one that has not caught up.
//
// The prompt that led to the hit is rendered above it when there is one. The
// hit alone is usually a long assistant answer with the term buried somewhere
// in it, which shows what was said and not what was asked; the question is
// what identifies the exchange to the reader. It costs nothing extra — the
// same scan that found the excerpt carried the prompt out with it — and when
// the hit IS that user turn there is nothing to prepend, so the pane shows one
// turn rather than the same text twice.
//
// This is one bounded read of one transcript, the same budget the rest of this
// file keeps to: fzf respawns the process on every cursor movement, and it must
// never become a scan of the corpus that the listing behind it already did.
func matchingTurns(s Session, q searchQuery) ([]turn, bool) {
	if q.empty() {
		return nil, false
	}
	hit, ok := searchTranscriptWithPrompt(s.TranscriptPath, q)
	if !ok {
		return nil, false
	}
	if hit.Prompt != "" {
		return []turn{{Role: "user", Text: hit.Prompt}, {Role: hit.Role, Text: hit.Text}}, true
	}
	return []turn{{Role: hit.Role, Text: hit.Text}}, true
}

// writePreviewHeader names the session in three lines: what it is doing and
// where, when and from which directory, and the id itself. The id is last
// because it identifies the session to the tool rather than to the reader.
func writePreviewHeader(b *strings.Builder, v sessionView, style previewStyle) {
	location := v.Repo
	if v.Worktree != "" {
		location += "/" + v.Worktree
	}
	if v.Branch != "" {
		location += " (" + v.Branch + ")"
	}
	b.WriteString(style.strong(strings.TrimSpace(v.Tag + "  " + location)))
	b.WriteString("\n")

	if meta := strings.TrimSpace(v.Date + "  " + v.Cwd); meta != "" {
		b.WriteString(style.faint(meta))
		b.WriteString("\n")
	}
	if v.ID != "" {
		b.WriteString(style.faint(v.ID))
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

// writePreviewTurns renders the turns, or explains their absence. A session
// with no readable transcript is not an error: the pane keeps the header,
// which is still most of what the user needed.
func writePreviewTurns(b *strings.Builder, s Session, turns []turn, style previewStyle) {
	if len(turns) == 0 {
		b.WriteString(style.faint(noTranscriptNote(s)))
		b.WriteString("\n")
		return
	}

	budget := previewRuneBudget
	for i, t := range turns {
		if budget <= 0 {
			b.WriteString(style.faint("(preview truncated)"))
			b.WriteString("\n")
			return
		}
		if i > 0 {
			b.WriteString("\n")
		}
		// stripControl, not sanitizeField: a turn is displayed over several
		// lines here, so tabs and newlines are kept and only the characters
		// that would drive the terminal -- an OSC or CSI introducer among them
		// -- are dropped.
		text, used := clampRunes(stripControl(t.Text), min(budget, previewTurnRunes))
		budget -= used

		b.WriteString(style.strong(turnLabel(t.Role)))
		b.WriteString("\n")
		for _, line := range strings.Split(text, "\n") {
			b.WriteString("  ")
			b.WriteString(strings.TrimRight(line, " \t"))
			b.WriteString("\n")
		}
	}
}

func noTranscriptNote(s Session) string {
	if s.TranscriptPath == "" {
		return "(no transcript yet)"
	}
	return "(nothing readable in " + stripControl(s.TranscriptPath) + ")"
}

// turnLabel names the speaker the way the user thinks of them. An unfamiliar
// role is printed as claude wrote it, stripped like any other transcript text,
// because new roles appear faster than we can enumerate them.
func turnLabel(role string) string {
	switch role {
	case "user":
		return "you"
	case "assistant":
		return "claude"
	default:
		return stripControl(role)
	}
}

// clampRunes cuts text to limit runes, counting runes rather than bytes so a
// Japanese prompt is not truncated to a third of the intended length, and
// reports what it spent so the caller can hold a budget across turns.
func clampRunes(text string, limit int) (string, int) {
	count := 0
	for i := range text {
		if count == limit {
			return text[:i] + " …", limit
		}
		count++
	}
	return text, count
}

func lastTurns(turns []turn) []turn {
	if len(turns) > previewMaxTurns {
		return turns[len(turns)-previewMaxTurns:]
	}
	return turns
}

// readPreviewHead returns the opening turns of a transcript. It is bounded
// twice over -- by turns collected and by lines scanned -- so that a transcript
// whose first hundred records are all tool traffic costs a bounded read rather
// than the whole file.
//
// Like everything else in the transcript path it never fails: missing,
// truncated and unparseable files yield no turns, which the caller renders as
// a note.
func readPreviewHead(path string, maxTurns int) []turn {
	if path == "" || maxTurns <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close() //nolint:errcheck

	r := bufio.NewReaderSize(f, readBufferSize)
	var turns []turn
	for i := 0; i < previewHeadLines && len(turns) < maxTurns; i++ {
		line, eof := readBoundedLine(r, maxLineBytes)
		if t, ok := parseTurn(line); ok && t.Role != "" && t.Text != "" {
			// The synthetic user turns claude injects at the top of a session
			// -- caveat blocks, system reminders, command stubs -- are exactly
			// what a preview from the beginning must scroll past, since they
			// are identical across every session and identify none of them.
			if t.Role != "user" || isRealPrompt(t.Text) {
				turns = append(turns, t)
			}
		}
		if eof {
			break
		}
	}
	return turns
}

// previewStyle is the two attributes the pane uses. It is a value rather than
// a package-level flag so a test can render both ways in one process, and it
// collapses to plain text under NO_COLOR -- which is not politeness: the pane
// is filled with other people's text, and a user who asked for no escapes must
// get an output with no escape bytes in it at all.
type previewStyle struct {
	colour bool
}

func newPreviewStyle() previewStyle {
	// Any non-empty value means "no colour", per the NO_COLOR convention.
	return previewStyle{colour: os.Getenv("NO_COLOR") == ""}
}

func (p previewStyle) strong(s string) string { return p.wrap("\x1b[1m", s) }

func (p previewStyle) faint(s string) string { return p.wrap("\x1b[2m", s) }

func (p previewStyle) wrap(attr, s string) string {
	if !p.colour || s == "" {
		return s
	}
	return attr + s + "\x1b[0m"
}
