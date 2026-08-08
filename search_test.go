package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// textLine is a transcript record whose whole content is one text part, which
// is the shape every real prompt and reply has.
func textLine(role, text string) string {
	b, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"type":%q,"message":{"role":%q,"content":[{"type":"text","text":%s}]}}`, role, role, b)
}

// toolUseLine is a record whose only occurrence of a term is inside a tool_use
// payload -- the false positive the prefilter+parse split exists to reject.
func toolUseLine(command string) string {
	b, err := json.Marshal(command)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":%s}}]}}`, b)
}

// writeLines writes a transcript fixture and returns its path.
func writeLines(t *testing.T, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustQuery(t *testing.T, s string) searchQuery {
	t.Helper()
	q, ok := parseSearchQuery(s)
	if !ok {
		t.Fatalf("parseSearchQuery(%q) reported no terms", s)
	}
	return q
}

func TestParseSearchQuery(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  []string
		wantO bool
	}{
		{name: "empty", in: "", want: nil, wantO: false},
		{name: "blank", in: "   \t\n ", want: nil, wantO: false},
		{name: "one term", in: "osc8", want: []string{"osc8"}, wantO: true},
		{name: "folded to lower", in: "OSC8 Ghostty", want: []string{"osc8", "ghostty"}, wantO: true},
		{name: "collapses whitespace", in: "  a\t\tb  ", want: []string{"a", "b"}, wantO: true},
		// Non-ASCII must survive byte-for-byte: the haystack is never folded
		// beyond ASCII, so a unicode-lowered term would stop matching.
		{name: "japanese", in: "検索 ﾃｽﾄ", want: []string{"検索", "ﾃｽﾄ"}, wantO: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, ok := parseSearchQuery(tt.in)
			if ok != tt.wantO {
				t.Errorf("ok = %v, want %v", ok, tt.wantO)
			}
			if q.empty() != (len(tt.want) == 0) {
				t.Errorf("empty() = %v, want %v", q.empty(), len(tt.want) == 0)
			}
			var got []string
			for _, term := range q.terms {
				got = append(got, string(term))
			}
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("terms = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContainsFold(t *testing.T) {
	tests := []struct {
		name   string
		hay    string
		needle string
		want   bool
	}{
		{name: "plain", hay: "hello world", needle: "world", want: true},
		{name: "absent", hay: "hello world", needle: "worlds", want: false},
		{name: "upper haystack", hay: "HELLO GHOSTTY", needle: "ghostty", want: true},
		{name: "mixed haystack", hay: "GhOsTtY term", needle: "ghostty", want: true},
		{name: "at start", hay: "Ghostty", needle: "ghostty", want: true},
		{name: "at end", hay: "run GHOSTTY", needle: "ghostty", want: true},
		{name: "empty needle", hay: "x", needle: "", want: true},
		{name: "needle longer", hay: "ab", needle: "abc", want: false},
		{name: "empty haystack", hay: "", needle: "a", want: false},
		{name: "japanese identity fold", hay: "これは検索のテスト", needle: "検索", want: true},
		{name: "japanese absent", hay: "これはテスト", needle: "検索", want: false},
		// Near-miss prefixes force the restart path in the two-spelling scan.
		{name: "restart after prefix", hay: "aaAaAab", needle: "aab", want: true},
		{name: "restart no match", hay: "aAaAaA", needle: "aab", want: false},
		{name: "non letter first byte", hay: "x-Y", needle: "-y", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := containsFold([]byte(tt.hay), []byte(tt.needle)); got != tt.want {
				t.Errorf("containsFold(%q, %q) = %v, want %v", tt.hay, tt.needle, got, tt.want)
			}
		})
	}
}

func TestSearchTranscript(t *testing.T) {
	// One line longer than maxLineBytes, whose term sits past the cap.
	longLine := textLine("user", strings.Repeat("x", maxLineBytes+4096)+" ghostty")

	tests := []struct {
		name     string
		lines    []string
		query    string
		wantOK   bool
		wantRole string
		wantText string // substring the excerpt must contain
	}{
		{
			name:     "single term",
			lines:    []string{textLine("user", "please fix the osc8 linker")},
			query:    "osc8",
			wantOK:   true,
			wantRole: "user",
			wantText: "osc8 linker",
		},
		{
			name: "terms anded across different turns",
			lines: []string{
				textLine("user", "how does osc8 hyperlinking work"),
				textLine("assistant", "ghostty renders it natively"),
			},
			query:    "osc8 ghostty",
			wantOK:   true,
			wantRole: "user",
			wantText: "osc8 hyperlinking",
		},
		{
			name: "second term missing",
			lines: []string{
				textLine("user", "how does osc8 hyperlinking work"),
				textLine("assistant", "iterm renders it natively"),
			},
			query:  "osc8 ghostty",
			wantOK: false,
		},
		{
			// The whole point of the prefilter+parse split: the raw line
			// contains "kubectl", the message text does not.
			name: "term only in tool_use payload",
			lines: []string{
				textLine("user", "deploy the thing"),
				toolUseLine("kubectl apply -f deploy.yaml"),
			},
			query:  "kubectl",
			wantOK: false,
		},
		{
			name: "tool_use payload term with a real term",
			lines: []string{
				textLine("user", "deploy the thing"),
				toolUseLine("kubectl apply -f deploy.yaml"),
			},
			query:  "deploy kubectl",
			wantOK: false,
		},
		{
			name:     "query lowercase, text uppercase",
			lines:    []string{textLine("user", "GHOSTTY is the terminal")},
			query:    "ghostty",
			wantOK:   true,
			wantRole: "user",
			wantText: "GHOSTTY",
		},
		{
			name:     "query uppercase, text lowercase",
			lines:    []string{textLine("assistant", "ghostty is the terminal")},
			query:    "GHOSTTY",
			wantOK:   true,
			wantRole: "assistant",
			wantText: "ghostty",
		},
		{
			name:     "japanese term",
			lines:    []string{textLine("user", "この検索機能をGoで実装してほしい")},
			query:    "検索機能",
			wantOK:   true,
			wantRole: "user",
			wantText: "検索機能",
		},
		{
			name: "japanese and ascii anded",
			lines: []string{
				textLine("user", "この検索機能を実装して"),
				textLine("assistant", "Ghostty で確認しました"),
			},
			query:    "検索 ghostty",
			wantOK:   true,
			wantRole: "user",
			wantText: "検索機能",
		},
		{
			// Bounded read: a line over maxLineBytes is kept only as a prefix,
			// which no longer parses as JSON, so nothing in it is found --
			// neither the term past the cap nor anything before it.
			name:   "match inside an over-long line is not found",
			lines:  []string{longLine},
			query:  "ghostty",
			wantOK: false,
		},
		{
			name:     "a long line under the cap still matches",
			lines:    []string{textLine("user", "ghostty "+strings.Repeat("x", maxLineBytes/2))},
			query:    "ghostty",
			wantOK:   true,
			wantRole: "user",
			wantText: "ghostty",
		},
		{
			// An over-long line must not poison the lines after it.
			name:     "scan survives an over-long line",
			lines:    []string{longLine, textLine("user", "ghostty afterwards")},
			query:    "ghostty",
			wantOK:   true,
			wantRole: "user",
			wantText: "ghostty afterwards",
		},
		{
			name: "corrupt lines are skipped, not fatal",
			lines: []string{
				`{"type":"user","message":{"role":"user","content":[{"type":"text","tex`,
				"not json at all, contains ghostty",
				"",
				textLine("user", "ghostty for real"),
			},
			query:    "ghostty",
			wantOK:   true,
			wantRole: "user",
			wantText: "ghostty for real",
		},
		{
			name: "truncated file with no complete match",
			lines: []string{
				`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"ghostty`,
			},
			query:  "ghostty",
			wantOK: false,
		},
		{
			name:   "empty file",
			lines:  nil,
			query:  "ghostty",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeLines(t, "session.jsonl", tt.lines...)
			hit, ok := searchTranscript(path, mustQuery(t, tt.query))
			if ok != tt.wantOK {
				t.Fatalf("searchTranscript ok = %v, want %v (hit %+v)", ok, tt.wantOK, hit)
			}
			if !ok {
				if hit != (searchHit{}) {
					t.Errorf("hit = %+v, want zero on no match", hit)
				}
				return
			}
			if hit.Role != tt.wantRole {
				t.Errorf("hit.Role = %q, want %q", hit.Role, tt.wantRole)
			}
			if !strings.Contains(hit.Text, tt.wantText) {
				t.Errorf("hit.Text = %q, want it to contain %q", hit.Text, tt.wantText)
			}
			if !utf8.ValidString(hit.Text) {
				t.Errorf("hit.Text is not valid UTF-8: %q", hit.Text)
			}
		})
	}
}

// The hit carries the question that led to it, picked up by the same pass that
// found the excerpt. What the preview does with it is preview_test's business;
// what is pinned here is which turn is chosen and that it leaves the scan
// bounded.
func TestSearchTranscriptCarriesThePrompt(t *testing.T) {
	huge := strings.Repeat("あ", searchPromptRunes*2)

	tests := []struct {
		name       string
		lines      []string
		query      string
		wantPrompt string
		wantRunes  int // when non-zero, the prompt is checked by length instead
	}{
		{
			name: "the last real prompt before the hit",
			lines: []string{
				textLine("user", "the first question"),
				textLine("assistant", "an answer with nothing in it"),
				textLine("user", "the second question"),
				textLine("assistant", "the ratchet is wired in linker.go"),
			},
			query:      "ratchet",
			wantPrompt: "the second question",
		},
		{
			// Nothing above the hit but turns claude wrote to itself.
			name: "synthetic user turns are not prompts",
			lines: []string{
				textLine("user", "the only real question"),
				textLine("user", "<system-reminder>the file changed</system-reminder>"),
				textLine("user", "<command-name>/compact</command-name>"),
				textLine("assistant", "the ratchet is wired in linker.go"),
			},
			query:      "ratchet",
			wantPrompt: "the only real question",
		},
		{
			// A prompt that IS the hit would be shown above itself.
			name:       "a hit on the prompt carries none",
			lines:      []string{textLine("user", "how do I wire the ratchet")},
			query:      "ratchet",
			wantPrompt: "",
		},
		{
			// A prompt AFTER the hit answers nothing about it.
			name: "a later prompt is not adopted",
			lines: []string{
				textLine("assistant", "the ratchet is wired in linker.go"),
				textLine("user", "asked afterwards"),
			},
			query:      "ratchet",
			wantPrompt: "",
		},
		{
			name: "a pasted prompt is clamped",
			lines: []string{
				textLine("user", huge),
				textLine("assistant", "the ratchet is wired in linker.go"),
			},
			query:     "ratchet",
			wantRunes: searchPromptRunes,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeLines(t, "session.jsonl", tt.lines...)
			hit, ok := searchTranscriptWithPrompt(path, mustQuery(t, tt.query))
			if !ok {
				t.Fatalf("searchTranscriptWithPrompt found no match in %q", tt.query)
			}
			// The listing's entry point pays nothing for a field it never
			// renders, so the same file must yield no prompt through it.
			if plain, ok := searchTranscript(path, mustQuery(t, tt.query)); !ok || plain.Prompt != "" {
				t.Errorf("searchTranscript = (%+v, %v), want a hit carrying no prompt", plain, ok)
			}
			if tt.wantRunes > 0 {
				if n := strings.Count(hit.Prompt, "あ"); n != tt.wantRunes {
					t.Errorf("hit.Prompt kept %d runes, want %d", n, tt.wantRunes)
				}
				if !utf8.ValidString(hit.Prompt) {
					t.Errorf("hit.Prompt is not valid UTF-8: %q", hit.Prompt)
				}
				return
			}
			if hit.Prompt != tt.wantPrompt {
				t.Errorf("hit.Prompt = %q, want %q", hit.Prompt, tt.wantPrompt)
			}
		})
	}
}

func TestSearchTranscriptMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.jsonl")
	if hit, ok := searchTranscript(missing, mustQuery(t, "ghostty")); ok {
		t.Errorf("searchTranscript(missing) = %+v, true; want no match", hit)
	}
	// A directory where a transcript was expected reads no better than a
	// missing one, and must be just as quiet.
	if hit, ok := searchTranscript(t.TempDir(), mustQuery(t, "ghostty")); ok {
		t.Errorf("searchTranscript(dir) = %+v, true; want no match", hit)
	}
}

// An empty query must never look like "everything matched".
func TestSearchTranscriptEmptyQuery(t *testing.T) {
	path := writeLines(t, "session.jsonl", textLine("user", "anything at all"))
	var empty searchQuery
	if !empty.empty() {
		t.Fatal("zero searchQuery is not empty()")
	}
	if hit, ok := searchTranscript(path, empty); ok {
		t.Errorf("searchTranscript with empty query = %+v, true; want false", hit)
	}
}

func TestSearchExcerpt(t *testing.T) {
	long := strings.Repeat("a", 1000) + "NEEDLE" + strings.Repeat("b", 1000)
	japanese := strings.Repeat("あ", 1000) + "検索" + strings.Repeat("い", 1000)

	tests := []struct {
		name        string
		text        string
		term        string
		wantLead    bool // excerpt starts with an ellipsis
		wantTrail   bool // excerpt ends with an ellipsis
		wantCentred bool
	}{
		{name: "short text kept whole", text: "just osc8 here", term: "osc8"},
		{
			name: "match in the middle", text: long, term: "NEEDLE",
			wantLead: true, wantTrail: true, wantCentred: true,
		},
		{
			name: "match at the start", text: "NEEDLE" + strings.Repeat("b", 1000), term: "NEEDLE",
			wantTrail: true,
		},
		{
			name: "match at the end", text: strings.Repeat("a", 1000) + "NEEDLE", term: "NEEDLE",
			wantLead: true,
		},
		{
			name: "japanese window", text: japanese, term: "検索",
			wantLead: true, wantTrail: true, wantCentred: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			at := strings.Index(tt.text, tt.term)
			if at < 0 {
				t.Fatalf("fixture does not contain %q", tt.term)
			}
			got := searchExcerpt(tt.text, at)

			if !utf8.ValidString(got) {
				t.Fatalf("excerpt is not valid UTF-8: %q", got)
			}
			if !strings.Contains(got, tt.term) {
				t.Fatalf("excerpt %q does not contain the match %q", got, tt.term)
			}
			body := strings.Trim(got, "…")
			if n := utf8.RuneCountInString(body); n > searchExcerptRunes {
				t.Errorf("excerpt is %d runes, want <= %d", n, searchExcerptRunes)
			}
			if got, want := strings.HasPrefix(got, "…"), tt.wantLead; got != want {
				t.Errorf("leading ellipsis = %v, want %v", got, want)
			}
			if got, want := strings.HasSuffix(got, "…"), tt.wantTrail; got != want {
				t.Errorf("trailing ellipsis = %v, want %v", got, want)
			}
			if tt.wantCentred {
				// Roughly half the window ahead of the match: the point of
				// centring is that the reader sees the context on both sides.
				before := utf8.RuneCountInString(body[:strings.Index(body, tt.term)])
				if want := searchExcerptRunes / 2; before != want {
					t.Errorf("%d runes before the match, want %d", before, want)
				}
			}
		})
	}
}

