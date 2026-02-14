package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

type WriteRecord struct {
	Seq    int
	Input  []byte
	Output []byte
}

type StreamMode string

const (
	StreamOutput StreamMode = "output"
	StreamInput  StreamMode = "input"
)

type ReplayOptions struct {
	Mode StreamMode
}

var writeHeaderPattern = regexp.MustCompile(`^=== Write #(\d+) \(\d+ bytes\) ===$`)
var errInterrupted = errors.New("interrupted")

func ParseStreamMode(value string) (StreamMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(StreamOutput):
		return StreamOutput, nil
	case string(StreamInput):
		return StreamInput, nil
	default:
		return "", fmt.Errorf("invalid --stream %q (expected: output, input)", value)
	}
}

func ParseDebugLog(r io.Reader) ([]WriteRecord, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var records []WriteRecord
	var current WriteRecord
	var lineNum int
	var blockStartLine int
	var inBlock bool
	var hasInput bool
	var hasOutput bool

	finalize := func() error {
		if !inBlock {
			return nil
		}
		if !hasInput || !hasOutput {
			return fmt.Errorf("incomplete write block starting at line %d", blockStartLine)
		}
		records = append(records, current)
		inBlock = false
		hasInput = false
		hasOutput = false
		return nil
	}

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		if strings.HasPrefix(line, "=== Write #") {
			if err := finalize(); err != nil {
				return nil, err
			}

			seq, err := parseWriteHeader(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNum, err)
			}
			current = WriteRecord{Seq: seq}
			blockStartLine = lineNum
			inBlock = true
			continue
		}

		if strings.TrimSpace(line) == "" {
			continue
		}

		if !inBlock {
			return nil, fmt.Errorf("line %d: unexpected content outside write block", lineNum)
		}

		if strings.HasPrefix(line, "Input:  ") {
			if hasInput {
				return nil, fmt.Errorf("line %d: duplicate Input line for write #%d", lineNum, current.Seq)
			}
			decoded, err := parseQuotedPayload(strings.TrimPrefix(line, "Input:  "))
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNum, err)
			}
			current.Input = decoded
			hasInput = true
			continue
		}

		if strings.HasPrefix(line, "Output: ") {
			if hasOutput {
				return nil, fmt.Errorf("line %d: duplicate Output line for write #%d", lineNum, current.Seq)
			}
			decoded, err := parseQuotedPayload(strings.TrimPrefix(line, "Output: "))
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNum, err)
			}
			current.Output = decoded
			hasOutput = true
			continue
		}

		return nil, fmt.Errorf("line %d: unexpected line inside write block", lineNum)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := finalize(); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, errors.New("no write blocks found")
	}

	return records, nil
}

func ReplayWrites(ctx context.Context, records []WriteRecord, stepInput io.Reader, streamOutput io.Writer, opts ReplayOptions) error {
	if len(records) == 0 {
		return errors.New("no records to replay")
	}

	if opts.Mode == "" {
		opts.Mode = StreamOutput
	}
	if _, err := ParseStreamMode(string(opts.Mode)); err != nil {
		return err
	}
	if stepInput == nil {
		return errors.New("step input is nil")
	}
	if streamOutput == nil {
		streamOutput = io.Discard
	}

	reader := bufio.NewReader(stepInput)

	for i, rec := range records {
		if err := emitRecord(streamOutput, rec, opts.Mode); err != nil {
			return err
		}

		if i == len(records)-1 {
			break
		}

		errCh := make(chan error, 1)
		go func() {
			errCh <- waitForNextStep(reader)
		}()

		select {
		case <-ctx.Done():
			return errInterrupted
		case err := <-errCh:
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func parseWriteHeader(line string) (int, error) {
	matches := writeHeaderPattern.FindStringSubmatch(line)
	if len(matches) != 2 {
		return 0, fmt.Errorf("invalid write header %q", line)
	}
	seq, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, fmt.Errorf("invalid write sequence in %q", line)
	}
	return seq, nil
}

func parseQuotedPayload(quoted string) ([]byte, error) {
	unquoted, err := strconv.Unquote(quoted)
	if err != nil {
		return nil, fmt.Errorf("invalid quoted payload %q: %w", quoted, err)
	}
	return []byte(unquoted), nil
}

func emitRecord(w io.Writer, rec WriteRecord, mode StreamMode) error {
	switch mode {
	case StreamInput:
		_, err := w.Write(rec.Input)
		return err
	default:
		_, err := w.Write(rec.Output)
		return err
	}
}

func waitForNextStep(reader *bufio.Reader) error {
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return err
		}

		switch b {
		case '\n', '\r':
			return nil
		case 0x03:
			return errInterrupted
		case 0x1b:
			interrupted, err := parseEscapedInterrupt(reader)
			if err != nil {
				return err
			}
			if interrupted {
				return errInterrupted
			}
		}
	}
}

func parseEscapedInterrupt(reader *bufio.Reader) (bool, error) {
	b, err := reader.ReadByte()
	if err != nil {
		return false, err
	}
	if b != '[' {
		return false, nil
	}

	seq := make([]byte, 0, 16)
	seq = append(seq, '[')

	for {
		next, err := reader.ReadByte()
		if err != nil {
			return false, err
		}
		seq = append(seq, next)

		if next >= 0x40 && next <= 0x7E {
			return isCtrlCSequence(seq), nil
		}
		if len(seq) >= 64 {
			return false, nil
		}
	}
}

func isCtrlCSequence(seq []byte) bool {
	if len(seq) < 2 || seq[0] != '[' {
		return false
	}

	switch seq[len(seq)-1] {
	case 'u':
		body := string(seq[1 : len(seq)-1])
		parts := strings.Split(body, ";")
		if len(parts) < 2 {
			return false
		}
		keyCode, err := strconv.Atoi(parts[0])
		if err != nil || !isCtrlCKeyCode(keyCode) {
			return false
		}

		modifierPart := parts[1]
		if i := strings.IndexByte(modifierPart, ':'); i >= 0 {
			modifierPart = modifierPart[:i]
		}
		modifier, err := strconv.Atoi(modifierPart)
		if err != nil {
			return false
		}

		return hasCtrlModifier(modifier)
	case '~':
		body := string(seq[1 : len(seq)-1])
		parts := strings.Split(body, ";")
		if len(parts) < 3 {
			return false
		}

		first, err := strconv.Atoi(parts[0])
		if err != nil || first != 27 {
			return false
		}
		modifier, err := strconv.Atoi(parts[1])
		if err != nil || !hasCtrlModifier(modifier) {
			return false
		}
		keyCode, err := strconv.Atoi(parts[2])
		if err != nil {
			return false
		}
		return isCtrlCKeyCode(keyCode)
	default:
		return false
	}
}

func hasCtrlModifier(modifier int) bool {
	// Kitty/xterm encode modifiers as 1 + bitmask(shift=1,alt=2,ctrl=4,...).
	if modifier <= 0 {
		return false
	}
	return ((modifier - 1) & 4) != 0
}

func isCtrlCKeyCode(keyCode int) bool {
	return keyCode == 3 || keyCode == 67 || keyCode == 99
}
