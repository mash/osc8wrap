package main

import (
	"bufio"
	"bytes"
	"os"
	"runtime"
	"sync"
	"unicode/utf8"
)

// searchExcerptRunes is how much of the matching turn the hit carries. The
// excerpt exists to answer "why did this row match", which a couple of lines
// answers; it is counted in runes rather than bytes so a Japanese match is not
// cut to a third of the intended width.
const searchExcerptRunes = 240

// searchPromptRunes bounds the prompt a hit carries out of the scan. It is
// previewTurnRunes because that is where the prompt is going: the pane clamps
// every turn to it anyway, so carrying more would only be cut again, and
// carrying it unbounded would mean a pasted 10 MB prompt travelling whole out
// of a scan whose whole design is to hold no line longer than it must.
const searchPromptRunes = previewTurnRunes

// searchQuery is a parsed query: whitespace-separated terms, ANDed.
//
// Terms are stored ASCII-lowercased because that is the fold containsFold
// implements. Lowercasing with unicode rules instead would be actively wrong:
// the haystack is never folded beyond ASCII, so a term the Unicode tables had
// altered -- a Turkish dotted I, a Greek final sigma -- would stop matching the
// bytes it was typed from.
type searchQuery struct {
	terms [][]byte
}

// parseSearchQuery parses a raw query. ok is false when it holds no terms.
func parseSearchQuery(s string) (searchQuery, bool) {
	var q searchQuery
	for _, field := range bytes.Fields([]byte(s)) {
		q.terms = append(q.terms, asciiLower(field))
	}
	return q, !q.empty()
}

// empty reports whether q would match everything.
func (q searchQuery) empty() bool { return len(q.terms) == 0 }

// asciiLower lowercases in place, ASCII only. Bytes >= 0x80 are left alone, so
// a UTF-8 term survives byte-for-byte.
func asciiLower(b []byte) []byte {
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return b
}

// searchHit is the evidence a transcript matched.
type searchHit struct {
	Role string // the role of the turn the excerpt came from
	Text string // excerpt around the first match; NOT sanitized (callers sanitize)
	// Prompt is what the user had asked when the hit was written: the text of
	// the last turn with Role "user" that isRealPrompt accepted, at or before
	// the turn the excerpt came from. It is empty when that turn IS the hit --
	// there is nothing to show above itself -- when nothing preceded it, and
	// for the plain searchTranscript, which does not follow it.
	//
	// It costs no I/O: searchTranscriptWithPrompt already walks the file turn by
	// turn, so this is a rolling variable over that same pass, not a second one.
	// Like Text it is raw and unsanitized (callers sanitize) but clamped to
	// searchPromptRunes, so nothing unbounded leaves the scan.
	Prompt string
}

// searchTranscript scans a whole transcript and reports a hit when EVERY term of
// q appears in some message text in that file. ok is false when it does not
// match, when q is empty, or when the file cannot be read.
//
// Nothing here returns an error, by design: this runs over hundreds of files
// that claude is writing concurrently, where a truncated tail, a file deleted
// between the directory scan and the open, and a record shape we do not know
// yet are all normal. The only useful reaction to any of them is "this
// transcript did not match", so that is the only outcome the API has.
//
// Three separate decisions make this affordable over ~900 MB:
//
//   - Matching is over message text (parseTurn -> turn.Text), never raw JSON.
//     Raw JSONL matching would hit tool_use payloads, whole file contents echoed
//     back by Read, JSON keys and absolute paths -- a search for "ghostty" would
//     match every session that ever grepped this repository.
//   - But parsing is the expensive half, so the raw line is the prefilter: a
//     line that does not even contain the bytes of something still wanted is
//     skipped without being decoded. The prefilter over-accepts (it is what
//     matches inside tool payloads) and parseTurn then rejects; it never
//     under-accepts, because turn.Text is a subset of the line's bytes modulo
//     JSON escaping.
//   - Terms are ANDed across the file rather than per line, and the scan stops
//     as soon as every term has been seen and an excerpt has been captured. The
//     common query -- two words that both occur early -- reads a few KB.
//
// Consequence of the bounded read: a line over maxLineBytes survives only as a
// prefix, which then fails to parse as JSON, so nothing inside a gigantic
// pasted line is findable -- not just the part past the cap. That is
// deliberate; the alternative is buffering a 40 MB line per worker, times
// NumCPU workers. The record after it is read normally.
func searchTranscript(path string, q searchQuery) (searchHit, bool) {
	return searchTranscriptScan(path, q, false)
}

