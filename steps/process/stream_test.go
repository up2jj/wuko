package process

import (
	"bytes"
	"errors"
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

func TestPrefixedWriterBoundsOutputWithoutNewlines(t *testing.T) {
	var output bytes.Buffer
	writer := prefixedWriter(&output, "svc", nil).(*linePrefixWriter)
	progress := bytes.Repeat([]byte("50%\r"), maxPendingLine/2)
	for range 4 {
		if _, err := writer.Write(progress); err != nil {
			t.Fatal(err)
		}
		if len(writer.pending) > maxPendingLine {
			t.Fatalf("pending = %d bytes, want at most %d", len(writer.pending), maxPendingLine)
		}
	}
	if _, err := writer.Write([]byte("done\nnext\n")); err != nil {
		t.Fatal(err)
	}
	if got := bytes.Count(output.Bytes(), []byte("[svc] ")); got != 2 {
		t.Fatalf("prefixes = %d, want one for the long line and one for the next", got)
	}
	if !bytes.HasSuffix(output.Bytes(), []byte("done\n[svc] next\n")) {
		t.Fatalf("output suffix = %q", output.Bytes()[max(0, output.Len()-40):])
	}
}

type failOnceWriter struct {
	failed bool
	bytes.Buffer
}

func (writer *failOnceWriter) Write(data []byte) (int, error) {
	if !writer.failed {
		writer.failed = true
		return 0, errors.New("temporarily unavailable")
	}
	return writer.Buffer.Write(data)
}

func TestPrefixedWriterDoesNotRepeatFailedLine(t *testing.T) {
	output := &failOnceWriter{}
	writer := prefixedWriter(output, "svc", nil)
	if _, err := writer.Write([]byte("first\n")); err == nil {
		t.Fatal("expected the first write to fail")
	}
	if _, err := writer.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "[svc] second\n" {
		t.Fatalf("output = %q", got)
	}
}
