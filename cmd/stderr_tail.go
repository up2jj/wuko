package cmd

import (
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	stderrTailBytes     = 16 << 10
	stderrTailLines     = 10
	stderrTailLineBytes = 512
)

var terminalEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// stderrTail keeps the end of streamed step stderr in bounded memory so a command that runs a
// workflow internally can show why it failed. Concurrent steps may write at the same time.
type stderrTail struct {
	mutex  sync.Mutex
	buffer []byte
}

func (tail *stderrTail) Write(data []byte) (int, error) {
	tail.mutex.Lock()
	defer tail.mutex.Unlock()
	if len(data) >= stderrTailBytes {
		tail.buffer = append(tail.buffer[:0], data[len(data)-stderrTailBytes:]...)
		return len(data), nil
	}
	tail.buffer = append(tail.buffer, data...)
	// Compact only after the buffer doubles, so a stream of small writes is amortized.
	if len(tail.buffer) > 2*stderrTailBytes {
		tail.buffer = append(tail.buffer[:0], tail.buffer[len(tail.buffer)-stderrTailBytes:]...)
	}
	return len(data), nil
}

func (tail *stderrTail) Reset() {
	tail.mutex.Lock()
	tail.buffer = tail.buffer[:0]
	tail.mutex.Unlock()
}

// Lines returns the last non-blank lines without terminal styling or carriage-return redraws.
func (tail *stderrTail) Lines() string {
	tail.mutex.Lock()
	rest := string(tail.buffer)
	tail.mutex.Unlock()
	var lines []string
	for rest != "" && len(lines) < stderrTailLines {
		index := strings.LastIndexByte(rest, '\n')
		line := stderrTailLine(rest[index+1:])
		rest = rest[:max(index, 0)]
		if line != "" {
			lines = append(lines, line)
		}
	}
	slices.Reverse(lines)
	return strings.Join(lines, "\n")
}

func stderrTailLine(line string) string {
	line = strings.TrimRight(line, "\r")
	if index := strings.LastIndexByte(line, '\r'); index >= 0 {
		line = line[index+1:]
	}
	line = strings.TrimRightFunc(terminalEscape.ReplaceAllString(line, ""), unicode.IsSpace)
	if !utf8.ValidString(line) {
		line = strings.ToValidUTF8(line, "�")
	}
	if len(line) <= stderrTailLineBytes {
		return line
	}
	limit := stderrTailLineBytes
	for !utf8.RuneStart(line[limit]) {
		limit--
	}
	return line[:limit] + "…"
}
