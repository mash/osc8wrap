package main

import (
	"bytes"
	"testing"
)

func TestAnsiTokenizerBasicText(t *testing.T) {
	tok := NewAnsiTokenizer()
	tokens := tok.Feed([]byte("hello world"))
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}
	if tokens[0].Kind != TokenText {
		t.Errorf("expected TokenText, got %d", tokens[0].Kind)
	}
	if string(tokens[0].Data) != "hello world" {
		t.Errorf("expected 'hello world', got %q", tokens[0].Data)
	}
}

func TestAnsiTokenizerSGR(t *testing.T) {
	tok := NewAnsiTokenizer()
	tokens := tok.Feed([]byte("\x1b[31mred\x1b[0m"))
	if len(tokens) != 3 {
		t.Fatalf("expected 3 tokens, got %d", len(tokens))
	}

	if tokens[0].Kind != TokenSGR {
		t.Errorf("token 0: expected TokenSGR, got %d", tokens[0].Kind)
	}
	if string(tokens[0].Data) != "\x1b[31m" {
		t.Errorf("token 0: expected '\\x1b[31m', got %q", tokens[0].Data)
	}
	if !tokens[0].Styled {
		t.Error("token 0: expected Styled=true")
	}

	if tokens[1].Kind != TokenText {
		t.Errorf("token 1: expected TokenText, got %d", tokens[1].Kind)
	}
	if string(tokens[1].Data) != "red" {
		t.Errorf("token 1: expected 'red', got %q", tokens[1].Data)
	}

	if tokens[2].Kind != TokenSGR {
		t.Errorf("token 2: expected TokenSGR, got %d", tokens[2].Kind)
	}
	if tokens[2].Styled {
		t.Error("token 2: expected Styled=false after reset")
	}
}

func TestAnsiTokenizerCSI(t *testing.T) {
	tok := NewAnsiTokenizer()
	tokens := tok.Feed([]byte("\x1b[2Jclear"))
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}

	if tokens[0].Kind != TokenCSI {
		t.Errorf("token 0: expected TokenCSI, got %d", tokens[0].Kind)
	}
	if string(tokens[0].Data) != "\x1b[2J" {
		t.Errorf("token 0: expected '\\x1b[2J', got %q", tokens[0].Data)
	}

	if tokens[1].Kind != TokenText {
		t.Errorf("token 1: expected TokenText, got %d", tokens[1].Kind)
	}
}

func TestAnsiTokenizerOSC(t *testing.T) {
	tok := NewAnsiTokenizer()
	tokens := tok.Feed([]byte("\x1b]0;title\x07text"))
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}

	if tokens[0].Kind != TokenOSC {
		t.Errorf("token 0: expected TokenOSC, got %d", tokens[0].Kind)
	}

	if tokens[1].Kind != TokenText {
		t.Errorf("token 1: expected TokenText, got %d", tokens[1].Kind)
	}
}

