package main

import "bytes"

type tokenKind int

const (
	tokenText tokenKind = iota
	tokenSGR
)

type token struct {
	kind   tokenKind
	start  int
	end    int
	styled bool
}

func scanTokens(data []byte, fn func(tok token, next int)) {
	for i := 0; i < len(data); {
		tok, next := nextToken(data, i)
		fn(tok, next)
		if next <= i {
			next = i + 1
		}
		i = next
	}
}

func nextToken(data []byte, start int) (token, int) {
	if start >= len(data) {
		return token{kind: tokenText, start: start, end: start}, start
	}
	escIdx := bytes.Index(data[start:], []byte("\x1b["))
	if escIdx == -1 {
		return token{kind: tokenText, start: start, end: len(data)}, len(data)
	}
	escIdx += start
	if escIdx > start {
		return token{kind: tokenText, start: start, end: escIdx}, escIdx
	}

	endIdx, styled, ok := parseSGRSequence(data, escIdx)
	if !ok {
		return token{kind: tokenText, start: escIdx, end: escIdx + 1}, escIdx + 1
	}
	return token{kind: tokenSGR, start: escIdx, end: endIdx, styled: styled}, endIdx
}

func parseSGRSequence(data []byte, start int) (end int, styled bool, ok bool) {
	if start+2 >= len(data) || data[start] != 0x1b || data[start+1] != '[' {
		return 0, false, false
	}
	i := start + 2
	for i < len(data) && data[i] != 'm' {
		b := data[i]
		if (b < '0' || b > '9') && b != ';' {
			return 0, false, false
		}
		i++
	}
	if i >= len(data) || data[i] != 'm' {
		return 0, false, false
	}
	params := data[start+2 : i]
	return i + 1, sgrEnablesStyle(params), true
}

func sgrEnablesStyle(params []byte) bool {
	if len(params) == 0 {
		return false
	}
	start := 0
	for i := 0; i <= len(params); i++ {
		if i == len(params) || params[i] == ';' {
			if i > start && !allZeros(params[start:i]) {
				return true
			}
			start = i + 1
		}
	}
	return false
}

func allZeros(s []byte) bool {
	for _, b := range s {
		if b != '0' {
			return false
		}
	}
	return true
}