// A window cut out of Japanese text must never slice a rune in half, and the
// anchor itself may land mid-rune when a multi-byte needle matches.
func TestSearchExcerptNeverSplitsRune(t *testing.T) {
	text := strings.Repeat("日本語のテキスト", 200)
	for at := 0; at < len(text); at++ {
		got := searchExcerpt(text, at)
		if !utf8.ValidString(got) {
			t.Fatalf("searchExcerpt(text, %d) = %q, not valid UTF-8", at, got)
		}
	}
	// Out-of-range anchors are clamped rather than panicking.
	for _, at := range []int{-1, len(text) + 10} {
		if got := searchExcerpt(text, at); !utf8.ValidString(got) {
			t.Fatalf("searchExcerpt(text, %d) = %q, not valid UTF-8", at, got)
		}
	}
}

func TestSearchQueryResidual(t *testing.T) {
	tests := []struct {
		name  string
		query string
		hay   string
		want  []string
	}{
		{name: "nothing satisfied", query: "osc8 ratchet", hay: "/src/other", want: []string{"osc8", "ratchet"}},
		{name: "all satisfied", query: "osc8 main", hay: "/src/osc8\nmain", want: nil},
		{name: "one satisfied", query: "osc8 ratchet", hay: "/src/osc8", want: []string{"ratchet"}},
		{name: "case insensitive haystack", query: "osc8", hay: "/src/OSC8Wrap", want: nil},
		{name: "empty haystack", query: "osc8", hay: "", want: []string{"osc8"}},
		{name: "japanese", query: "検索 osc8", hay: "/src/検索", want: []string{"osc8"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := mustQuery(t, tt.query)
			before := termStrings(q)

			got := termStrings(q.residual(tt.hay))
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("residual terms = %q, want %q", got, tt.want)
			}
			if q.empty() != (len(tt.query) == 0) {
				t.Errorf("receiver became empty() = %v", q.empty())
			}
			if now := termStrings(q); strings.Join(now, "|") != strings.Join(before, "|") {
				t.Errorf("residual rewrote the receiver: %q, want %q", now, before)
			}
		})
	}
}