// searchTranscriptWithPrompt is searchTranscript plus searchHit.Prompt: the
// question the hit was an answer to.
//
// It is a separate entry point rather than a field on every hit because the
// prompt is not free at corpus scale. Following it means decoding every user
// record, and most user records in a transcript are tool results rather than
// anything a person typed; measured over the author's 939 MB of transcripts
// that took `sessions prefilter` from 0.28 s to 0.71 s. The listing
// never shows the prompt, so it does not pay for it. The preview does, over
// exactly one file.
func searchTranscriptWithPrompt(path string, q searchQuery) (searchHit, bool) {
	return searchTranscriptScan(path, q, true)
}

func searchTranscriptScan(path string, q searchQuery, wantPrompt bool) (searchHit, bool) {
	// An empty query matching everything would be a trap for the caller: the
	// natural "did the user's filter match this session" call site would then
	// answer yes for a filter nobody typed. It reports no match instead, and
	// grepSessions handles "no filter" explicitly.
	if q.empty() {
		return searchHit{}, false
	}

	f, err := os.Open(path)
	if err != nil {
		return searchHit{}, false
	}
	defer f.Close() //nolint:errcheck

	// unseen[i] is "term i has not been found in this file yet"; remaining is
	// how many of those are left. The excerpt is anchored to the first term, so
	// the scan may have to continue past remaining == 0 -- a later turn can be
	// the one that satisfies the last term without containing the first.
	unseen := make([]bool, len(q.terms))
	for i := range unseen {
		unseen[i] = true
	}
	remaining := len(q.terms)
	var hit searchHit
	found := false
	// prompt is the last real user turn seen so far -- rolling over the pass
	// already being made, so that when a hit is captured the question that led
	// to it is already in hand and no second read of the file is needed. It
	// stops being maintained the moment the excerpt exists, which keeps the
	// tracking off the long tail of a file that goes on for another 40 MB.
	prompt := ""

	r := bufio.NewReaderSize(f, readBufferSize)
	for {
		line, eof := readBoundedLine(r, maxLineBytes)
		wanted := searchLineWanted(line, q, unseen, found)
		// The prompt has to see every user record, not only the lines the query
		// prefilter kept -- the turn that asked the question rarely contains the
		// term the answer matched on. promptLineWanted is the same shape of
		// cheap over-accepting byte test, so a line that is neither a candidate
		// nor a plausible user record is still never decoded.
		if wanted || (wantPrompt && !found && promptLineWanted(line)) {
			if t, ok := parseTurn(line); ok && t.Text != "" {
				// One conversion per candidate line, not per line: this is why
				// the prefilter above is worth its scan.
				text := []byte(t.Text)
				isPrompt := t.Role == "user" && isRealPrompt(t.Text)
				if wanted {
					for i, term := range q.terms {
						if unseen[i] && indexFold(text, term) >= 0 {
							unseen[i] = false
							remaining--
						}
					}
					if !found {
						if at := indexFold(text, q.terms[0]); at >= 0 {
							hit = searchHit{Role: t.Role, Text: searchExcerpt(t.Text, at)}
							// The hit is its own question: prepending the
							// rolling prompt here would either repeat this turn
							// or attribute it to an older one.
							if !isPrompt {
								hit.Prompt = prompt
							}
							found = true
						}
					}
				}
				if wantPrompt && !found && isPrompt {
					// Clamped on the way in, by the same rune-counting cut the
					// pane uses, so no arbitrarily large turn is held across the
					// rest of the scan.
					prompt, _ = clampRunes(t.Text, searchPromptRunes)
				}
			}
		}
		if remaining == 0 && found {
			return hit, true
		}
		if eof {
			return searchHit{}, false
		}
	}
}

// searchLineWanted is the prefilter: it reports whether the raw line could
// still contribute something, i.e. whether it carries the bytes of a term not
// yet seen or -- while the excerpt is still missing -- of the first term.
func searchLineWanted(line []byte, q searchQuery, unseen []bool, found bool) bool {
	if len(line) == 0 {
		return false
	}
	for i, term := range q.terms {
		if !unseen[i] && (found || i != 0) {
			continue
		}
		if indexFold(line, term) >= 0 {
			return true
		}
	}
	return false
}

