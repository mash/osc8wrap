package main

import "bytes"

type TokenKind int

const (
	TokenText  TokenKind = iota // Plain text, may contain symbols to link
	TokenSGR                    // CSI Pm m - Select Graphic Rendition (colors, bold, etc.)
	TokenCSI                    // CSI sequences other than SGR (cursor control, etc.)
	TokenOSC8                   // OSC 8 hyperlink sequence
	TokenOSC                    // OSC sequences other than OSC 8 (window title, etc.)
	TokenDCS                    // Device Control String (ESC P ... ST)
	TokenOther                  // APC, PM, SOS, or buffer overflow
	TokenESC                    // ESC + single byte that's not a sequence introducer
)

type Token struct {
	Kind   TokenKind
	Data   []byte
	Styled bool // TokenSGR: true if styling remains active after this token
	IsEnd  bool // TokenOSC8: true if this is a link-closing sequence (empty URI)
}

type state int

const (
	stateGround      state = iota // Normal text processing
	stateEsc                      // Received ESC, waiting for sequence introducer
	stateCSI                      // Inside CSI sequence (ESC [), collecting params
	stateOSC                      // Inside OSC sequence (ESC ]), collecting string
	stateSTCandidate              // Received ESC inside OSC/DCS, checking for ST (\)
	stateDCS                      // Inside DCS/APC/PM sequence, waiting for ST
)

const (
	escByte = 0x1b
	belByte = 0x07
)

// maxBufferSize limits buffer growth for unterminated OSC/DCS sequences.
// If exceeded, the incomplete sequence is emitted as TokenOther and parsing resets.
const maxBufferSize = 4096

type sgrState struct {
	fgActive bool
	bgActive bool
	attrs    uint16
}

const (
	attrBold uint16 = 1 << iota
	attrFaint
	attrItalic
	attrUnderline
	attrBlinkSlow
	attrBlinkRapid
	attrInverse
	attrConceal
	attrStrikethrough
)

func (s *sgrState) styled() bool {
	return s.fgActive || s.bgActive || s.attrs != 0
}

func (s *sgrState) reset() {
	s.fgActive = false
	s.bgActive = false
	s.attrs = 0
}

type AnsiTokenizer struct {
	buf       []byte
	state     state
	prevState state
	sgr       sgrState
	inOSC8    bool
}

func NewAnsiTokenizer() *AnsiTokenizer {
	return &AnsiTokenizer{
		state: stateGround,
	}
}

