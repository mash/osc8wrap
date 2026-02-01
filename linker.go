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

var osc8Start = []byte("\x1b]8;;")

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
}

func NewLinker(output io.Writer, cwd, hostname, scheme string, domains []string) *Linker {
	return NewLinkerWithOptions(LinkerOptions{
		Output:          output,
		Cwd:             cwd,
		Hostname:        hostname,
		Scheme:          scheme,
		Domains:         domains,
		ResolveBasename: false,
	})
}

func NewLinkerWithOptions(opts LinkerOptions) *Linker {
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
		`(?:^|[^/\w.-]|\x1b\[[0-9;]*m)` + // boundary: start of line, non-path char, or ANSI SGR
		`(` + // group 3: path
		`(?:~|\.{0,2})/[\w./-]+(?:\.\w+)?` + // starts with ~/, /, ./, or ../: extension optional
		`|` +
		`[\w./-]+\.\w+` + // no path prefix: extension required
		`|` +
		`\w+file` + // files ending with "file" (Makefile, Dockerfile, etc.)
		`)` +
		`(:\d+(?:[-:]\d+)?)?` // group 4: optional :line, :line:col, or :line-line

	return regexp.MustCompile(pattern)
}

func (l *Linker) Write(p []byte) (n int, err error) {
	l.writeSeq++
	if l.debugFile != nil {
		fmt.Fprintf(l.debugFile, "=== Write #%d (%d bytes) ===\n", l.writeSeq, len(p))
		fmt.Fprintf(l.debugFile, "Input:  %q\n", p)
	}

	processed := l.convertLine(p)

	if l.debugFile != nil {
		fmt.Fprintf(l.debugFile, "Output: %q\n\n", processed)
		l.debugFile.Sync()
	}

	_, err = l.output.Write(processed)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (l *Linker) Flush() error {
	return nil
}

func (l *Linker) Close() error {
	if l.debugFile != nil {
		return l.debugFile.Close()
	}
	return nil
}

func (l *Linker) convertLine(data []byte) []byte {
	if bytes.Contains(data, osc8Start) {
		return data
	}

	matches := l.urlPattern.FindAllSubmatchIndex(data, -1)
	if len(matches) == 0 {
		if l.symbolLinks {
			return l.convertSymbols(data)
		}
		return data
	}

	var result bytes.Buffer
	last := 0
	for _, m := range matches {
		fullStart, fullEnd := m[0], m[1]
		if fullStart > last {
			result.Write(data[last:fullStart])
		}

		// group 1: https URL
		if start, end, ok := submatch(m, 1); ok {
			result.Write(l.wrapURL(data[start:end]))
			last = fullEnd
			continue
		}

		// group 2: bare domain URL
		if start, end, ok := submatch(m, 2); ok {
			prefix := data[fullStart:start]
			domainPart := data[start:end]
			result.Write(l.wrapBareDomain(prefix, domainPart))
			last = fullEnd
			continue
		}

		// group 3: file path
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
			result.Write(data[fullStart:fullEnd])
		}
		last = fullEnd
	}

	if last < len(data) {
		result.Write(data[last:])
	}

	output := result.Bytes()
	if l.symbolLinks {
		output = l.convertSymbols(output)
	}

	return output
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

func (l *Linker) wrapFile(prefix []byte, absPath, locSuffix string, displayText []byte) []byte {
	var buf bytes.Buffer
	buf.Write(prefix)
	buf.WriteString("\x1b]8;;")
	buf.WriteString(l.formatFileURL(absPath, locSuffix))
	buf.WriteString(l.st())
	buf.Write(displayText)
	buf.WriteString("\x1b]8;;")
	buf.WriteString(l.st())
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

func (l *Linker) wrapURL(url []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("\x1b]8;;")
	buf.Write(url)
	buf.WriteString(l.st())
	buf.Write(url)
	buf.WriteString("\x1b]8;;")
	buf.WriteString(l.st())
	return buf.Bytes()
}

func (l *Linker) wrapBareDomain(prefix, domain []byte) []byte {
	var buf bytes.Buffer
	buf.Write(prefix)
	buf.WriteString("\x1b]8;;https://")
	buf.Write(domain)
	buf.WriteString(l.st())
	buf.Write(domain)
	buf.WriteString("\x1b]8;;")
	buf.WriteString(l.st())
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

// disallowedEscapePattern matches escape sequences that should skip symbol linking.
// Only SGR (\x1b[...m) and OSC8 (\x1b]8;;) are allowed to overlap with symbol linking.
var disallowedEscapePattern = regexp.MustCompile(`` +
	// CSI not ending with 'm': cursor control, screen manipulation, etc.
	// Final byte 'm' (0x6D) is excluded to allow SGR sequences
	`\x1b\[[0-9;?<>=]*[@A-Za-lo-z\[\]^_` + "`" + `{|}~\\]` +
	`|` +
	// OSC 0-7 and 9: window title, clipboard, notifications, etc.
	`\x1b\][0-79]` +
	`|` +
	// DCS (\x1bP), APC (\x1b_), PM (\x1b^): device control strings
	`\x1b[P_^]`,
)

func (l *Linker) convertSymbols(data []byte) []byte {
	if bytes.IndexByte(data, 0x1b) != -1 {
		if disallowedEscapePattern.Match(data) {
			return data
		}
	}

	var result bytes.Buffer
	remaining := data

	for len(remaining) > 0 {
		startIdx := bytes.Index(remaining, osc8Start)
		if startIdx == -1 {
			result.Write(l.replaceSymbols(remaining))
			break
		}

		result.Write(l.replaceSymbols(remaining[:startIdx]))

		endIdx := bytes.Index(remaining[startIdx:], []byte("\x1b]8;;\x1b\\"))
		if endIdx == -1 {
			endIdx = bytes.Index(remaining[startIdx:], []byte("\x1b]8;;\x07"))
		}
		if endIdx == -1 {
			result.Write(remaining[startIdx:])
			break
		}

		linkEnd := startIdx + endIdx + len("\x1b]8;;\x1b\\")
		result.Write(remaining[startIdx:linkEnd])
		remaining = remaining[linkEnd:]
	}

	return result.Bytes()
}

func (l *Linker) replaceSymbols(data []byte) []byte {
	var result bytes.Buffer
	styled := false
	scanTokens(data, func(tok token, next int) {
		if tok.kind == tokenText {
			segment := data[tok.start:tok.end]
			if styled {
				result.Write(l.replaceSymbolsStyledSegment(segment))
			} else {
				result.Write(segment)
			}
			return
		}

		result.Write(data[tok.start:tok.end])
		styled = tok.styled
	})

	return result.Bytes()
}

func (l *Linker) replaceSymbolsStyledSegment(data []byte) []byte {
	var result bytes.Buffer
	for i := 0; i < len(data); {
		if !isWordChar(data[i]) {
			result.WriteByte(data[i])
			i++
			continue
		}

		start := i
		for i < len(data) && isWordChar(data[i]) {
			i++
		}
		word := data[start:i]

		if len(word) >= 3 {
			isFunction := i < len(data) && data[i] == '('
			result.Write(l.wrapSymbol(nil, word, isFunction))
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

func (l *Linker) wrapSymbol(prefix, symbol []byte, isFunction bool) []byte {
	var buf bytes.Buffer
	buf.Write(prefix)
	buf.WriteString("\x1b]8;;")
	buf.WriteString(l.scheme)
	buf.WriteString("://mash.symbol-opener?symbol=")
	buf.Write(symbol)
	buf.WriteString("&cwd=")
	buf.WriteString(l.cwd)
	if isFunction {
		buf.WriteString("&kind=Function")
	}
	buf.WriteString(l.st())
	buf.Write(symbol)
	buf.WriteString("\x1b]8;;")
	buf.WriteString(l.st())
	return buf.Bytes()
}
