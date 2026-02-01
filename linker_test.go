package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLinker_Write(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.go")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	makefile := filepath.Join(tmpDir, "Makefile")
	if err := os.WriteFile(makefile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	testFile, _ = filepath.EvalSymlinks(testFile)
	makefile, _ = filepath.EvalSymlinks(makefile)

	hostname := "testhost"

	tests := []struct {
		name     string
		input    string
		cwd      string
		expected string
	}{
		{
			name:     "absolute path",
			input:    "error in " + testFile + "\n",
			cwd:      tmpDir,
			expected: "error in \x1b]8;;file://testhost" + testFile + "\x1b\\" + testFile + "\x1b]8;;\x1b\\\n",
		},
		{
			name:     "absolute path with line number",
			input:    "error in " + testFile + ":42\n",
			cwd:      tmpDir,
			expected: "error in \x1b]8;;file://testhost" + testFile + "\x1b\\" + testFile + ":42\x1b]8;;\x1b\\\n",
		},
		{
			name:     "absolute path with line and column",
			input:    "error in " + testFile + ":42:10\n",
			cwd:      tmpDir,
			expected: "error in \x1b]8;;file://testhost" + testFile + "\x1b\\" + testFile + ":42:10\x1b]8;;\x1b\\\n",
		},
		{
			name:     "relative path",
			input:    "error in ./test.go:10\n",
			cwd:      tmpDir,
			expected: "error in \x1b]8;;file://testhost" + testFile + "\x1b\\./test.go:10\x1b]8;;\x1b\\\n",
		},
		{
			name:     "non-existent file not linked",
			input:    "error in /nonexistent/file.go:10\n",
			cwd:      tmpDir,
			expected: "error in /nonexistent/file.go:10\n",
		},
		{
			name:     "multiple paths on same line",
			input:    testFile + " and " + testFile + "\n",
			cwd:      tmpDir,
			expected: "\x1b]8;;file://testhost" + testFile + "\x1b\\" + testFile + "\x1b]8;;\x1b\\ and \x1b]8;;file://testhost" + testFile + "\x1b\\" + testFile + "\x1b]8;;\x1b\\\n",
		},
		{
			name:     "no paths",
			input:    "just some text\n",
			cwd:      tmpDir,
			expected: "just some text\n",
		},
		{
			name:     "colored path with ANSI escape",
			input:    "file: \x1b[32m" + testFile + "\x1b[0m\n",
			cwd:      tmpDir,
			expected: "file: \x1b[32m\x1b]8;;file://testhost" + testFile + "\x1b\\" + testFile + "\x1b]8;;\x1b\\\x1b[0m\n",
		},
		{
			name:     "already has OSC-8 link",
			input:    "file: \x1b]8;;file://testhost" + testFile + "\x1b\\test.go\x1b]8;;\x1b\\\n",
			cwd:      tmpDir,
			expected: "file: \x1b]8;;file://testhost" + testFile + "\x1b\\test.go\x1b]8;;\x1b\\\n",
		},
		{
			name:     "https URL",
			input:    "see https://example.com/path for details\n",
			cwd:      tmpDir,
			expected: "see \x1b]8;;https://example.com/path\x1b\\https://example.com/path\x1b]8;;\x1b\\ for details\n",
		},
		{
			name:     "https URL with query params",
			input:    "see https://example.com/path?foo=bar&baz=qux for details\n",
			cwd:      tmpDir,
			expected: "see \x1b]8;;https://example.com/path?foo=bar&baz=qux\x1b\\https://example.com/path?foo=bar&baz=qux\x1b]8;;\x1b\\ for details\n",
		},
		{
			name:     "mixed file path and URL on same line",
			input:    testFile + " see https://example.com/docs\n",
			cwd:      tmpDir,
			expected: "\x1b]8;;file://testhost" + testFile + "\x1b\\" + testFile + "\x1b]8;;\x1b\\ see \x1b]8;;https://example.com/docs\x1b\\https://example.com/docs\x1b]8;;\x1b\\\n",
		},
		{
			name:     "extensionless file with absolute path",
			input:    "error in " + makefile + "\n",
			cwd:      tmpDir,
			expected: "error in \x1b]8;;file://testhost" + makefile + "\x1b\\" + makefile + "\x1b]8;;\x1b\\\n",
		},
		{
			name:     "extensionless file with relative path",
			input:    "error in ./Makefile\n",
			cwd:      tmpDir,
			expected: "error in \x1b]8;;file://testhost" + makefile + "\x1b\\./Makefile\x1b]8;;\x1b\\\n",
		},
		{
			name:     "known extensionless file without path prefix",
			input:    "edit Makefile please\n",
			cwd:      tmpDir,
			expected: "edit \x1b]8;;file://testhost" + makefile + "\x1b\\Makefile\x1b]8;;\x1b\\ please\n",
		},
		{
			name:     "unknown extensionless file without path prefix not linked",
			input:    "edit UNKNOWN please\n",
			cwd:      tmpDir,
			expected: "edit UNKNOWN please\n",
		},
		{
			name:     "git diff a/ prefix stripped",
			input:    "--- a/test.go\n",
			cwd:      tmpDir,
			expected: "--- \x1b]8;;file://testhost" + testFile + "\x1b\\a/test.go\x1b]8;;\x1b\\\n",
		},
		{
			name:     "git diff b/ prefix stripped",
			input:    "+++ b/test.go\n",
			cwd:      tmpDir,
			expected: "+++ \x1b]8;;file://testhost" + testFile + "\x1b\\b/test.go\x1b]8;;\x1b\\\n",
		},
		{
			name:     "non-existent git diff path not linked",
			input:    "--- a/nonexistent.go\n",
			cwd:      tmpDir,
			expected: "--- a/nonexistent.go\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			linker := NewLinker(&buf, tt.cwd, hostname, "file", []string{"github.com"})

			_, err := linker.Write([]byte(tt.input))
			if err != nil {
				t.Fatal(err)
			}

			if err := linker.Flush(); err != nil {
				t.Fatal(err)
			}

			if got := buf.String(); got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestLinker_OutputsImmediately(t *testing.T) {
	tmpDir := t.TempDir()
	hostname := "testhost"

	var buf bytes.Buffer
	linker := NewLinker(&buf, tmpDir, hostname, "file", []string{"github.com"})

	linker.Write([]byte("first line\nsecond "))
	if got := buf.String(); got != "first line\nsecond " {
		t.Errorf("after first write: got %q, want %q", got, "first line\nsecond ")
	}

	buf.Reset()
	linker.Write([]byte("line\n"))
	if got := buf.String(); got != "line\n" {
		t.Errorf("after second write: got %q, want %q", got, "line\n")
	}
}

func TestLinker_OutputsWithoutNewline(t *testing.T) {
	tmpDir := t.TempDir()
	hostname := "testhost"

	var buf bytes.Buffer
	linker := NewLinker(&buf, tmpDir, hostname, "file", []string{"github.com"})

	linker.Write([]byte("no newline"))
	if got := buf.String(); got != "no newline" {
		t.Errorf("got %q, want %q", got, "no newline")
	}
}

func TestLinker_Schemes(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.go")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	testFile, _ = filepath.EvalSymlinks(testFile)

	hostname := "testhost"

	tests := []struct {
		name     string
		scheme   string
		input    string
		expected string
	}{
		{
			name:     "vscode scheme with line and column",
			scheme:   "vscode",
			input:    testFile + ":42:10\n",
			expected: "\x1b]8;;vscode://file" + testFile + ":42:10\x1b\\" + testFile + ":42:10\x1b]8;;\x1b\\\n",
		},
		{
			name:     "vscode scheme with line only",
			scheme:   "vscode",
			input:    testFile + ":42\n",
			expected: "\x1b]8;;vscode://file" + testFile + ":42\x1b\\" + testFile + ":42\x1b]8;;\x1b\\\n",
		},
		{
			name:     "vscode scheme without line",
			scheme:   "vscode",
			input:    testFile + "\n",
			expected: "\x1b]8;;vscode://file" + testFile + "\x1b\\" + testFile + "\x1b]8;;\x1b\\\n",
		},
		{
			name:     "cursor scheme",
			scheme:   "cursor",
			input:    testFile + ":10:5\n",
			expected: "\x1b]8;;cursor://file" + testFile + ":10:5\x1b\\" + testFile + ":10:5\x1b]8;;\x1b\\\n",
		},
		{
			name:     "custom scheme",
			scheme:   "myeditor",
			input:    testFile + ":1\n",
			expected: "\x1b]8;;myeditor://file" + testFile + ":1\x1b\\" + testFile + ":1\x1b]8;;\x1b\\\n",
		},
		{
			name:     "cursor scheme with range format converts to line:col",
			scheme:   "cursor",
			input:    testFile + ":12-12\n",
			expected: "\x1b]8;;cursor://file" + testFile + ":12:1\x1b\\" + testFile + ":12-12\x1b]8;;\x1b\\\n",
		},
		{
			name:     "cursor scheme with line range converts to start line",
			scheme:   "cursor",
			input:    testFile + ":12-24\n",
			expected: "\x1b]8;;cursor://file" + testFile + ":12:1\x1b\\" + testFile + ":12-24\x1b]8;;\x1b\\\n",
		},
		{
			name:     "empty scheme defaults to file",
			scheme:   "",
			input:    testFile + ":42\n",
			expected: "\x1b]8;;file://" + hostname + testFile + "\x1b\\" + testFile + ":42\x1b]8;;\x1b\\\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			linker := NewLinker(&buf, tmpDir, hostname, tt.scheme, []string{"github.com"})

			_, err := linker.Write([]byte(tt.input))
			if err != nil {
				t.Fatal(err)
			}

			if got := buf.String(); got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestLinker_BareDomains(t *testing.T) {
	tmpDir := t.TempDir()
	hostname := "testhost"

	tests := []struct {
		name     string
		domains  []string
		input    string
		expected string
	}{
		{
			name:     "https github.com unchanged",
			domains:  []string{"github.com"},
			input:    "https://github.com/mash/osc8wrap",
			expected: "\x1b]8;;https://github.com/mash/osc8wrap\x1b\\https://github.com/mash/osc8wrap\x1b]8;;\x1b\\",
		},
		{
			name:     "bare github.com",
			domains:  []string{"github.com"},
			input:    "github.com/mash/osc8wrap",
			expected: "\x1b]8;;https://github.com/mash/osc8wrap\x1b\\github.com/mash/osc8wrap\x1b]8;;\x1b\\",
		},
		{
			name:     "bare github.com with path",
			domains:  []string{"github.com"},
			input:    "see github.com/user/repo/issues/123",
			expected: "see \x1b]8;;https://github.com/user/repo/issues/123\x1b\\github.com/user/repo/issues/123\x1b]8;;\x1b\\",
		},
		{
			name:     "mixed https and bare",
			domains:  []string{"github.com"},
			input:    "https://github.com/a and github.com/b",
			expected: "\x1b]8;;https://github.com/a\x1b\\https://github.com/a\x1b]8;;\x1b\\ and \x1b]8;;https://github.com/b\x1b\\github.com/b\x1b]8;;\x1b\\",
		},
		{
			name:     "custom domain gitlab.com",
			domains:  []string{"gitlab.com"},
			input:    "gitlab.com/user/repo",
			expected: "\x1b]8;;https://gitlab.com/user/repo\x1b\\gitlab.com/user/repo\x1b]8;;\x1b\\",
		},
		{
			name:     "multiple domains",
			domains:  []string{"github.com", "gitlab.com"},
			input:    "github.com/a and gitlab.com/b",
			expected: "\x1b]8;;https://github.com/a\x1b\\github.com/a\x1b]8;;\x1b\\ and \x1b]8;;https://gitlab.com/b\x1b\\gitlab.com/b\x1b]8;;\x1b\\",
		},
		{
			name:     "unlisted domain not linked",
			domains:  []string{"github.com"},
			input:    "gitlab.com/user/repo",
			expected: "gitlab.com/user/repo",
		},
		{
			name:     "empty domains disables bare linking",
			domains:  nil,
			input:    "github.com/user/repo",
			expected: "github.com/user/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			linker := NewLinker(&buf, tmpDir, hostname, "file", tt.domains)

			_, err := linker.Write([]byte(tt.input))
			if err != nil {
				t.Fatal(err)
			}

			if got := buf.String(); got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestLinker_BasenameResolution(t *testing.T) {
	tmpDir := t.TempDir()
	hostname := "testhost"

	srcDir := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(srcDir, "main.go")
	if err := os.WriteFile(testFile, []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	testFile, _ = filepath.EvalSymlinks(testFile)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "basename only resolves via index",
			input:    "error in main.go:10\n",
			expected: "error in \x1b]8;;file://testhost" + testFile + "\x1b\\main.go:10\x1b]8;;\x1b\\\n",
		},
		{
			name:     "relative path still works",
			input:    "error in ./src/main.go:10\n",
			expected: "error in \x1b]8;;file://testhost" + testFile + "\x1b\\./src/main.go:10\x1b]8;;\x1b\\\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			linker := NewLinkerWithOptions(LinkerOptions{
				Output:          &buf,
				Cwd:             tmpDir,
				Hostname:        hostname,
				Scheme:          "file",
				Domains:         []string{"github.com"},
				ResolveBasename: true,
				ExcludeDirs:     []string{},
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go linker.StartIndexer(ctx)
			if err := linker.WaitForIndex(ctx); err != nil {
				t.Fatal(err)
			}

			_, err := linker.Write([]byte(tt.input))
			if err != nil {
				t.Fatal(err)
			}

			if got := buf.String(); got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestLinker_SuffixMatch(t *testing.T) {
	tmpDir := t.TempDir()
	hostname := "testhost"

	deepDir := filepath.Join(tmpDir, "path", "to")
	if err := os.MkdirAll(deepDir, 0755); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(deepDir, "file.go")
	if err := os.WriteFile(testFile, []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	linker := NewLinkerWithOptions(LinkerOptions{
		Output:          &buf,
		Cwd:             tmpDir,
		Hostname:        hostname,
		Scheme:          "file",
		Domains:         []string{"github.com"},
		ResolveBasename: true,
		ExcludeDirs:     []string{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go linker.StartIndexer(ctx)
	if err := linker.WaitForIndex(ctx); err != nil {
		t.Fatal(err)
	}

	input := "error in to/file.go:10\n"
	expected := "error in \x1b]8;;file://testhost" + testFile + "\x1b\\to/file.go:10\x1b]8;;\x1b\\\n"

	_, err := linker.Write([]byte(input))
	if err != nil {
		t.Fatal(err)
	}

	if got := buf.String(); got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestLinker_MtimePriority(t *testing.T) {
	tmpDir := t.TempDir()
	hostname := "testhost"

	dirA := filepath.Join(tmpDir, "a")
	dirB := filepath.Join(tmpDir, "b")
	if err := os.MkdirAll(dirA, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0755); err != nil {
		t.Fatal(err)
	}

	oldFile := filepath.Join(dirA, "file.go")
	newFile := filepath.Join(dirB, "file.go")
	if err := os.WriteFile(oldFile, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	linker := NewLinkerWithOptions(LinkerOptions{
		Output:          &buf,
		Cwd:             tmpDir,
		Hostname:        hostname,
		Scheme:          "file",
		Domains:         []string{"github.com"},
		ResolveBasename: true,
		ExcludeDirs:     []string{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go linker.StartIndexer(ctx)
	if err := linker.WaitForIndex(ctx); err != nil {
		t.Fatal(err)
	}

	input := "error in file.go:10\n"
	expected := "error in \x1b]8;;file://testhost" + newFile + "\x1b\\file.go:10\x1b]8;;\x1b\\\n"

	_, err := linker.Write([]byte(input))
	if err != nil {
		t.Fatal(err)
	}

	if got := buf.String(); got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestLinker_IndexNotReady(t *testing.T) {
	tmpDir := t.TempDir()
	hostname := "testhost"

	srcDir := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	linker := NewLinkerWithOptions(LinkerOptions{
		Output:          &buf,
		Cwd:             tmpDir,
		Hostname:        hostname,
		Scheme:          "file",
		Domains:         []string{"github.com"},
		ResolveBasename: true,
		ExcludeDirs:     []string{},
	})

	input := "error in main.go:10\n"
	expected := "error in main.go:10\n"

	_, err := linker.Write([]byte(input))
	if err != nil {
		t.Fatal(err)
	}

	if got := buf.String(); got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestLinker_ResolveBasenameDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	hostname := "testhost"

	srcDir := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	linker := NewLinkerWithOptions(LinkerOptions{
		Output:          &buf,
		Cwd:             tmpDir,
		Hostname:        hostname,
		Scheme:          "file",
		Domains:         []string{"github.com"},
		ResolveBasename: false,
		ExcludeDirs:     []string{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go linker.StartIndexer(ctx)
	if err := linker.WaitForIndex(ctx); err != nil {
		t.Fatal(err)
	}

	input := "error in main.go:10\n"
	expected := "error in main.go:10\n"

	_, err := linker.Write([]byte(input))
	if err != nil {
		t.Fatal(err)
	}

	if got := buf.String(); got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestLinker_TildePath(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	testDir := filepath.Join(homeDir, ".osc8wrap-test")
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testDir)

	testFile := filepath.Join(testDir, "test.go")
	if err := os.WriteFile(testFile, []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	hostname := "testhost"
	tmpDir := t.TempDir()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "tilde path basic",
			input:    "error in ~/.osc8wrap-test/test.go\n",
			expected: "error in \x1b]8;;file://testhost" + testFile + "\x1b\\~/.osc8wrap-test/test.go\x1b]8;;\x1b\\\n",
		},
		{
			name:     "tilde path with line number",
			input:    "error in ~/.osc8wrap-test/test.go:42\n",
			expected: "error in \x1b]8;;file://testhost" + testFile + "\x1b\\~/.osc8wrap-test/test.go:42\x1b]8;;\x1b\\\n",
		},
		{
			name:     "non-existent tilde path not linked",
			input:    "error in ~/.osc8wrap-test/nonexistent.go:10\n",
			expected: "error in ~/.osc8wrap-test/nonexistent.go:10\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			linker := NewLinker(&buf, tmpDir, hostname, "file", []string{"github.com"})

			_, err := linker.Write([]byte(tt.input))
			if err != nil {
				t.Fatal(err)
			}

			if got := buf.String(); got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestLinker_FsnotifyNewFile(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	hostname := "testhost"

	var buf bytes.Buffer
	linker := NewLinkerWithOptions(LinkerOptions{
		Output:          &buf,
		Cwd:             tmpDir,
		Hostname:        hostname,
		Scheme:          "file",
		Domains:         []string{"github.com"},
		ResolveBasename: true,
		ExcludeDirs:     []string{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go linker.StartIndexer(ctx)
	if err := linker.WaitForIndex(ctx); err != nil {
		t.Fatal(err)
	}

	newFile := filepath.Join(tmpDir, "newfile.go")
	if err := os.WriteFile(newFile, []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	input := "error in newfile.go:10\n"
	expected := "error in \x1b]8;;file://testhost" + newFile + "\x1b\\newfile.go:10\x1b]8;;\x1b\\\n"

	_, err := linker.Write([]byte(input))
	if err != nil {
		t.Fatal(err)
	}

	if got := buf.String(); got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestLinker_Terminator(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.go")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	testFile, _ = filepath.EvalSymlinks(testFile)

	hostname := "testhost"

	tests := []struct {
		name       string
		terminator string
		expected   string
	}{
		{
			name:       "default (st) uses ESC backslash",
			terminator: "",
			expected:   "error in \x1b]8;;file://testhost" + testFile + "\x1b\\" + testFile + "\x1b]8;;\x1b\\\n",
		},
		{
			name:       "explicit st uses ESC backslash",
			terminator: "st",
			expected:   "error in \x1b]8;;file://testhost" + testFile + "\x1b\\" + testFile + "\x1b]8;;\x1b\\\n",
		},
		{
			name:       "bel uses BEL character",
			terminator: "bel",
			expected:   "error in \x1b]8;;file://testhost" + testFile + "\x07" + testFile + "\x1b]8;;\x07\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			linker := NewLinkerWithOptions(LinkerOptions{
				Output:     &buf,
				Cwd:        tmpDir,
				Hostname:   hostname,
				Scheme:     "file",
				Domains:    []string{"github.com"},
				Terminator: tt.terminator,
			})

			input := "error in " + testFile + "\n"
			_, err := linker.Write([]byte(input))
			if err != nil {
				t.Fatal(err)
			}

			if got := buf.String(); got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestLinker_SymbolLinks(t *testing.T) {
	tmpDir := t.TempDir()
	hostname := "testhost"

	tests := []struct {
		name        string
		input       string
		symbolLinks bool
		expected    string
	}{
		{
			name:        "PascalCase symbol",
			input:       "undefined: \x1b[31mNewLinker\x1b[0m\n",
			symbolLinks: true,
			expected:    "undefined: \x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=NewLinker&cwd=" + tmpDir + "\x1b\\NewLinker\x1b]8;;\x1b\\\x1b[0m\n",
		},
		{
			name:        "camelCase symbol",
			input:       "undefined: \x1b[31mgetUserName\x1b[0m\n",
			symbolLinks: true,
			expected:    "undefined: \x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=getUserName&cwd=" + tmpDir + "\x1b\\getUserName\x1b]8;;\x1b\\\x1b[0m\n",
		},
		{
			name:        "function call with parens",
			input:       "undefined: \x1b[31mNewLinker()\x1b[0m\n",
			symbolLinks: true,
			expected:    "undefined: \x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=NewLinker&cwd=" + tmpDir + "&kind=Function\x1b\\NewLinker\x1b]8;;\x1b\\()\x1b[0m\n",
		},
		{
			name:        "multiple symbols",
			input:       "\x1b[31mNewLinker\x1b[0m calls \x1b[32mGetUser\x1b[0m\n",
			symbolLinks: true,
			expected:    "\x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=NewLinker&cwd=" + tmpDir + "\x1b\\NewLinker\x1b]8;;\x1b\\\x1b[0m calls \x1b[32m\x1b]8;;cursor://mash.symbol-opener?symbol=GetUser&cwd=" + tmpDir + "\x1b\\GetUser\x1b]8;;\x1b\\\x1b[0m\n",
		},
		{
			name:        "short identifiers not linked",
			input:       "\x1b[31mID\x1b[0m and \x1b[31mDB\x1b[0m\n",
			symbolLinks: true,
			expected:    "\x1b[31mID\x1b[0m and \x1b[31mDB\x1b[0m\n",
		},
		{
			name:        "lowercase words linked",
			input:       "\x1b[31merror\x1b[0m in \x1b[31mfunction\x1b[0m\n",
			symbolLinks: true,
			expected:    "\x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=error&cwd=" + tmpDir + "\x1b\\error\x1b]8;;\x1b\\\x1b[0m in \x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=function&cwd=" + tmpDir + "\x1b\\function\x1b]8;;\x1b\\\x1b[0m\n",
		},
		{
			name:        "symbol links disabled",
			input:       "undefined: \x1b[31mNewLinker\x1b[0m\n",
			symbolLinks: false,
			expected:    "undefined: \x1b[31mNewLinker\x1b[0m\n",
		},
		{
			name:        "cursor control sequences skip symbol linking",
			input:       "\x1b[sStatus Line Display\x1b[u",
			symbolLinks: true,
			expected:    "\x1b[sStatus Line Display\x1b[u",
		},
		{
			name:        "symbol with ANSI color",
			input:       "\x1b[31mNewLinker\x1b[0m\n",
			symbolLinks: true,
			expected:    "\x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=NewLinker&cwd=" + tmpDir + "\x1b\\NewLinker\x1b]8;;\x1b\\\x1b[0m\n",
		},
		{
			name:        "SGR terminator does not create m-prefixed symbol",
			input:       "\x1b[36m@@ -1,1 +1,1 @@\x1b[mINSERT INTO\n",
			symbolLinks: true,
			expected:    "\x1b[36m@@ -1,1 +1,1 @@\x1b[mINSERT INTO\n",
		},
		{
			name:        "CSI bracket not used as boundary",
			input:       "\x1b[mTestFunc and more\n",
			symbolLinks: true,
			expected:    "\x1b[mTestFunc and more\n",
		},
		{
			name:        "ALL_CAPS linked",
			input:       "\x1b[31mERROR\x1b[0m and \x1b[31mHTTP_STATUS\x1b[0m\n",
			symbolLinks: true,
			expected:    "\x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=ERROR&cwd=" + tmpDir + "\x1b\\ERROR\x1b]8;;\x1b\\\x1b[0m and \x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=HTTP_STATUS&cwd=" + tmpDir + "\x1b\\HTTP_STATUS\x1b]8;;\x1b\\\x1b[0m\n",
		},
		{
			name:        "snake_case linked",
			input:       "\x1b[31mget_user_name\x1b[0m\n",
			symbolLinks: true,
			expected:    "\x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=get_user_name&cwd=" + tmpDir + "\x1b\\get_user_name\x1b]8;;\x1b\\\x1b[0m\n",
		},
		{
			name:        "symbol with numbers",
			input:       "\x1b[31mHandler2\x1b[0m and \x1b[31mV2Client\x1b[0m\n",
			symbolLinks: true,
			expected:    "\x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=Handler2&cwd=" + tmpDir + "\x1b\\Handler2\x1b]8;;\x1b\\\x1b[0m and \x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=V2Client&cwd=" + tmpDir + "\x1b\\V2Client\x1b]8;;\x1b\\\x1b[0m\n",
		},
		{
			name:        "symbol in parentheses",
			input:       "(\x1b[31mNewLinker\x1b[0m)\n",
			symbolLinks: true,
			expected:    "(\x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=NewLinker&cwd=" + tmpDir + "\x1b\\NewLinker\x1b]8;;\x1b\\\x1b[0m)\n",
		},
		{
			name:        "function with args has kind",
			input:       "\x1b[31mNewLinker(arg)\x1b[0m\n",
			symbolLinks: true,
			expected:    "\x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=NewLinker&cwd=" + tmpDir + "&kind=Function\x1b\\NewLinker\x1b]8;;\x1b\\(\x1b]8;;cursor://mash.symbol-opener?symbol=arg&cwd=" + tmpDir + "\x1b\\arg\x1b]8;;\x1b\\)\x1b[0m\n",
		},
		{
			name:        "acronym in PascalCase",
			input:       "\x1b[31mHTTPClient\x1b[0m and \x1b[31mXMLParser\x1b[0m\n",
			symbolLinks: true,
			expected:    "\x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=HTTPClient&cwd=" + tmpDir + "\x1b\\HTTPClient\x1b]8;;\x1b\\\x1b[0m and \x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=XMLParser&cwd=" + tmpDir + "\x1b\\XMLParser\x1b]8;;\x1b\\\x1b[0m\n",
		},
		{
			name:        "mid-word reset links colored part only",
			input:       "\x1b[31mFoo\x1b[0mBar\n",
			symbolLinks: true,
			expected:    "\x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=Foo&cwd=" + tmpDir + "\x1b\\Foo\x1b]8;;\x1b\\\x1b[0mBar\n",
		},
		{
			name:        "plain text not linked",
			input:       "plain NewLinker text\n",
			symbolLinks: true,
			expected:    "plain NewLinker text\n",
		},
		{
			name:        "nested SGR sequences",
			input:       "\x1b[31m\x1b[1mFoo\x1b[0m\n",
			symbolLinks: true,
			expected:    "\x1b[31m\x1b[1m\x1b]8;;cursor://mash.symbol-opener?symbol=Foo&cwd=" + tmpDir + "\x1b\\Foo\x1b]8;;\x1b\\\x1b[0m\n",
		},
		{
			name:        "reset then space separates words",
			input:       "\x1b[31mFoo\x1b[0m Bar\n",
			symbolLinks: true,
			expected:    "\x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=Foo&cwd=" + tmpDir + "\x1b\\Foo\x1b]8;;\x1b\\\x1b[0m Bar\n",
		},
		{
			name:        "partial coloring links only colored part",
			input:       "Foo\x1b[31mBar\x1b[0mBaz\n",
			symbolLinks: true,
			expected:    "Foo\x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=Bar&cwd=" + tmpDir + "\x1b\\Bar\x1b]8;;\x1b\\\x1b[0mBaz\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			linker := NewLinkerWithOptions(LinkerOptions{
				Output:      &buf,
				Cwd:         tmpDir,
				Hostname:    hostname,
				Scheme:      "cursor",
				Domains:     []string{"github.com"},
				SymbolLinks: tt.symbolLinks,
			})

			_, err := linker.Write([]byte(tt.input))
			if err != nil {
				t.Fatal(err)
			}

			if got := buf.String(); got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestLinker_SymbolLinksWithFilePaths(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	testFile, _ = filepath.EvalSymlinks(testFile)

	hostname := "testhost"

	var buf bytes.Buffer
	linker := NewLinkerWithOptions(LinkerOptions{
		Output:      &buf,
		Cwd:         tmpDir,
		Hostname:    hostname,
		Scheme:      "cursor",
		Domains:     []string{"github.com"},
		SymbolLinks: true,
	})

	input := testFile + ":10: undefined: \x1b[31mNewLinker\x1b[0m\n"
	expected := "\x1b]8;;cursor://file" + testFile + ":10\x1b\\" + testFile + ":10\x1b]8;;\x1b\\: undefined: \x1b[31m\x1b]8;;cursor://mash.symbol-opener?symbol=NewLinker&cwd=" + tmpDir + "\x1b\\NewLinker\x1b]8;;\x1b\\\x1b[0m\n"

	_, err := linker.Write([]byte(input))
	if err != nil {
		t.Fatal(err)
	}

	if got := buf.String(); got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}