func (t *AnsiTokenizer) Feed(p []byte) []Token {
	var tokens []Token

	for i := 0; i < len(p); i++ {
		b := p[i]

		switch t.state {
		case stateGround:
			if b == escByte {
				if len(t.buf) > 0 {
					tokens = append(tokens, Token{Kind: TokenText, Data: t.copyBuf()})
					t.buf = t.buf[:0]
				}
				t.buf = append(t.buf, b)
				t.state = stateEsc
			} else {
				t.buf = append(t.buf, b)
			}

		case stateEsc:
			t.buf = append(t.buf, b)
			switch b {
			case '[':
				t.state = stateCSI
			case ']':
				t.state = stateOSC
			case 'P', '_', '^':
				t.state = stateDCS
			default:
				tokens = append(tokens, Token{Kind: TokenESC, Data: t.copyBuf()})
				t.buf = t.buf[:0]
				t.state = stateGround
			}

		case stateCSI:
			t.buf = append(t.buf, b)
			if isCSIFinalByte(b) {
				tok := t.emitCSI()
				tokens = append(tokens, tok)
				t.buf = t.buf[:0]
				t.state = stateGround
			} else if !isCSIParamByte(b) && !isCSIIntermediateByte(b) {
				tokens = append(tokens, Token{Kind: TokenCSI, Data: t.copyBuf()})
				t.buf = t.buf[:0]
				t.state = stateGround
			}

		case stateOSC:
			t.buf = append(t.buf, b)
			switch b {
			case belByte:
				tok := t.emitOSC()
				tokens = append(tokens, tok)
				t.buf = t.buf[:0]
				t.state = stateGround
			case escByte:
				t.prevState = stateOSC
				t.state = stateSTCandidate
			}

		case stateSTCandidate:
			t.buf = append(t.buf, b)
			switch b {
			case '\\':
				if t.prevState == stateDCS {
					tokens = append(tokens, Token{Kind: TokenDCS, Data: t.copyBuf()})
				} else {
					tok := t.emitOSC()
					tokens = append(tokens, tok)
				}
				t.buf = t.buf[:0]
				t.state = stateGround
			default:
				t.state = t.prevState
			}

		case stateDCS:
			t.buf = append(t.buf, b)
			switch b {
			case escByte:
				t.prevState = stateDCS
				t.state = stateSTCandidate
			default:
			}
		}

		if len(t.buf) > maxBufferSize {
			if t.state == stateGround {
				tokens = append(tokens, Token{Kind: TokenText, Data: t.copyBuf()})
			} else {
				tokens = append(tokens, Token{Kind: TokenOther, Data: t.copyBuf()})
				t.state = stateGround
			}
			t.buf = t.buf[:0]
		}
	}

	if t.state == stateGround && len(t.buf) > 0 {
		tokens = append(tokens, Token{Kind: TokenText, Data: t.copyBuf()})
		t.buf = t.buf[:0]
	}

	return tokens
}

func (t *AnsiTokenizer) Flush() []Token {
	if len(t.buf) == 0 {
		return nil
	}

	kind := t.inferIncompleteKind()
	tok := Token{Kind: kind, Data: t.copyBuf()}

	if kind == TokenCSI && len(t.buf) >= 2 {
		params := t.buf[2:]
		applySGRParams(params, &t.sgr)
		tok.Styled = t.sgr.styled()
	}

	t.buf = t.buf[:0]
	t.state = stateGround

	return []Token{tok}
}

func (t *AnsiTokenizer) Styled() bool {
	return t.sgr.styled()
}

func (t *AnsiTokenizer) InOSC8() bool {
	return t.inOSC8
}

func (t *AnsiTokenizer) copyBuf() []byte {
	cp := make([]byte, len(t.buf))
	copy(cp, t.buf)
	return cp
}

func (t *AnsiTokenizer) inferIncompleteKind() TokenKind {
	if len(t.buf) == 0 {
		return TokenText
	}
	if t.buf[0] != escByte {
		return TokenText
	}
	if len(t.buf) == 1 {
		return TokenESC
	}
	switch t.buf[1] {
	case '[':
		return TokenCSI
	case ']':
		return TokenOSC
	case 'P', '_', '^':
		return TokenDCS
	default:
		return TokenESC
	}
}

func (t *AnsiTokenizer) emitCSI() Token {
	data := t.copyBuf()
	tok := Token{Kind: TokenCSI, Data: data}

	if len(data) >= 3 && data[len(data)-1] == 'm' {
		tok.Kind = TokenSGR
		params := data[2 : len(data)-1]
		applySGRParams(params, &t.sgr)
		tok.Styled = t.sgr.styled()
	}

	return tok
}

func (t *AnsiTokenizer) emitOSC() Token {
	data := t.copyBuf()

	oscData := extractOSCData(data)
	if isEnd, ok := parseOSC8(oscData); ok {
		t.inOSC8 = !isEnd
		return Token{Kind: TokenOSC8, Data: data, IsEnd: isEnd}
	}

	return Token{Kind: TokenOSC, Data: data}
}

func extractOSCData(data []byte) []byte {
	if len(data) < 2 {
		return nil
	}
	start := 2
	if len(data) > start && data[start] == ';' {
		start++
	}

	end := len(data)
	if end > 0 && data[end-1] == belByte {
		end--
	} else if end >= 2 && data[end-2] == escByte && data[end-1] == '\\' {
		end -= 2
	}

	if start >= end {
		return nil
	}
	return data[start:end]
}

