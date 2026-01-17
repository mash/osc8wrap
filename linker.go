package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var combinedPattern = regexp.MustCompile(
	// group 1: https URL (no whitespace, quotes, backticks, or control chars)
	`(https://[^\s<>"'\x60\x00-\x1f\x7f]+)` +
		`|` +
		// file path pattern
		`(?:^|[^/\w.-]|\x1b\[[0-9;]*m)` + // boundary: start of line, non-path char, or ANSI SGR
		`(` + // group 2: path
		`\.{0,2}/[\w./-]+(?:\.\w+)?` + // starts with /, ./, or ../: extension optional
		`|` +
		`[\w./-]+\.\w+` + // no path prefix: extension required
		`|` +
		`\w+file` + // files ending with "file" (Makefile, Dockerfile, etc.)
		`)` +
		`(:\d+(?:[-:]\d+)?)?`, // group 3: optional :line, :line:col, or :line-line
)

var osc8Start = []byte("\x1b]8;;")

type Linker struct {
	output    io.Writer
	cwd       string
	hostname  string
	scheme    string
	fileCache map[string]bool
}

func NewLinker(output io.Writer, cwd, hostname, scheme string) *Linker {
	if scheme == "" {
		scheme = "file"
	}
	return &Linker{
		output:    output,
		cwd:       cwd,
		hostname:  hostname,
		scheme:    scheme,
		fileCache: make(map[string]bool),
	}
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

	return combinedPattern.ReplaceAllFunc(data, func(match []byte) []byte {
		submatch := combinedPattern.FindSubmatch(match)
		if submatch == nil {
			return match
		}

		if len(submatch[1]) > 0 {
			return l.wrapURLWithOSC8(submatch[1])
		}

		fullMatch := submatch[0]
		pathPart := submatch[2]
		locSuffix := submatch[3]

		if len(pathPart) == 0 {
			return match
		}

		absPath := l.resolvePath(string(pathPart))
		if absPath == "" {
			return match
		}

		if !l.fileExists(absPath) {
			return match
		}

		prefix := fullMatch[:bytes.Index(fullMatch, pathPart)]
		displayText := append(pathPart, locSuffix...)

		return l.wrapFileWithOSC8(prefix, absPath, string(locSuffix), displayText)
	})
}

func (l *Linker) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(l.cwd, path)
}

func (l *Linker) fileExists(path string) bool {
	if exists, ok := l.fileCache[path]; ok {
		return exists
	}
	info, err := os.Stat(path)
	exists := err == nil && !info.IsDir()
	l.fileCache[path] = exists
	return exists
}

func (l *Linker) wrapFileWithOSC8(prefix []byte, absPath, locSuffix string, displayText []byte) []byte {
	var buf bytes.Buffer
	buf.Write(prefix)
	buf.WriteString("\x1b]8;;")
	buf.WriteString(l.formatFileURL(absPath, locSuffix))
	buf.WriteByte('\x07')
	buf.Write(displayText)
	buf.WriteString("\x1b]8;;\x07")
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
	buf.WriteByte('\x07')
	buf.Write(url)
	buf.WriteString("\x1b]8;;\x07")
	return buf.Bytes()
}
