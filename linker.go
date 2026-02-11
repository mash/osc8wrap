package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type LinkerOptions struct {
	Output          io.Writer
	Cwd             string
	Hostname        string
	Scheme          string
	Domains         []string
	ResolveBasename bool
	ExcludeDirs     []string
	Terminator      string // "st" (default, ESC \) or "bel" (0x07)
	SymbolLinks     bool
	DebugWrites     bool
}

type Linker struct {
	output          io.Writer
	cwd             string
	hostname        string
	scheme          string
	fileCache       map[string]bool
	domains         []string
	urlPattern      *regexp.Regexp
	resolveBasename bool
	excludeDirs     []string
	index           *FileIndex
	terminator      string
	symbolLinks     bool
	debugFile       *os.File
	writeSeq        int
	tokenizer       *AnsiTokenizer
	styled          bool // true when inside SGR-styled text; enables symbol linking
	inOSC8          bool // true when inside OSC8 hyperlink; disables all processing
}

func NewLinker(opts LinkerOptions) *Linker {
	scheme := opts.Scheme
	if scheme == "" {
		scheme = "file"
	}
	terminator := opts.Terminator
	if terminator == "" {
		terminator = "st"
	}
	l := &Linker{
		output:          opts.Output,
		cwd:             opts.Cwd,
		hostname:        opts.Hostname,
		scheme:          scheme,
		fileCache:       make(map[string]bool),
		domains:         opts.Domains,
		resolveBasename: opts.ResolveBasename,
		excludeDirs:     opts.ExcludeDirs,
		index:           NewFileIndex(opts.Cwd, opts.ExcludeDirs),
		terminator:      terminator,
		symbolLinks:     opts.SymbolLinks,
		tokenizer:       NewAnsiTokenizer(),
	}
	l.urlPattern = l.buildPattern()
	if opts.DebugWrites {
		f, err := os.CreateTemp("", "osc8wrap-debug-*.log")
		if err == nil {
			l.debugFile = f
			fmt.Fprintf(os.Stderr, "osc8wrap: debug writes log: %s\n", f.Name())
		}
	}
	return l
}

func (l *Linker) buildPattern() *regexp.Regexp {
	// group 1: https URL
	pattern := `(https://[^\s<>"'\x60\x00-\x1f\x7f]+)`

	// group 2: bare domain URL with boundary (github.com/..., etc.)
	// boundary is included to prevent file path pattern from matching domain names
	if len(l.domains) > 0 {
		escaped := make([]string, len(l.domains))
		for i, d := range l.domains {
			escaped[i] = regexp.QuoteMeta(d)
		}
		pattern += `|(?:^|[^/\w.-]|\x1b\[[0-9;]*m)((?:` + strings.Join(escaped, "|") + `)/[^\s<>"'\x60\x00-\x1f\x7f]+)`
	} else {
		pattern += `|()` // empty group to keep group numbers consistent
	}

	// file path pattern
	pattern += `|` +
		`(?:^|[^/\w.%+@\x{0080}-\x{10FFFF}-]|\x1b\[[0-9;]*m)` + // boundary: start of line, non-path char, or ANSI SGR
		`(` + // group 3: path
		`(?:~|\.{0,2})/[\w./%+@\x{0080}-\x{10FFFF}-]+(?:\.\w+)?` + // starts with ~/, /, ./, or ../: extension optional
		`|` +
		`[\w./%+@\x{0080}-\x{10FFFF}-]+\.\w+` + // no path prefix: extension required
		`|` +
		`\w+file` + // files ending with "file" (Makefile, Dockerfile, etc.)
		`)` +
		`(:\d+(?:[-:]\d+)?)?` // group 4: optional :line, :line:col, or :line-line

	return regexp.MustCompile(pattern)
}