func isCSIFinalByte(b byte) bool {
	return b >= 0x40 && b <= 0x7e
}

func isCSIParamByte(b byte) bool {
	return b >= 0x30 && b <= 0x3f
}

func isCSIIntermediateByte(b byte) bool {
	return b >= 0x20 && b <= 0x2f
}

func sgrSetsStyled(params []byte) (styled bool, explicit bool) {
	var st sgrState
	explicit = applySGRParams(params, &st)
	return st.styled(), explicit
}

func applySGRParams(params []byte, st *sgrState) (explicit bool) {
	if len(params) == 0 {
		st.reset()
		return true
	}

	codes := parseCSIParams(params)
	for i := 0; i < len(codes); i++ {
		code := codes[i]
		switch code {
		case 0:
			st.reset()
			explicit = true
		case 1:
			st.attrs |= attrBold
			explicit = true
		case 2:
			st.attrs |= attrFaint
			explicit = true
		case 3:
			st.attrs |= attrItalic
			explicit = true
		case 4:
			st.attrs |= attrUnderline
			explicit = true
		case 5:
			st.attrs |= attrBlinkSlow
			explicit = true
		case 6:
			st.attrs |= attrBlinkRapid
			explicit = true
		case 7:
			st.attrs |= attrInverse
			explicit = true
		case 8:
			st.attrs |= attrConceal
			explicit = true
		case 9:
			st.attrs |= attrStrikethrough
			explicit = true
		case 22:
			st.attrs &^= attrBold | attrFaint
			explicit = true
		case 23:
			st.attrs &^= attrItalic
			explicit = true
		case 24:
			st.attrs &^= attrUnderline
			explicit = true
		case 25:
			st.attrs &^= attrBlinkSlow | attrBlinkRapid
			explicit = true
		case 27:
			st.attrs &^= attrInverse
			explicit = true
		case 28:
			st.attrs &^= attrConceal
			explicit = true
		case 29:
			st.attrs &^= attrStrikethrough
			explicit = true
		case 39:
			st.fgActive = false
			explicit = true
		case 49:
			st.bgActive = false
			explicit = true
		default:
			if (code >= 30 && code <= 37) || (code >= 90 && code <= 97) {
				st.fgActive = true
				explicit = true
			} else if code == 38 {
				st.fgActive = true
				explicit = true
				i += skipExtendedColor(codes, i+1)
			} else if (code >= 40 && code <= 47) || (code >= 100 && code <= 107) {
				st.bgActive = true
				explicit = true
			} else if code == 48 {
				st.bgActive = true
				explicit = true
				i += skipExtendedColor(codes, i+1)
			}
		}
	}
	return explicit
}

func skipExtendedColor(codes []int, start int) int {
	if start >= len(codes) {
		return 0
	}
	switch codes[start] {
	case 5:
		return 2
	case 2:
		return 4
	default:
		return 0
	}
}

func parseCSIParams(params []byte) []int {
	var codes []int
	start := 0
	for i := 0; i <= len(params); i++ {
		if i == len(params) || params[i] == ';' {
			if i > start {
				code := parseNumber(params[start:i])
				codes = append(codes, code)
			} else {
				codes = append(codes, 0)
			}
			start = i + 1
		}
	}
	return codes
}

func parseNumber(s []byte) int {
	n := 0
	for _, b := range s {
		if b >= '0' && b <= '9' {
			n = n*10 + int(b-'0')
		}
	}
	return n
}

func parseOSC8(data []byte) (isEnd bool, ok bool) {
	if !bytes.HasPrefix(data, []byte("8;")) {
		return false, false
	}
	parts := bytes.SplitN(data, []byte(";"), 3)
	if len(parts) < 3 {
		return false, false
	}
	uri := parts[2]
	return len(uri) == 0, true
}
