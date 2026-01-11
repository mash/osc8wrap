package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLinker_Write(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.go")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

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
			expected: "error in \x1b]8;;file://testhost" + testFile + "\x07" + testFile + "\x1b]8;;\x07\n",
		},
		{
			name:     "absolute path with line number",
			input:    "error in " + testFile + ":42\n",
			cwd:      tmpDir,
			expected: "error in \x1b]8;;file://testhost" + testFile + "\x07" + testFile + ":42\x1b]8;;\x07\n",
		},
		{
			name:     "absolute path with line and column",
			input:    "error in " + testFile + ":42:10\n",
			cwd:      tmpDir,
			expected: "error in \x1b]8;;file://testhost" + testFile + "\x07" + testFile + ":42:10\x1b]8;;\x07\n",
		},
		{
			name:     "relative path",
			input:    "error in ./test.go:10\n",
			cwd:      tmpDir,
			expected: "error in \x1b]8;;file://testhost" + testFile + "\x07./test.go:10\x1b]8;;\x07\n",
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
			expected: "\x1b]8;;file://testhost" + testFile + "\x07" + testFile + "\x1b]8;;\x07 and \x1b]8;;file://testhost" + testFile + "\x07" + testFile + "\x1b]8;;\x07\n",
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
			expected: "file: \x1b[32m\x1b]8;;file://testhost" + testFile + "\x07" + testFile + "\x1b]8;;\x07\x1b[0m\n",
		},
		{
			name:     "already has OSC-8 link",
			input:    "file: \x1b]8;;file://testhost" + testFile + "\x07test.go\x1b]8;;\x07\n",
			cwd:      tmpDir,
			expected: "file: \x1b]8;;file://testhost" + testFile + "\x07test.go\x1b]8;;\x07\n",
		},
		{
			name:     "https URL",
			input:    "see https://example.com/path for details\n",
			cwd:      tmpDir,
			expected: "see \x1b]8;;https://example.com/path\x07https://example.com/path\x1b]8;;\x07 for details\n",
		},
		{
			name:     "https URL with query params",
			input:    "see https://example.com/path?foo=bar&baz=qux for details\n",
			cwd:      tmpDir,
			expected: "see \x1b]8;;https://example.com/path?foo=bar&baz=qux\x07https://example.com/path?foo=bar&baz=qux\x1b]8;;\x07 for details\n",
		},
		{
			name:     "mixed file path and URL on same line",
			input:    testFile + " see https://example.com/docs\n",
			cwd:      tmpDir,
			expected: "\x1b]8;;file://testhost" + testFile + "\x07" + testFile + "\x1b]8;;\x07 see \x1b]8;;https://example.com/docs\x07https://example.com/docs\x1b]8;;\x07\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			linker := NewLinker(&buf, tt.cwd, hostname, "file")

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
	linker := NewLinker(&buf, tmpDir, hostname, "file")

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
	linker := NewLinker(&buf, tmpDir, hostname, "file")

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
			expected: "\x1b]8;;vscode://file" + testFile + ":42:10\x07" + testFile + ":42:10\x1b]8;;\x07\n",
		},
		{
			name:     "vscode scheme with line only",
			scheme:   "vscode",
			input:    testFile + ":42\n",
			expected: "\x1b]8;;vscode://file" + testFile + ":42\x07" + testFile + ":42\x1b]8;;\x07\n",
		},
		{
			name:     "vscode scheme without line",
			scheme:   "vscode",
			input:    testFile + "\n",
			expected: "\x1b]8;;vscode://file" + testFile + "\x07" + testFile + "\x1b]8;;\x07\n",
		},
		{
			name:     "cursor scheme",
			scheme:   "cursor",
			input:    testFile + ":10:5\n",
			expected: "\x1b]8;;cursor://file" + testFile + ":10:5\x07" + testFile + ":10:5\x1b]8;;\x07\n",
		},
		{
			name:     "custom scheme",
			scheme:   "myeditor",
			input:    testFile + ":1\n",
			expected: "\x1b]8;;myeditor://file" + testFile + ":1\x07" + testFile + ":1\x1b]8;;\x07\n",
		},
		{
			name:     "empty scheme defaults to file",
			scheme:   "",
			input:    testFile + ":42\n",
			expected: "\x1b]8;;file://" + hostname + testFile + "\x07" + testFile + ":42\x1b]8;;\x07\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			linker := NewLinker(&buf, tmpDir, hostname, tt.scheme)

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
