package process

import (
	"bytes"
	"io"
	"regexp"
	"sync"
)

const logMatchLimit = 1 << 20

type logMatcher struct {
	pattern *regexp.Regexp
	ready   chan struct{}
	mu      sync.Mutex
	matched bool
	buffer  []byte
}

func newLogMatcher(pattern *regexp.Regexp) *logMatcher {
	return &logMatcher{pattern: pattern, ready: make(chan struct{})}
}

func (matcher *logMatcher) add(data []byte) {
	if matcher == nil || matcher.pattern == nil {
		return
	}
	matcher.mu.Lock()
	defer matcher.mu.Unlock()
	// Readiness is decided once. Matching every later chunk would scan the whole rolling
	// window again for as long as the service keeps logging, which for a service that logs
	// is the entire run.
	if matcher.matched {
		return
	}
	matcher.buffer = append(matcher.buffer, data...)
	if len(matcher.buffer) > logMatchLimit {
		matcher.buffer = matcher.buffer[len(matcher.buffer)-logMatchLimit:]
	}
	if matcher.pattern.Match(matcher.buffer) {
		matcher.matched = true
		matcher.buffer = nil
		close(matcher.ready)
	}
}

type linePrefixWriter struct {
	mu      sync.Mutex
	writer  io.Writer
	prefix  []byte
	matcher *logMatcher
	pending []byte
}

func prefixedWriter(writer io.Writer, label string, matcher *logMatcher) io.Writer {
	if writer == nil {
		writer = io.Discard
	}
	return &linePrefixWriter{writer: writer, prefix: []byte("[" + label + "] "), matcher: matcher}
}

func (writer *linePrefixWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.matcher.add(data)
	inputLength := len(data)
	writer.pending = append(writer.pending, data...)
	for {
		index := bytes.IndexByte(writer.pending, '\n')
		if index < 0 {
			return inputLength, nil
		}
		line := append(append(make([]byte, 0, len(writer.prefix)+index+1), writer.prefix...), writer.pending[:index+1]...)
		if _, err := writer.writer.Write(line); err != nil {
			return inputLength, err
		}
		writer.pending = writer.pending[index+1:]
	}
}

func (writer *linePrefixWriter) Flush() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(writer.pending) == 0 {
		return nil
	}
	line := append(append(make([]byte, 0, len(writer.prefix)+len(writer.pending)), writer.prefix...), writer.pending...)
	writer.pending = nil
	_, err := writer.writer.Write(line)
	return err
}