func termStrings(q searchQuery) []string {
	var out []string
	for _, term := range q.terms {
		out = append(out, string(term))
	}
	return out
}

// residual must not compact into the receiver's backing array: grepSessions
// calls it once per session, and several listings can be running at once.
func TestSearchQueryResidualIsNotShared(t *testing.T) {
	q := mustQuery(t, "osc8 ratchet migration")

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// Each goroutine satisfies a different term, so an in-place compaction
			// would leave the others searching for the wrong things.
			hays := []string{"/src/osc8", "the ratchet", "migration notes", "/src/unrelated"}
			for i := 0; i < 200; i++ {
				hay := hays[(w+i)%len(hays)]
				r := q.residual(hay)
				want := 3
				if hay != "/src/unrelated" {
					want = 2
				}
				if len(r.terms) != want {
					t.Errorf("residual(%q) kept %d terms, want %d", hay, len(r.terms), want)
					return
				}
				if got := strings.Join(termStrings(q), "|"); got != "osc8|ratchet|migration" {
					t.Errorf("the query itself became %q", got)
					return
				}
			}
		}(w)
	}
	wg.Wait()
}

// newGrepCorpus writes n sessions, giving those whose index is in hit a
// conversation containing the term, and returns them in a fixed order.
func newGrepCorpus(t *testing.T, n int, hit map[int]bool) []Session {
	t.Helper()
	dir := t.TempDir()
	sessions := make([]Session, 0, n)
	for i := 0; i < n; i++ {
		text := fmt.Sprintf("session %d talks about nothing in particular", i)
		if hit[i] {
			text = fmt.Sprintf("session %d talks about ghostty", i)
		}
		id := fmt.Sprintf("id-%03d", i)
		path := filepath.Join(dir, id+".jsonl")
		body := textLine("user", text) + "\n" + textLine("assistant", "understood") + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, Session{
			ID:             id,
			TranscriptPath: path,
			Cwd:            "/src/plain",
			Root:           "/src/plain",
		})
	}
	return sessions
}

