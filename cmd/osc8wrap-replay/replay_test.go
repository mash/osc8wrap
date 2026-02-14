package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseDebugLog(t *testing.T) {
	input := `=== Write #1 (8 bytes) ===
Input:  "\x1b[?2004h"
Output: "\x1b[?2004h"

=== Write #2 (4 bytes) ===
Input:  "foo\n"
Output: "bar\n"
`

	records, err := ParseDebugLog(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseDebugLog() error = %v", err)
	}

	want := []WriteRecord{
		{Seq: 1, Input: []byte("\x1b[?2004h"), Output: []byte("\x1b[?2004h")},
		{Seq: 2, Input: []byte("foo\n"), Output: []byte("bar\n")},
	}
	if diff := cmp.Diff(want, records); diff != "" {
		t.Fatalf("ParseDebugLog() mismatch (-want +got):\n%s", diff)
	}
}

func TestParseDebugLogErrors(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name: "incomplete block",
			input: `=== Write #1 (1 bytes) ===
Input:  "a"
`,
			wantErr: "incomplete write block",
		},
		{
			name: "invalid quoted payload",
			input: `=== Write #1 (1 bytes) ===
Input:  "a"
Output: "\xZZ"
`,
			wantErr: "invalid quoted payload",
		},
		{
			name: "unexpected content outside block",
			input: `hello
`,
			wantErr: "outside write block",
		},
		{
			name: "no blocks",
			input: `
`,
			wantErr: "no write blocks found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDebugLog(strings.NewReader(tc.input))
			if err == nil {
				t.Fatalf("ParseDebugLog() error = nil, want containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ParseDebugLog() error = %q, want containing %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestReplayWrites(t *testing.T) {
	tests := []struct {
		name      string
		records   []WriteRecord
		stepInput string
		opts      ReplayOptions
		wantOut   string
		wantErr   error
	}{
		{
			name: "output mode advances through all records",
			records: []WriteRecord{
				{Seq: 1, Input: []byte("in1"), Output: []byte("one")},
				{Seq: 2, Input: []byte("in2"), Output: []byte("two")},
			},
			stepInput: "\n",
			opts: ReplayOptions{
				Mode: StreamOutput,
			},
			wantOut: "onetwo",
		},
		{
			name: "input mode advances through all records",
			records: []WriteRecord{
				{Seq: 1, Input: []byte("in1"), Output: []byte("one")},
				{Seq: 2, Input: []byte("in2"), Output: []byte("two")},
			},
			stepInput: "\n",
			opts: ReplayOptions{
				Mode: StreamInput,
			},
			wantOut: "in1in2",
		},
		{
			name: "stops on EOF before next step",
			records: []WriteRecord{
				{Seq: 1, Output: []byte("first")},
				{Seq: 2, Output: []byte("second")},
			},
			stepInput: "",
			opts: ReplayOptions{
				Mode: StreamOutput,
			},
			wantOut: "first",
		},
		{
			name: "stops on ctrl-c byte",
			records: []WriteRecord{
				{Seq: 1, Output: []byte("first")},
				{Seq: 2, Output: []byte("second")},
			},
			stepInput: "\x03",
			opts: ReplayOptions{
				Mode: StreamOutput,
			},
			wantOut: "first",
			wantErr: errInterrupted,
		},
		{
			name: "stops on kitty ctrl-c sequence",
			records: []WriteRecord{
				{Seq: 1, Output: []byte("first")},
				{Seq: 2, Output: []byte("second")},
			},
			stepInput: "\x1b[99;5u",
			opts: ReplayOptions{
				Mode: StreamOutput,
			},
			wantOut: "first",
			wantErr: errInterrupted,
		},
		{
			name: "stops on xterm modify-other-keys ctrl-c sequence",
			records: []WriteRecord{
				{Seq: 1, Output: []byte("first")},
				{Seq: 2, Output: []byte("second")},
			},
			stepInput: "\x1b[27;5;99~",
			opts: ReplayOptions{
				Mode: StreamOutput,
			},
			wantOut: "first",
			wantErr: errInterrupted,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer

			err := ReplayWrites(context.Background(), tc.records, strings.NewReader(tc.stepInput), &out, tc.opts)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ReplayWrites() error = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("ReplayWrites() error = %v", err)
			}

			if diff := cmp.Diff(tc.wantOut, out.String()); diff != "" {
				t.Fatalf("ReplayWrites() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReplayWritesContextCancellation(t *testing.T) {
	records := []WriteRecord{
		{Seq: 1, Output: []byte("first")},
		{Seq: 2, Output: []byte("second")},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	var out bytes.Buffer
	err := ReplayWrites(ctx, records, pr, &out, ReplayOptions{Mode: StreamOutput})
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("ReplayWrites() error = %v, want %v", err, errInterrupted)
	}
	if diff := cmp.Diff("first", out.String()); diff != "" {
		t.Fatalf("ReplayWrites() output mismatch (-want +got):\n%s", diff)
	}
}
