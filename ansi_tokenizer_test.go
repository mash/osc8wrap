package main

import (
	"bytes"
	"testing"
)

type tokenExpectation struct {
	kind   TokenKind
	data   string
	styled bool
	isEnd  bool
}

// assertTokens compares token streams in order.
func assertTokens(t *testing.T, got []Token, want []tokenExpectation) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected %d tokens, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i].Kind != want[i].kind {
			t.Errorf("token %d: expected kind %d, got %d", i, want[i].kind, got[i].Kind)
		}
		if string(got[i].Data) != want[i].data {
			t.Errorf("token %d: expected data %q, got %q", i, want[i].data, got[i].Data)
		}
		if got[i].Styled != want[i].styled {
			t.Errorf("token %d: expected styled %v, got %v", i, want[i].styled, got[i].Styled)
		}
		if got[i].IsEnd != want[i].isEnd {
			t.Errorf("token %d: expected isEnd %v, got %v", i, want[i].isEnd, got[i].IsEnd)
		}
	}
}

// TestAnsiTokenizerFeed exercises common tokenization paths with stepwise feeds.
func TestAnsiTokenizerFeed(t *testing.T) {
	type feedStep struct {
		input string
		want  []tokenExpectation
	}

	type tokenizerCase struct {
		name       string
		steps      []feedStep
		flush      []tokenExpectation
		wantStyled bool
		wantInOSC8 bool
	}

	cases := []tokenizerCase{
		{
			name: "basic-text",
			steps: []feedStep{
				{input: "hello world", want: []tokenExpectation{{kind: TokenText, data: "hello world"}}},
			},
			flush:      []tokenExpectation{},
			wantStyled: false,
			wantInOSC8: false,
		},
		{
			name: "sgr",
			steps: []feedStep{
				{
					input: "\x1b[31mred\x1b[0m",
					want: []tokenExpectation{
						{kind: TokenSGR, data: "\x1b[31m", styled: true},
						{kind: TokenText, data: "red"},
						{kind: TokenSGR, data: "\x1b[0m"},
					},
				},
			},
			flush:      []tokenExpectation{},
			wantStyled: false,
			wantInOSC8: false,
		},
		{
			name: "csi",
			steps: []feedStep{
				{
					input: "\x1b[2Jclear",
					want: []tokenExpectation{
						{kind: TokenCSI, data: "\x1b[2J"},
						{kind: TokenText, data: "clear"},
					},
				},
			},
			flush:      []tokenExpectation{},
			wantStyled: false,
			wantInOSC8: false,
		},
		{
			name: "osc",
			steps: []feedStep{
				{
					input: "\x1b]0;title\x07text",
					want: []tokenExpectation{
						{kind: TokenOSC, data: "\x1b]0;title\x07"},
						{kind: TokenText, data: "text"},
					},
				},
			},
			flush:      []tokenExpectation{},
			wantStyled: false,
			wantInOSC8: false,
		},
		{
			name: "osc8-st",
			steps: []feedStep{
				{
					input: "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\",
					want: []tokenExpectation{
						{kind: TokenOSC8, data: "\x1b]8;;https://example.com\x1b\\"},
						{kind: TokenText, data: "link"},
						{kind: TokenOSC8, data: "\x1b]8;;\x1b\\", isEnd: true},
					},
				},
			},
			flush:      []tokenExpectation{},
			wantStyled: false,
			wantInOSC8: false,
		},
		{
			name: "osc8-bel",
			steps: []feedStep{
				{
					input: "\x1b]8;;https://example.com\x07link",
					want: []tokenExpectation{
						{kind: TokenOSC8, data: "\x1b]8;;https://example.com\x07"},
						{kind: TokenText, data: "link"},
					},
				},
			},
			flush:      []tokenExpectation{},
			wantStyled: false,
			wantInOSC8: true,
		},
		{
			name: "chunked-sgr",
			steps: []feedStep{
				{
					input: "text\x1b[38;2;136;136",
					want:  []tokenExpectation{{kind: TokenText, data: "text"}},
				},
				{
					input: ";136mmore",
					want: []tokenExpectation{
						{kind: TokenSGR, data: "\x1b[38;2;136;136;136m", styled: true},
						{kind: TokenText, data: "more"},
					},
				},
			},
			flush:      []tokenExpectation{},
			wantStyled: true,
			wantInOSC8: false,
		},
		{
			name: "chunked-esc",
			steps: []feedStep{
				{
					input: "abc\x1b",
					want:  []tokenExpectation{{kind: TokenText, data: "abc"}},
				},
				{
					input: "[31mred",
					want: []tokenExpectation{
						{kind: TokenSGR, data: "\x1b[31m", styled: true},
						{kind: TokenText, data: "red"},
					},
				},
			},
			flush:      []tokenExpectation{},
			wantStyled: true,
			wantInOSC8: false,
		},
		{
			name: "flush-csi",
			steps: []feedStep{
				{
					input: "text\x1b[38;2;136",
					want:  []tokenExpectation{{kind: TokenText, data: "text"}},
				},
			},
			flush:      []tokenExpectation{{kind: TokenCSI, data: "\x1b[38;2;136", styled: true}},
			wantStyled: true,
			wantInOSC8: false,
		},
		{
			name: "flush-esc",
			steps: []feedStep{
				{
					input: "text\x1b",
					want:  []tokenExpectation{{kind: TokenText, data: "text"}},
				},
			},
			flush:      []tokenExpectation{{kind: TokenESC, data: "\x1b"}},
			wantStyled: false,
			wantInOSC8: false,
		},
		{
			name: "dcs",
			steps: []feedStep{
				{
					input: "\x1bPdata\x1b\\text",
					want: []tokenExpectation{
						{kind: TokenDCS, data: "\x1bPdata\x1b\\"},
						{kind: TokenText, data: "text"},
					},
				},
			},
			flush:      []tokenExpectation{},
			wantStyled: false,
			wantInOSC8: false,
		},
		{
			name: "esc",
			steps: []feedStep{
				{
					input: "\x1b7text\x1b8",
					want: []tokenExpectation{
						{kind: TokenESC, data: "\x1b7"},
						{kind: TokenText, data: "text"},
						{kind: TokenESC, data: "\x1b8"},
					},
				},
			},
			flush:      []tokenExpectation{},
			wantStyled: false,
			wantInOSC8: false,
		},
		{
			name: "invalid-csi",
			steps: []feedStep{
				{
					input: "\x1b[\x00text",
					want: []tokenExpectation{
						{kind: TokenCSI, data: "\x1b[\x00"},
						{kind: TokenText, data: "text"},
					},
				},
			},
			flush:      []tokenExpectation{},
			wantStyled: false,
			wantInOSC8: false,
		},
		{
			name: "multiple-sgr",
			steps: []feedStep{
				{
					input: "\x1b[1m\x1b[31mtext",
					want: []tokenExpectation{
						{kind: TokenSGR, data: "\x1b[1m", styled: true},
						{kind: TokenSGR, data: "\x1b[31m", styled: true},
						{kind: TokenText, data: "text"},
					},
				},
			},
			flush:      []tokenExpectation{},
			wantStyled: true,
			wantInOSC8: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := NewAnsiTokenizer()
			for _, step := range tc.steps {
				got := tok.Feed([]byte(step.input))
				assertTokens(t, got, step.want)
			}
			flush := tok.Flush()
			assertTokens(t, flush, tc.flush)
			if tok.Styled() != tc.wantStyled {
				t.Errorf("expected styled %v, got %v", tc.wantStyled, tok.Styled())
			}
			if tok.InOSC8() != tc.wantInOSC8 {
				t.Errorf("expected inOSC8 %v, got %v", tc.wantInOSC8, tok.InOSC8())
			}
		})
	}
}