// Every term must be satisfiable by EITHER half of the session, with the terms
// still ANDed across it.
func TestGrepSessionsMetadataOrBody(t *testing.T) {
	body := []string{
		textLine("user", "port the ratchet migration"),
		textLine("assistant", "the schema is now ratcheted"),
	}

	tests := []struct {
		name    string
		session Session
		query   string
		want    bool
		// wantMatch is the substring the excerpt must carry; empty means the
		// excerpt itself must be empty.
		wantMatch string
	}{
		{
			name:    "branch only",
			session: Session{Branch: "feat/ev-charge", Cwd: "/src/a", Root: "/src/a"},
			query:   "ev-charge",
			want:    true,
		},
		{
			name:    "repo basename only",
			session: Session{Cwd: "/src/x", Root: "/Users/mash/src/github.com/mash/osc8wrap"},
			query:   "osc8wrap",
			want:    true,
		},
		{
			name:    "worktree basename only",
			session: Session{Root: "/src/nature-server", Worktree: "/src/nature-server-feat-ev"},
			query:   "feat-ev",
			want:    true,
		},
		{
			name:    "cwd only",
			session: Session{Cwd: "/Users/mash/scratch/reports"},
			query:   "scratch",
			want:    true,
		},
		{
			name:    "case insensitive metadata",
			session: Session{Cwd: "/src/OSC8Wrap"},
			query:   "osc8wrap",
			want:    true,
		},
		{
			// The headline case: one term names the place, the other names what
			// was said there.
			name:      "one metadata term and one body term",
			session:   Session{Root: "/src/osc8wrap", Cwd: "/src/osc8wrap"},
			query:     "osc8wrap ratchet",
			want:      true,
			wantMatch: "ratchet migration",
		},
		{
			name:      "body only",
			session:   Session{Cwd: "/src/a", Root: "/src/a"},
			query:     "migration",
			want:      true,
			wantMatch: "ratchet migration",
		},
		{
			name:    "a term nothing satisfies drops the session",
			session: Session{Root: "/src/osc8wrap", Cwd: "/src/osc8wrap"},
			query:   "osc8wrap kubernetes",
			want:    false,
		},
		{
			// Terms are ANDed across the session, not required to share a source.
			name:      "metadata term repeated in the body still needs the other term",
			session:   Session{Root: "/src/osc8wrap", Cwd: "/src/osc8wrap"},
			query:     "osc8wrap ratchet schema",
			want:      true,
			wantMatch: "ratchet",
		},
		{
			// The status words are deliberately NOT in the haystack: --grep done
			// listing every finished session is a filter that hides nothing.
			name:    "tag word is not metadata",
			session: Session{Cwd: "/src/a", Root: "/src/a"},
			query:   "done",
			want:    false,
		},
		{
			// A term must not straddle two fields of the joined haystack.
			name:    "terms do not straddle fields",
			session: Session{Root: "/src/osc8wrap", Branch: "main"},
			query:   "osc8wrapmain",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.session
			s.ID = "the-session"
			s.TranscriptPath = writeLines(t, "session.jsonl", body...)

			kept := grepSessions([]Session{s}, mustQuery(t, tt.query))
			if got := len(kept) == 1; got != tt.want {
				t.Fatalf("kept %d sessions, want match = %v", len(kept), tt.want)
			}
			if !tt.want {
				return
			}
			if tt.wantMatch == "" {
				if kept[0].Match != "" {
					t.Errorf("Match = %q, want empty for a metadata-only hit", kept[0].Match)
				}
				return
			}
			if !strings.Contains(kept[0].Match, tt.wantMatch) {
				t.Errorf("Match = %q, want it to contain %q", kept[0].Match, tt.wantMatch)
			}
		})
	}
}

