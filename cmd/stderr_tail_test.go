package cmd

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestStderrTailLines(t *testing.T) {
	var many strings.Builder
	for line := range 15 {
		fmt.Fprintf(&many, "line %d\n", line)
	}
	tests := []struct {
		name   string
		writes []string
		want   string
	}{
		{name: "empty", want: ""},
		{name: "blank lines only", writes: []string{" \n\r\n"}, want: ""},
		{name: "skips blank lines", writes: []string{"compiling\n\n** (CompileError) broken\n"}, want: "compiling\n** (CompileError) broken"},
		{name: "joins split writes", writes: []string{"comp", "iling\nerr", "or"}, want: "compiling\nerror"},
		{name: "keeps the last lines", writes: []string{many.String()}, want: "line 5\nline 6\nline 7\nline 8\nline 9\nline 10\nline 11\nline 12\nline 13\nline 14"},
		{name: "strips styling and redraws", writes: []string{"\x1b[31mfailed\x1b[0m\r\n10%\r50%\r100%\n"}, want: "failed\n100%"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tail := &stderrTail{}
			for _, write := range test.writes {
				if _, err := tail.Write([]byte(write)); err != nil {
					t.Fatal(err)
				}
			}
			if got := tail.Lines(); got != test.want {
				t.Fatalf("Lines() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestStderrTailBoundsMemoryAndLines(t *testing.T) {
	tail := &stderrTail{}
	for range 1000 {
		_, _ = tail.Write([]byte(strings.Repeat("x", 100) + "\n"))
	}
	if len(tail.buffer) > 2*stderrTailBytes {
		t.Fatalf("buffer = %d bytes, want at most %d", len(tail.buffer), 2*stderrTailBytes)
	}
	_, _ = tail.Write([]byte(strings.Repeat("é", stderrTailBytes)))
	if len(tail.buffer) != stderrTailBytes {
		t.Fatalf("buffer after a large write = %d bytes, want %d", len(tail.buffer), stderrTailBytes)
	}
	line := tail.Lines()
	if len(line) > stderrTailLineBytes+len("…") || !strings.HasSuffix(line, "…") || !utf8.ValidString(line) {
		t.Fatalf("Lines() = %d bytes, valid UTF-8 %t", len(line), utf8.ValidString(line))
	}
	tail.Reset()
	if got := tail.Lines(); got != "" {
		t.Fatalf("Lines() after Reset = %q", got)
	}
}

func TestStderrTailConcurrentWrites(t *testing.T) {
	tail := &stderrTail{}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 200 {
				_, _ = tail.Write([]byte("output line\n"))
				_ = tail.Lines()
			}
		})
	}
	group.Go(func() {
		for range 50 {
			tail.Reset()
		}
	})
	group.Wait()
}