func TestSgrSetsStyled(t *testing.T) {
	tests := []struct {
		params   string
		styled   bool
		explicit bool
	}{
		{"", false, true},
		{"0", false, true},
		{"1", true, true},
		{"31", true, true},
		{"0;31", true, true},
		{"31;0", false, true},
		{"31;0m", false, true},
		{"38;5;196", true, true},
		{"38;2;255;0;0", true, true},
		{"39", false, true},
		{"49", false, true},
		{"22", false, true},
		{"1;22", false, true},
		{"22;1", true, true},
		{"40", true, true},
		{"100", true, true},
	}

	for _, tt := range tests {
		styled, explicit := sgrSetsStyled([]byte(tt.params))
		if styled != tt.styled || explicit != tt.explicit {
			t.Errorf("sgrSetsStyled(%q): got (%v, %v), want (%v, %v)",
				tt.params, styled, explicit, tt.styled, tt.explicit)
		}
	}
}

func TestParseOSC8(t *testing.T) {
	tests := []struct {
		data  string
		isEnd bool
		ok    bool
	}{
		{"8;;https://example.com", false, true},
		{"8;;", true, true},
		{"8;id=foo;https://example.com", false, true},
		{"8;id=foo;", true, true},
		{"0;title", false, false},
		{"8", false, false},
		{"8;", false, false},
	}

	for _, tt := range tests {
		isEnd, ok := parseOSC8([]byte(tt.data))
		if isEnd != tt.isEnd || ok != tt.ok {
			t.Errorf("parseOSC8(%q): got (%v, %v), want (%v, %v)",
				tt.data, isEnd, ok, tt.isEnd, tt.ok)
		}
	}
}