// promptLineWanted is the prefilter for the rolling prompt: whether the raw
// line could be a user record at all.
//
// It looks for the quoted word rather than for `"role":"user"` on purpose. Key
// order and spacing are the writer's choice, and under-accepting here is
// invisible -- the pane would silently lose the prompt for some transcripts
// and no test would fail -- while over-accepting costs only a parseTurn on a
// line that then turns out not to be a user turn.
var promptMarker = []byte(`"user"`)

func promptLineWanted(line []byte) bool { return bytes.Contains(line, promptMarker) }

// containsFold reports whether hay contains needle, ASCII-case-insensitively.
// needle must already be lowercased.
func containsFold(hay, needle []byte) bool { return indexFold(hay, needle) >= 0 }

// indexFold is containsFold with the position, which the excerpt needs.
//
// It allocates nothing and never copies the haystack: bytes.ToLower(hay) per
// line would be an extra ~900 MB of garbage over a full search. The fold is
// ASCII-only, so bytes >= 0x80 compare exactly and a UTF-8 (Japanese) needle
// works as a plain byte search -- which is the common case here.
func indexFold(hay, needle []byte) int {
	n := len(needle)
	if n == 0 {
		return 0
	}
	if n > len(hay) {
		return -1
	}
	last := len(hay) - n

	lo := needle[0]
	up := lo
	if 'a' <= lo && lo <= 'z' {
		up = lo - ('a' - 'A')
	}

	// loAt and upAt are the next positions of the two spellings of the first
	// byte, carried across iterations. Re-running both searches from each
	// candidate instead would be quadratic on the realistic input -- a line
	// dense in one spelling and holding the other only at its far end.
	loAt := indexByteFrom(hay, lo, 0)
	upAt := loAt
	if up != lo {
		upAt = indexByteFrom(hay, up, 0)
	}
	for {
		i := loAt
		if i < 0 || (upAt >= 0 && upAt < i) {
			i = upAt
		}
		if i < 0 || i > last {
			return -1
		}
		if equalFold(hay[i:i+n], needle) {
			return i
		}
		if loAt == i {
			loAt = indexByteFrom(hay, lo, i+1)
		}
		if upAt == i {
			upAt = indexByteFrom(hay, up, i+1)
		}
	}
}

func indexByteFrom(b []byte, c byte, from int) int {
	if from >= len(b) {
		return -1
	}
	i := bytes.IndexByte(b[from:], c)
	if i < 0 {
		return -1
	}
	return from + i
}

// equalFold compares equal-length slices with needle already lowercased.
func equalFold(hay, needle []byte) bool {
	for i, c := range hay {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != needle[i] {
			return false
		}
	}
	return true
}

// searchExcerpt cuts a window of at most searchExcerptRunes runes out of text,
// centred on the match at byte offset at, marking each cut end with an ellipsis.
//
// It walks by rune, never by byte, for two reasons: the budget is meaningless
// in bytes for Japanese text, and slicing at an arbitrary byte would hand the
// caller a broken UTF-8 fragment. The anchor is first pulled back to a rune
// start, because a byte-level match of a multi-byte needle can land inside a
// rune of the haystack.
//
// Sanitizing is the caller's job; this only chooses the window.
func searchExcerpt(text string, at int) string {
	if at < 0 || at > len(text) {
		at = 0
	}
	for at > 0 && !utf8.RuneStart(text[at]) {
		at--
	}

	// Half the budget of lead-in, so the match sits in the middle of what is
	// shown rather than at its left edge.
	start := at
	for i := 0; i < searchExcerptRunes/2 && start > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(text[:start])
		start -= size
	}

	end := start
	for count := 0; count < searchExcerptRunes && end < len(text); count++ {
		_, size := utf8.DecodeRuneInString(text[end:])
		end += size
	}

	out := text[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(text) {
		out += "…"
	}
	return out
}

// residual returns the terms of q that hay does NOT satisfy, i.e. what is still
// left to look for once the cheap haystack has been consulted. An empty result
// means hay alone matched the whole query.
//
// It allocates a fresh slice instead of compacting into q.terms[:0], which is
// the obvious and wrong way to write this: q is copied by value but its terms
// slice is not, so compacting in place would rewrite the caller's query -- and
// grepSessions calls this once per session from a loop, so the second session
// would be filtered by whatever the first one had left behind. Nothing here
// touches the term bytes themselves; those are shared, read-only, and already
// lowercased by parseSearchQuery.
func (q searchQuery) residual(hay string) searchQuery {
	if q.empty() {
		return searchQuery{}
	}
	// One conversion for the whole query rather than one per term. It does not
	// escape, and containsFold never writes to it.
	h := []byte(hay)

	var out searchQuery
	for _, term := range q.terms {
		if !containsFold(h, term) {
			out.terms = append(out.terms, term)
		}
	}
	return out
}

