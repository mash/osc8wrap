package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var osc8Start = []byte("\x1b]8;;")

type FileInfo struct {
	path  string
	mtime time.Time
}

type FileIndex struct {
	mu    sync.RWMutex
	ready bool
	files map[string][]FileInfo
}

type LinkerOptions struct {
	Output          io.Writer
	Cwd             string
	Hostname        string
	Scheme          string
	Domains         []string
	ResolveBasename bool
	ExcludeDirs     []string
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
	indexReady      chan struct{}
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
	l := &Linker{
		output:          opts.Output,
		cwd:             opts.Cwd,
		hostname:        opts.Hostname,
		scheme:          scheme,
		fileCache:       make(map[string]bool),
		domains:         opts.Domains,
		resolveBasename: opts.ResolveBasename,
		excludeDirs:     opts.ExcludeDirs,
		index: &FileIndex{
			files: make(map[string][]FileInfo),
		},
		indexReady: make(chan struct{}),
	}
	l.urlPattern = l.buildPattern()
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
		`\.{0,2}/[\w./-]+(?:\.\w+)?` + // starts with /, ./, or ../: extension optional
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

	return l.urlPattern.ReplaceAllFunc(data, func(match []byte) []byte {
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

		if !l.fileExists(absPath) {
			absPath = l.resolveViaIndex(string(pathPart))
			if absPath == "" {
				return match
			}
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

func (l *Linker) wrapBareDomainWithOSC8(prefix, domain []byte) []byte {
	var buf bytes.Buffer
	buf.Write(prefix)
	buf.WriteString("\x1b]8;;https://")
	buf.Write(domain)
	buf.WriteByte('\x07')
	buf.Write(domain)
	buf.WriteString("\x1b]8;;\x07")
	return buf.Bytes()
}

func (l *Linker) StartIndexer(ctx context.Context) {
	if !l.resolveBasename {
		close(l.indexReady)
		return
	}

	gitDir := filepath.Join(l.cwd, ".git")
	if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
		l.buildIndexFromGit(ctx)
	} else {
		l.buildIndexFromFilesystem(ctx)
	}

	l.index.mu.Lock()
	l.index.ready = true
	l.index.mu.Unlock()
	close(l.indexReady)
}

func (l *Linker) buildIndexFromGit(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "git", "ls-files")
	cmd.Dir = l.cwd
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			cmd.Process.Kill()
			return
		default:
		}

		relPath := scanner.Text()
		absPath := filepath.Join(l.cwd, relPath)
		info, err := os.Stat(absPath)
		if err != nil || info.IsDir() {
			continue
		}

		basename := filepath.Base(relPath)
		l.index.mu.Lock()
		l.index.files[basename] = append(l.index.files[basename], FileInfo{
			path:  absPath,
			mtime: info.ModTime(),
		})
		l.index.mu.Unlock()
	}

	cmd.Wait()
}

func (l *Linker) buildIndexFromFilesystem(ctx context.Context) {
	excludeSet := make(map[string]bool)
	for _, d := range l.excludeDirs {
		excludeSet[d] = true
	}

	filepath.WalkDir(l.cwd, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return filepath.SkipAll
		default:
		}

		if d.IsDir() {
			if excludeSet[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		basename := filepath.Base(path)
		l.index.mu.Lock()
		l.index.files[basename] = append(l.index.files[basename], FileInfo{
			path:  path,
			mtime: info.ModTime(),
		})
		l.index.mu.Unlock()

		return nil
	})
}

func (l *Linker) WaitForIndex(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.indexReady:
		return nil
	}
}

func (l *Linker) resolveViaIndex(path string) string {
	if !l.resolveBasename {
		return ""
	}

	l.index.mu.RLock()
	ready := l.index.ready
	l.index.mu.RUnlock()
	if !ready {
		return ""
	}

	basename := filepath.Base(path)
	l.index.mu.RLock()
	candidates := l.index.files[basename]
	l.index.mu.RUnlock()

	if len(candidates) == 0 {
		return ""
	}

	if strings.Contains(path, "/") {
		var filtered []FileInfo
		for _, c := range candidates {
			if strings.HasSuffix(c.path, "/"+path) {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) > 0 {
			candidates = filtered
		}
	}

	if len(candidates) == 1 {
		return candidates[0].path
	}

	var newest FileInfo
	for _, c := range candidates {
		if c.mtime.After(newest.mtime) {
			newest = c
		}
	}
	return newest.path
}