// The observable proof of the residual optimization: a session whose every term
// is already satisfied by its location is never opened, so an unreadable
// transcript path cannot change the answer.
func TestGrepSessionsSkipsTheTranscriptWhenMetadataSuffices(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.jsonl")
	s := Session{ID: "meta-only", Cwd: "/src/osc8wrap", Root: "/src/osc8wrap",
		Branch: "feat/ratchet", TranscriptPath: missing}

	kept := grepSessions([]Session{s}, mustQuery(t, "osc8wrap ratchet"))
	if len(kept) != 1 {
		t.Fatalf("kept %d sessions, want the metadata-only match", len(kept))
	}
	if kept[0].Match != "" {
		t.Errorf("Match = %q, want empty", kept[0].Match)
	}

	// The same session with one term the location cannot satisfy does have to
	// open the file, and then degrades to "no match" rather than failing.
	if kept := grepSessions([]Session{s}, mustQuery(t, "osc8wrap kubernetes")); len(kept) != 0 {
		t.Errorf("kept %d sessions, want none once the file is actually needed", len(kept))
	}
}

// A live session with no UUID -- the wrapped command is not claude, or claude
// has not published its session file yet -- has no transcript, but it does have
// a location, and that is enough for the terms that name one.
func TestGrepSessionsLiveSessionWithNoTranscript(t *testing.T) {
	s := Session{
		Live: &LiveSession{Entry: RegistryEntry{WrapperPID: 4242, Cwd: "/src/osc8wrap"}},
		Cwd:  "/src/osc8wrap", Root: "/src/osc8wrap", Branch: "main",
	}

	if kept := grepSessions([]Session{s}, mustQuery(t, "osc8wrap main")); len(kept) != 1 {
		t.Errorf("kept %d sessions, want the metadata match", len(kept))
	}
	if kept := grepSessions([]Session{s}, mustQuery(t, "ratchet")); len(kept) != 0 {
		t.Errorf("kept %d sessions, want none: there is no conversation to hold the term", len(kept))
	}
}

