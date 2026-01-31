package main

import (
	"bytes"
	"context"
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
	symbolPattern   *regexp.Regexp
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
	if l.symbolLinks {
		l.symbolPattern = l.buildSymbolPattern()
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
	processed := l.convertLine(p)
	_, err = l.output.Write(processed)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (l *Linker) Flush() error {
	return nil
}

func (l *Linker) convertLine(data []byte) []byte {
	if bytes.Contains(data, osc8Start) {
		return data
	}

	result := l.urlPattern.ReplaceAllFunc(data, func(match []byte) []byte {
		submatch := l.urlPattern.FindSubmatch(match)
		if submatch == nil {
			return match
		}

		// group 1: https URL
		if len(submatch[1]) > 0 {
			return l.wrapURLWithOSC8(submatch[1])
		}

		// group 2: bare domain URL
		if len(submatch[2]) > 0 {
			fullMatch := submatch[0]
			domainPart := submatch[2]
			prefix := fullMatch[:bytes.Index(fullMatch, domainPart)]
			return l.wrapBareDomainWithOSC8(prefix, domainPart)
		}

		fullMatch := submatch[0]
		pathPart := submatch[3]  // group 3: path
		locSuffix := submatch[4] // group 4: loc suffix

		if len(pathPart) == 0 {
			return match
		}

		absPath := l.resolvePath(string(pathPart))
		if absPath == "" {
			return match
		}

		if !l.pathExists(absPath) {
			if !l.resolveBasename {
				return match
			}
			absPath = l.index.Resolve(string(pathPart))
			if absPath == "" {
				return match
			}
		}

		prefix := fullMatch[:bytes.Index(fullMatch, pathPart)]
		displayText := append(pathPart, locSuffix...)

		return l.wrapFileWithOSC8(prefix, absPath, string(locSuffix), displayText)
	})

	if l.symbolLinks {
		result = l.convertSymbols(result)
	}

	return result
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

func (l *Linker) wrapFileWithOSC8(prefix []byte, absPath, locSuffix string, displayText []byte) []byte {
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

func (l *Linker) wrapURLWithOSC8(url []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("\x1b]8;;")
	buf.Write(url)
	buf.WriteString(l.st())
	buf.Write(url)
	buf.WriteString("\x1b]8;;")
	buf.WriteString(l.st())
	return buf.Bytes()
}

func (l *Linker) wrapBareDomainWithOSC8(prefix, domain []byte) []byte {
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

func (l *Linker) buildSymbolPattern() *regexp.Regexp {
	// symbol pattern: matches PascalCase or camelCase identifiers
	pattern := `` +
		`(?:^|[^\w]|\x1b\[[0-9;]*m)` + // boundary: start, non-word char, or ANSI SGR
		`(` + // group 1: symbol name
		`[A-Z][a-zA-Z0-9]*[a-z][a-zA-Z0-9]*` + // PascalCase: requires lowercase to exclude ALL_CAPS
		`|` +
		`[a-z][a-z0-9]*[A-Z][a-zA-Z0-9]*` + // camelCase: requires uppercase to exclude lowercase words
		`)` +
		`(\(\))?` // group 2: optional () to distinguish function calls

	return regexp.MustCompile(pattern)
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
	if disallowedEscapePattern.Match(data) {
		return data
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
	return l.symbolPattern.ReplaceAllFunc(data, func(match []byte) []byte {
		submatch := l.symbolPattern.FindSubmatch(match)
		if submatch == nil {
			return match
		}

		symbolName := submatch[1]
		hasParens := len(submatch[2]) > 0

		prefix := match[:bytes.Index(match, symbolName)]
		return l.wrapSymbolWithOSC8(prefix, symbolName, hasParens)
	})
}

func (l *Linker) wrapSymbolWithOSC8(prefix, symbol []byte, isFunction bool) []byte {
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
	if isFunction {
		buf.WriteString("()")
	}
	buf.WriteString("\x1b]8;;")
	buf.WriteString(l.st())
	return buf.Bytes()
}
