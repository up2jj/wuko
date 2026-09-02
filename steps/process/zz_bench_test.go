package process

import (
	"bytes"
	"regexp"
	"testing"
)

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