func TestGrepSessions(t *testing.T) {
	sessions := newGrepCorpus(t, 60, map[int]bool{3: true, 17: true, 42: true})
	// A session pointing at a file that does not exist degrades to "no match"
	// rather than failing the whole filter.
	sessions = append(sessions, Session{ID: "missing", Cwd: "/src/plain",
		TranscriptPath: filepath.Join(t.TempDir(), "gone.jsonl")})

	kept := grepSessions(sessions, mustQuery(t, "ghostty"))

	wantIDs := []string{"id-003", "id-017", "id-042"}
	var gotIDs []string
	for _, s := range kept {
		gotIDs = append(gotIDs, s.ID)
	}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("kept = %v, want %v", gotIDs, wantIDs)
	}
	for _, s := range kept {
		if !strings.Contains(s.Match, "ghostty") {
			t.Errorf("%s .Match = %q, want the excerpt that explains the hit", s.ID, s.Match)
		}
	}
	// The input is not annotated in place: the caller's slice keeps its rows
	// unmatched, which is what makes a second call over it mean the same thing.
	for i := range sessions {
		if sessions[i].Match != "" {
			t.Fatalf("grepSessions wrote Match %q back into its input", sessions[i].Match)
		}
	}
}

// The returned order is the input order, on every run: this feeds a picker, and
// a list that reshuffles between keystrokes is unusable.
func TestGrepSessionsIsDeterministic(t *testing.T) {
	hit := map[int]bool{}
	for i := 0; i < 40; i += 3 {
		hit[i] = true
	}
	sessions := newGrepCorpus(t, 40, hit)
	// One row that matches on its location only, in the middle of the corpus:
	// the rows that skip the transcript must land in input order too, not ahead
	// of the ones that were scanned.
	sessions[10].Cwd = "/src/ghostty"
	hit[10] = true
	q := mustQuery(t, "ghostty")

	kept := grepSessions(sessions, q)
	if len(kept) != len(hit) {
		t.Fatalf("kept %d, want %d", len(kept), len(hit))
	}
	first := make([]string, len(kept))
	for i, s := range kept {
		first[i] = s.ID
	}
	sorted := make([]string, len(first))
	copy(sorted, first)
	sort.Strings(sorted)
	if strings.Join(sorted, ",") != strings.Join(first, ",") {
		t.Fatalf("kept out of input order: %v", first)
	}
	for run := 0; run < 20; run++ {
		got := make([]string, 0, len(first))
		for _, s := range grepSessions(sessions, q) {
			got = append(got, s.ID)
		}
		if strings.Join(got, ",") != strings.Join(first, ",") {
			t.Fatalf("run %d returned %v, want %v", run, got, first)
		}
	}
}

// An empty query is not a filter: the sessions come back untouched, unannotated.
func TestGrepSessionsEmptyQuery(t *testing.T) {
	sessions := newGrepCorpus(t, 5, map[int]bool{1: true})
	var empty searchQuery

	kept := grepSessions(sessions, empty)
	if len(kept) != len(sessions) {
		t.Fatalf("kept %d sessions, want all %d", len(kept), len(sessions))
	}
	for i := range kept {
		if kept[i].ID != sessions[i].ID {
			t.Fatalf("kept[%d] = %s, want %s", i, kept[i].ID, sessions[i].ID)
		}
		if kept[i].Match != "" {
			t.Errorf("kept[%d] carries a match %q with nothing searched for", i, kept[i].Match)
		}
	}
}

func TestGrepSessionsEmptyInput(t *testing.T) {
	if kept := grepSessions(nil, mustQuery(t, "ghostty")); len(kept) != 0 {
		t.Errorf("grepSessions(nil) = %v, want empty", kept)
	}
}

// Run under -race: the workers write into per-index slots and several filters
// run at once over the same sessions and the same query.
func TestGrepSessionsConcurrentCallers(t *testing.T) {
	sessions := newGrepCorpus(t, 80, map[int]bool{5: true, 25: true, 75: true})
	q := mustQuery(t, "ghostty session")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if kept := grepSessions(sessions, q); len(kept) != 3 {
				t.Errorf("kept %d, want 3", len(kept))
			}
		}()
	}
	wg.Wait()
}