func (l *Linker) Write(p []byte) (n int, err error) {
	l.writeSeq++
	if l.debugFile != nil {
		_, _ = fmt.Fprintf(l.debugFile, "=== Write #%d (%d bytes) ===\n", l.writeSeq, len(p))
		_, _ = fmt.Fprintf(l.debugFile, "Input:  %q\n", p)
	}

	tokens := l.tokenizer.Feed(p)
	var result bytes.Buffer

	for _, tok := range tokens {
		switch tok.Kind {
		case TokenText:
			processed := l.processTextWithState(tok.Data, l.styled, l.inOSC8)
			result.Write(processed)
		case TokenSGR:
			result.Write(tok.Data)
			l.styled = tok.Styled
		case TokenOSC8:
			result.Write(tok.Data)
			l.inOSC8 = !tok.IsEnd
		default:
			result.Write(tok.Data)
		}
	}

	if l.debugFile != nil {
		_, _ = fmt.Fprintf(l.debugFile, "Output: %q\n\n", result.Bytes())
		_ = l.debugFile.Sync()
	}

	_, err = l.output.Write(result.Bytes())
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (l *Linker) Flush() error {
	tokens := l.tokenizer.Flush()
	for _, tok := range tokens {
		if _, err := l.output.Write(tok.Data); err != nil {
			return err
		}
	}
	return nil
}

func (l *Linker) Close() error {
	if err := l.Flush(); err != nil {
		return err
	}
	if l.debugFile != nil {
		return l.debugFile.Close()
	}
	return nil
}

func (l *Linker) processTextWithState(data []byte, styled, inOSC8 bool) []byte {
	if inOSC8 {
		return data
	}

	matches := l.urlPattern.FindAllSubmatchIndex(data, -1)
	if len(matches) == 0 {
		if l.symbolLinks && styled {
			return l.replaceSymbolsStyledSegment(data)
		}
		return data
	}

	var result bytes.Buffer
	last := 0
	for _, m := range matches {
		fullStart, fullEnd := m[0], m[1]
		if fullStart > last {
			segment := data[last:fullStart]
			if l.symbolLinks && styled {
				result.Write(l.replaceSymbolsStyledSegment(segment))
			} else {
				result.Write(segment)
			}
		}

		if start, end, ok := submatch(m, 1); ok {
			wrapped, suffix := l.wrapURL(data[start:end])
			result.Write(wrapped)
			result.Write(suffix)
			last = fullEnd
			continue
		}

		if start, end, ok := submatch(m, 2); ok {
			prefix := data[fullStart:start]
			domainPart := data[start:end]
			result.Write(l.wrapBareDomain(prefix, domainPart))
			last = fullEnd
			continue
		}

		pathStart, pathEnd, ok := submatch(m, 3)
		if !ok {
			result.Write(data[fullStart:fullEnd])
			last = fullEnd
			continue
		}

		pathPart := data[pathStart:pathEnd]
		var locSuffix []byte
		if start, end, ok := submatch(m, 4); ok {
			locSuffix = data[start:end]
		}

		prefix := data[fullStart:pathStart]
		displayText := append(pathPart, locSuffix...)

		if replacement, ok := l.wrapFilePath(prefix, pathPart, locSuffix, displayText); ok {
			result.Write(replacement)
		} else {
			segment := data[fullStart:fullEnd]
			if l.symbolLinks && styled {
				result.Write(l.replaceSymbolsStyledSegment(segment))
			} else {
				result.Write(segment)
			}
		}
		last = fullEnd
	}

	if last < len(data) {
		segment := data[last:]
		if l.symbolLinks && styled {
			result.Write(l.replaceSymbolsStyledSegment(segment))
		} else {
			result.Write(segment)
		}
	}

	return result.Bytes()
}

func (l *Linker) resolvePath(path string) string {
	var absPath string
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		absPath = filepath.Join(home, path[2:])
	} else if filepath.IsAbs(path) {
		absPath = path
	} else {
		absPath = filepath.Join(l.cwd, path)
	}

	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return absPath
	}
	return resolved
}

func (l *Linker) pathExists(path string) bool {
	if exists, ok := l.fileCache[path]; ok {
		return exists
	}
	_, err := os.Stat(path)
	exists := err == nil
	l.fileCache[path] = exists
	return exists
}

func (l *Linker) st() string {
	if l.terminator == "bel" {
		return "\x07"
	}
	return "\x1b\\"
}

func (l *Linker) osc8Link(url string, display []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("\x1b]8;;")
	buf.WriteString(url)
	buf.WriteString(l.st())
	buf.Write(display)
	buf.WriteString("\x1b]8;;")
	buf.WriteString(l.st())
	return buf.Bytes()
}

func (l *Linker) wrapFile(prefix []byte, absPath, locSuffix string, displayText []byte) []byte {
	var buf bytes.Buffer
	buf.Write(prefix)
	buf.Write(l.osc8Link(l.formatFileURL(absPath, locSuffix), displayText))
	return buf.Bytes()
}

func (l *Linker) formatFileURL(absPath, locSuffix string) string {
	if l.scheme == "file" {
		return "file://" + l.hostname + absPath
	}
	return l.scheme + "://file" + absPath + normalizeLocSuffix(locSuffix)
}

func normalizeLocSuffix(s string) string {
	if len(s) == 0 {
		return s
	}
	for i := 1; i < len(s); i++ {
		if s[i] == '-' {
			return s[:i] + ":1"
		}
	}
	return s
}

func (l *Linker) wrapURL(url []byte) ([]byte, []byte) {
	url, suffix := trimURLSuffix(url)
	return l.osc8Link(string(url), url), suffix
}

func trimURLSuffix(url []byte) ([]byte, []byte) {
	if len(url) == 0 || url[len(url)-1] != ')' {
		return url, nil
	}
	if bytes.IndexByte(url, '(') != -1 {
		return url, nil
	}
	return url[:len(url)-1], url[len(url)-1:]
}