func TestAnsiTokenizerOSC8(t *testing.T) {
	tok := NewAnsiTokenizer()
	tokens := tok.Feed([]byte("\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\"))
	if len(tokens) != 3 {
		t.Fatalf("expected 3 tokens, got %d", len(tokens))
	}

	if tokens[0].Kind != TokenOSC8 {
		t.Errorf("token 0: expected TokenOSC8, got %d", tokens[0].Kind)
	}
	if tokens[0].IsEnd {
		t.Error("token 0: expected IsEnd=false for link start")
	}

	if tokens[1].Kind != TokenText {
		t.Errorf("token 1: expected TokenText, got %d", tokens[1].Kind)
	}

	if tokens[2].Kind != TokenOSC8 {
		t.Errorf("token 2: expected TokenOSC8, got %d", tokens[2].Kind)
	}
	if !tokens[2].IsEnd {
		t.Error("token 2: expected IsEnd=true for link end")
	}
}

func TestAnsiTokenizerChunkedInput(t *testing.T) {
	tok := NewAnsiTokenizer()

	tokens1 := tok.Feed([]byte("text\x1b[38;2;136;136"))
	if len(tokens1) != 1 {
		t.Fatalf("chunk 1: expected 1 token, got %d", len(tokens1))
	}
	if tokens1[0].Kind != TokenText {
		t.Errorf("chunk 1: expected TokenText, got %d", tokens1[0].Kind)
	}
	if string(tokens1[0].Data) != "text" {
		t.Errorf("chunk 1: expected 'text', got %q", tokens1[0].Data)
	}

	tokens2 := tok.Feed([]byte(";136mmore"))
	if len(tokens2) != 2 {
		t.Fatalf("chunk 2: expected 2 tokens, got %d", len(tokens2))
	}
	if tokens2[0].Kind != TokenSGR {
		t.Errorf("chunk 2 token 0: expected TokenSGR, got %d", tokens2[0].Kind)
	}
	if string(tokens2[0].Data) != "\x1b[38;2;136;136;136m" {
		t.Errorf("chunk 2 token 0: expected SGR sequence, got %q", tokens2[0].Data)
	}
	if tokens2[1].Kind != TokenText {
		t.Errorf("chunk 2 token 1: expected TokenText, got %d", tokens2[1].Kind)
	}
	if string(tokens2[1].Data) != "more" {
		t.Errorf("chunk 2 token 1: expected 'more', got %q", tokens2[1].Data)
	}
}

func TestAnsiTokenizerChunkedEscOnly(t *testing.T) {
	tok := NewAnsiTokenizer()

	tokens1 := tok.Feed([]byte("abc\x1b"))
	if len(tokens1) != 1 {
		t.Fatalf("chunk 1: expected 1 token, got %d", len(tokens1))
	}
	if string(tokens1[0].Data) != "abc" {
		t.Errorf("chunk 1: expected 'abc', got %q", tokens1[0].Data)
	}

	tokens2 := tok.Feed([]byte("[31mred"))
	if len(tokens2) != 2 {
		t.Fatalf("chunk 2: expected 2 tokens, got %d", len(tokens2))
	}
	if tokens2[0].Kind != TokenSGR {
		t.Errorf("chunk 2 token 0: expected TokenSGR, got %d", tokens2[0].Kind)
	}
	if tokens2[1].Kind != TokenText {
		t.Errorf("chunk 2 token 1: expected TokenText, got %d", tokens2[1].Kind)
	}
}

func TestAnsiTokenizerFlush(t *testing.T) {
	tok := NewAnsiTokenizer()

	tok.Feed([]byte("text\x1b[38;2;136"))
	tokens := tok.Flush()

	if len(tokens) != 1 {
		t.Fatalf("expected 1 token from Flush, got %d", len(tokens))
	}
	if tokens[0].Kind != TokenCSI {
		t.Errorf("expected TokenCSI from Flush, got %d", tokens[0].Kind)
	}
}

func TestAnsiTokenizerFlushESC(t *testing.T) {
	tok := NewAnsiTokenizer()

	tok.Feed([]byte("text\x1b"))
	flushTokens := tok.Flush()

	if len(flushTokens) != 1 {
		t.Fatalf("expected 1 token from Flush, got %d", len(flushTokens))
	}
	if flushTokens[0].Kind != TokenESC {
		t.Errorf("expected TokenESC from Flush, got %d", flushTokens[0].Kind)
	}
}

func TestAnsiTokenizerStyledState(t *testing.T) {
	tok := NewAnsiTokenizer()

	if tok.Styled() {
		t.Error("initial styled should be false")
	}

	tok.Feed([]byte("\x1b[31m"))
	if !tok.Styled() {
		t.Error("styled should be true after foreground color")
	}

	tok.Feed([]byte("\x1b[0m"))
	if tok.Styled() {
		t.Error("styled should be false after reset")
	}
}

func TestAnsiTokenizerInOSC8State(t *testing.T) {
	tok := NewAnsiTokenizer()

	if tok.InOSC8() {
		t.Error("initial inOSC8 should be false")
	}

	tok.Feed([]byte("\x1b]8;;https://example.com\x1b\\"))
	if !tok.InOSC8() {
		t.Error("inOSC8 should be true after link start")
	}

	tok.Feed([]byte("\x1b]8;;\x1b\\"))
	if tok.InOSC8() {
		t.Error("inOSC8 should be false after link end")
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
		params := []byte(tt.params)
		if len(params) > 0 && params[len(params)-1] == 'm' {
			params = params[:len(params)-1]
		}
		styled, explicit := sgrSetsStyled(params)
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
		name           string
		input          []byte
		wantKind       TokenKind
		wantState      state
	}{
		{
			name:     "osc-overflow",
			input:    func() []byte { b := make([]byte, maxBufferSize+100); b[0] = 0x1b; b[1] = ']'; for i := 2; i < len(b); i++ { b[i] = 'x' }; return b }(),
			wantKind: TokenOther,
			wantState: stateGround,
		},
		{
			name:           "text-overflow",
			input:          bytes.Repeat([]byte("a"), maxBufferSize+10),
			wantKind:       TokenText,
			wantState:      stateGround,
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

func TestAnsiTokenizerDCS(t *testing.T) {
	tok := NewAnsiTokenizer()
	tokens := tok.Feed([]byte("\x1bPdata\x1b\\text"))
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}

	if tokens[0].Kind != TokenDCS {
		t.Errorf("token 0: expected TokenDCS, got %d", tokens[0].Kind)
	}

	if tokens[1].Kind != TokenText {
		t.Errorf("token 1: expected TokenText, got %d", tokens[1].Kind)
	}
}

func TestAnsiTokenizerESC(t *testing.T) {
	tok := NewAnsiTokenizer()
	tokens := tok.Feed([]byte("\x1b7text\x1b8"))
	if len(tokens) != 3 {
		t.Fatalf("expected 3 tokens, got %d", len(tokens))
	}

	if tokens[0].Kind != TokenESC {
		t.Errorf("token 0: expected TokenESC, got %d", tokens[0].Kind)
	}
	if string(tokens[0].Data) != "\x1b7" {
		t.Errorf("token 0: expected '\\x1b7', got %q", tokens[0].Data)
	}

	if tokens[1].Kind != TokenText {
		t.Errorf("token 1: expected TokenText, got %d", tokens[1].Kind)
	}

	if tokens[2].Kind != TokenESC {
		t.Errorf("token 2: expected TokenESC, got %d", tokens[2].Kind)
	}
}

func TestAnsiTokenizerInvalidCSI(t *testing.T) {
	tok := NewAnsiTokenizer()
	tokens := tok.Feed([]byte("\x1b[\x00text"))

	found := false
	for _, token := range tokens {
		if token.Kind == TokenCSI {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected invalid CSI to be emitted as TokenCSI")
	}
}

func TestAnsiTokenizerOSCWithBEL(t *testing.T) {
	tok := NewAnsiTokenizer()
	tokens := tok.Feed([]byte("\x1b]8;;https://example.com\x07link"))
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}

	if tokens[0].Kind != TokenOSC8 {
		t.Errorf("token 0: expected TokenOSC8, got %d", tokens[0].Kind)
	}
	if tokens[0].IsEnd {
		t.Error("token 0: expected IsEnd=false")
	}
	if string(tokens[0].Data) != "\x1b]8;;https://example.com\x07" {
		t.Errorf("token 0: expected OSC data with BEL terminator, got %q", tokens[0].Data)
	}

	if tokens[1].Kind != TokenText {
		t.Errorf("token 1: expected TokenText, got %d", tokens[1].Kind)
	}
}

func TestAnsiTokenizerMultipleSGR(t *testing.T) {
	tok := NewAnsiTokenizer()
	tokens := tok.Feed([]byte("\x1b[1m\x1b[31mtext"))

	if len(tokens) != 3 {
		t.Fatalf("expected 3 tokens, got %d", len(tokens))
	}

	if tokens[0].Kind != TokenSGR {
		t.Errorf("token 0: expected TokenSGR, got %d", tokens[0].Kind)
	}
	if !tokens[0].Styled {
		t.Error("token 0: expected Styled=true for bold")
	}

	if tokens[1].Kind != TokenSGR {
		t.Errorf("token 1: expected TokenSGR, got %d", tokens[1].Kind)
	}
	if !tokens[1].Styled {
		t.Error("token 1: expected Styled=true for foreground")
	}
}

func TestAnsiTokenizerDataLifetime(t *testing.T) {
	tok := NewAnsiTokenizer()

	tokens1 := tok.Feed([]byte("first"))
	data1Copy := make([]byte, len(tokens1[0].Data))
	copy(data1Copy, tokens1[0].Data)

	tok.Feed([]byte("second"))

	if !bytes.Equal(data1Copy, []byte("first")) {
		t.Error("copied data should remain valid after next Feed()")
	}
}