// sessionHaystack is the text a term may match a session on without opening its
// transcript: where the session lives.
//
// It is exactly the location -- cwd, repository root, worktree, branch, plus the
// two basenames the display column renders -- and deliberately not the tag or
// the status. Folding "done", "wait", "busy" or "live" in would make `sessions done`
// list every finished session on the machine, which is a filter that hides
// nothing while looking like it filtered; not having it is better. The prompt
// and the session name are left out too: they are the head of the conversation,
// which the transcript scan already covers, and including them here would make
// a body term match twice with different excerpt semantics.
//
// The fields are joined by a newline so that a term cannot straddle two of them
// -- "osc8wrapmain" must not match a cwd of .../osc8wrap followed by branch main.
func sessionHaystack(s Session) string {
	repo, worktree := repoAndWorktree(s)
	var b bytes.Buffer
	for _, field := range [...]string{s.Cwd, s.Root, s.Worktree, s.Branch, repo, worktree} {
		if field == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(field)
	}
	return b.String()
}

// grepSessions returns the sessions matching q, each annotated with the excerpt
// that explains it, in the input order.
//
// A session matches when EVERY term is satisfied, but a term may be satisfied by
// either half of the session: its location (sessionHaystack) or its conversation
// (searchTranscript). Different terms may come from different halves, which is
// the whole point -- `sessions osc8wrap ratchet` is "the session in that repo
// where we discussed ratchets", and neither half answers it alone.
//
// The metadata is consulted first, and only the residual -- the terms the
// location did not already satisfy -- is looked for in the transcript. That is
// not a micro-optimization: a session whose residual is empty is never opened at
// all, so `sessions nature-server` reads zero transcript bodies, and a term naming
// a repository shrinks the corpus BEFORE the expensive scan instead of after it.
// It also makes Match honest, since the excerpt is anchored on the first term
// that actually came from the body.
//
// A session with no transcript -- a live wrapper claude has not published a
// session file for yet -- can therefore still match, on its location alone; it
// is only dropped when it has residual terms and nothing to search for them in.
//
// Concurrency is unchanged from the transcript-level filter it replaces: the
// work is I/O plus a memchr over hundreds of megabytes, bounded by NumCPU
// because past that the disk, not the CPU, is the limit. Determinism is not
// negotiable -- this feeds a picker, and a list that reshuffles between
// keystrokes is unusable -- so workers write into a slot indexed by the
// session's position and never append, and the surviving rows are assembled in
// input order after the join.
func grepSessions(sessions []Session, q searchQuery) []Session {
	// No filter is not "match nothing": the caller asked for an unfiltered
	// listing and gets its input back untouched, with no excerpts on it.
	if q.empty() {
		return sessions
	}
	if len(sessions) == 0 {
		return nil
	}

	matched := make([]bool, len(sessions))
	hits := make([]searchHit, len(sessions))
	// residuals[i] is only read by the worker that draws i, and only for the
	// indices in scan.
	residuals := make([]searchQuery, len(sessions))
	scan := make([]int, 0, len(sessions))

	for i := range sessions {
		r := q.residual(sessionHaystack(sessions[i]))
		if r.empty() {
			matched[i] = true
			continue
		}
		if sessions[i].TranscriptPath == "" {
			continue
		}
		residuals[i] = r
		scan = append(scan, i)
	}

	if len(scan) > 0 {
		workers := runtime.NumCPU()
		if workers > len(scan) {
			workers = len(scan)
		}
		// Unbuffered: the queue is the loop below, so a worker that draws a
		// 200 MB transcript simply stops taking new ones.
		queue := make(chan int)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range queue {
					if hit, ok := searchTranscript(sessions[i].TranscriptPath, residuals[i]); ok {
						hits[i] = hit
						matched[i] = true
					}
				}
			}()
		}
		for _, i := range scan {
			queue <- i
		}
		close(queue)
		wg.Wait()
	}

	kept := make([]Session, 0, len(sessions))
	for i, s := range sessions {
		if !matched[i] {
			continue
		}
		// A row that matched on its location alone has no body hit, so it keeps
		// an empty Match rather than a fabricated one: the excerpt answers "where
		// in the conversation", and for that row the answer is nowhere.
		s.Match = hits[i].Text
		kept = append(kept, s)
	}
	return kept
}