func (l *Linker) wrapBareDomain(prefix, domain []byte) []byte {
	var buf bytes.Buffer
	buf.Write(prefix)
	buf.Write(l.osc8Link("https://"+string(domain), domain))
	return buf.Bytes()
}

func (l *Linker) wrapFilePath(prefix, pathPart, locSuffix, displayText []byte) ([]byte, bool) {
	pathStr := string(pathPart)
	absPath := l.resolvePath(pathStr)
	if absPath == "" {
		return nil, false
	}

	if l.pathExists(absPath) {
		return l.wrapFile(prefix, absPath, string(locSuffix), displayText), true
	}

	// Try stripping git diff a/ or b/ prefix
	if stripped, ok := stripGitDiffPrefix(pathStr); ok {
		strippedAbs := l.resolvePath(stripped)
		if strippedAbs != "" && l.pathExists(strippedAbs) {
			return l.wrapFile(prefix, strippedAbs, string(locSuffix), displayText), true
		}
	}

	if !l.resolveBasename {
		return nil, false
	}
	absPath = l.index.Resolve(pathStr)
	if absPath == "" {
		return nil, false
	}
	return l.wrapFile(prefix, absPath, string(locSuffix), displayText), true
}

// stripGitDiffPrefix removes the "a/" or "b/" prefix that git diff adds to file paths.
func stripGitDiffPrefix(path string) (string, bool) {
	if len(path) > 2 && (path[0] == 'a' || path[0] == 'b') && path[1] == '/' {
		return path[2:], true
	}
	return "", false
}

func (l *Linker) StartIndexer(ctx context.Context) {
	if !l.resolveBasename {
		return
	}
	l.index.Start(ctx)
}

func (l *Linker) WaitForIndex(ctx context.Context) error {
	if !l.resolveBasename {
		return nil
	}
	return l.index.Wait(ctx)
}

// replaceSymbolsStyledSegment links identifiers in a styled text segment.
// It tracks dot-separated chains (e.g. "vscode.window.showMessage") so that
// each word's link carries the full qualified name up to that point, helping
// symbol-opener disambiguate common names like "Window".
func (l *Linker) replaceSymbolsStyledSegment(data []byte) []byte {
	var result bytes.Buffer
	// qualifiedName accumulates the dot-separated chain seen so far,
	// e.g. "ProgressLocation" → "ProgressLocation.Window"
	var qualifiedName []byte
	for i := 0; i < len(data); {
		if !isWordChar(data[i]) {
			// A dot between two words continues the qualified chain
			// rather than resetting it, so "Foo.Bar" links Bar as "Foo.Bar".
			if data[i] == '.' && len(qualifiedName) > 0 && i+1 < len(data) && isWordChar(data[i+1]) {
				result.WriteByte('.')
				qualifiedName = append(qualifiedName, '.')
				i++
				continue
			}
			qualifiedName = qualifiedName[:0]
			result.WriteByte(data[i])
			i++
			continue
		}

		start := i
		for i < len(data) && isWordChar(data[i]) {
			i++
		}
		word := data[start:i]
		qualifiedName = append(qualifiedName, word...)

		if len(word) >= 3 {
			isFunction := i < len(data) && data[i] == '('
			// word is the display text; qualifiedName is used in the URL
			result.Write(l.wrapSymbol(nil, word, qualifiedName, isFunction))
		} else {
			result.Write(word)
		}
	}

	return result.Bytes()
}

func isWordChar(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func submatch(match []int, group int) (start, end int, ok bool) {
	// submatch indices are 2 slots per group: [start, end].
	idx := group * 2
	if idx+1 >= len(match) {
		return 0, 0, false
	}
	start, end = match[idx], match[idx+1]
	// Treat empty or missing groups as not present.
	if start == -1 || end == -1 || start == end {
		return start, end, false
	}
	return start, end, true
}

// wrapSymbol wraps display text in an OSC 8 hyperlink pointing to symbol-opener.
// display is the visible text and symbol is used in the URL query parameter.
// They differ for qualified names: for "ProgressLocation.Window", the second
// word is wrapped with display="Window" and symbol="ProgressLocation.Window".
//
// Returns: {prefix}ESC]8;;{scheme}://maaashjp.symbol-opener?symbol={symbol}&cwd={cwd}[&kind=Function]ST{display}ESC]8;;ST
func (l *Linker) wrapSymbol(prefix, display, symbol []byte, isFunction bool) []byte {
	var urlBuf bytes.Buffer
	urlBuf.WriteString(l.scheme)
	urlBuf.WriteString("://maaashjp.symbol-opener?symbol=")
	urlBuf.Write(symbol)
	urlBuf.WriteString("&cwd=")
	urlBuf.WriteString(l.cwd)
	if isFunction {
		urlBuf.WriteString("&kind=Function")
	}

	var buf bytes.Buffer
	buf.Write(prefix)
	buf.Write(l.osc8Link(urlBuf.String(), display))
	return buf.Bytes()
}