func TestAnsiTokenizerBufferOverflow(t *testing.T) {
	tests := []struct {
		name      string
		input     []byte
		wantKind  TokenKind
		wantState state
	}{
		{
			name: "osc-overflow",
			input: func() []byte {
				b := make([]byte, maxBufferSize+100)
				b[0] = 0x1b
				b[1] = ']'
				for i := 2; i < len(b); i++ {
					b[i] = 'x'
				}
				return b
			}(),
			wantKind:  TokenOther,
			wantState: stateGround,
		},
		{
			name:      "text-overflow",
			input:     bytes.Repeat([]byte("a"), maxBufferSize+10),
			wantKind:  TokenText,
			wantState: stateGround,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok := NewAnsiTokenizer()
			tokens := tok.Feed(tt.input)
			if len(tokens) == 0 {
				t.Fatal("expected tokens for overflow input")
			}

			found := false
			var out []byte
			for _, token := range tokens {
				if token.Kind == tt.wantKind {
					found = true
				}
				out = append(out, token.Data...)
			}

			if !found {
				t.Errorf("expected token kind %d for %s", tt.wantKind, tt.name)
			}
			if tok.state != tt.wantState {
				t.Errorf("expected state %v after overflow, got %v", tt.wantState, tok.state)
			}
			if !bytes.Equal(out, tt.input) {
				t.Errorf("expected output to match input, got len %d want %d", len(out), len(tt.input))
			}
		})
	}
}

// TestAnsiTokenizerDataLifetime ensures copied token data stays stable after later feeds.
func TestAnsiTokenizerDataLifetime(t *testing.T) {
	tests := []struct {
		name   string
		first  string
		second string
		want   string
	}{
		{name: "simple", first: "first", second: "second", want: "first"},
		{name: "symbols", first: "alpha", second: "beta", want: "alpha"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok := NewAnsiTokenizer()
			tokens1 := tok.Feed([]byte(tt.first))
			// Copy token data to verify it is safe to retain across subsequent feeds.
			data1Copy := make([]byte, len(tokens1[0].Data))
			copy(data1Copy, tokens1[0].Data)

			tok.Feed([]byte(tt.second))

			if !bytes.Equal(data1Copy, []byte(tt.want)) {
				t.Errorf("expected copy %q, got %q", tt.want, data1Copy)
			}
		})
	}
}
