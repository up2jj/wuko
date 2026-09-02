package process

import (
	"bytes"
	"regexp"
	"testing"
)

// BenchmarkLogMatcherAfterReady covers the steady state of a service that keeps logging after its
// readiness pattern matched. Rescanning the rolling window on every chunk cost about 25us per
// chunk, which capped a service's output at roughly 10 MB/s of pure regex work.
func BenchmarkLogMatcherAfterReady(b *testing.B) {
	matcher := newLogMatcher(regexp.MustCompile("listening on [0-9]+"))
	matcher.add([]byte("listening on 8080\n"))
	chunk := bytes.Repeat([]byte("2026-09-02 request served in 3ms\n"), 8)
	for range logMatchLimit / len(chunk) {
		matcher.add(chunk)
	}
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for range b.N {
		matcher.add(chunk)
	}
}

func TestLogMatcherStopsMatchingOnceReady(t *testing.T) {
	matcher := newLogMatcher(regexp.MustCompile("^ready$"))
	matcher.add([]byte("ready"))
	select {
	case <-matcher.ready:
	default:
		t.Fatal("the matcher did not report readiness")
	}
	matcher.add([]byte("more output"))
	if matcher.buffer != nil {
		t.Fatalf("the matcher retained %d bytes after readiness", len(matcher.buffer))
	}
}
