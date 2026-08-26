package ptyinteract

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestCompileValidatesSpecs(t *testing.T) {
	tests := []struct {
		name  string
		specs []Spec
		want  string
	}{
		{name: "empty", want: "at least one"},
		{name: "empty expect", specs: []Spec{{HasExpect: true}}, want: "expect must not be empty"},
		{name: "invalid expect", specs: []Spec{{HasExpect: true, Expect: "[", Send: "x"}}, want: "compiling expect"},
		{name: "send timeout", specs: []Spec{{Send: "x", TimeoutSet: true, Timeout: time.Second}}, want: "timeout requires expect"},
		{name: "zero timeout", specs: []Spec{{HasExpect: true, Expect: "x", TimeoutSet: true}}, want: "timeout must be positive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(test.specs)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Compile() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunExecutesImmediateAndPromptInteractionsInOrder(t *testing.T) {
	plan, err := Compile([]Spec{
		{Send: "first", Newline: true},
		{HasExpect: true, Expect: "prompt-one>", Send: "second", Newline: true},
		{HasExpect: true, Expect: "prompt-two>", Send: "", Newline: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	output := make(chan []byte, 1)
	output <- []byte("prefix prompt-one> trailing prompt-two>")
	close(output)
	sink := newRecordingSink()
	if err := plan.Run(t.Context(), Stream{Output: output}, nil, sink); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sink.writes, "|"); got != "first\r|second\r|\r" {
		t.Fatalf("writes = %q", got)
	}
}

func TestRunMatchesAcrossOutputChunks(t *testing.T) {
	plan, err := Compile([]Spec{{HasExpect: true, Expect: "ready>", Send: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	output := make(chan []byte, 2)
	output <- []byte("rea")
	output <- []byte("dy>")
	close(output)
	sink := newRecordingSink()
	if err := plan.Run(t.Context(), Stream{Output: output}, nil, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.writes) != 1 || sink.writes[0] != "go" {
		t.Fatalf("writes = %#v", sink.writes)
	}
}

func TestRunUsesSensitiveSink(t *testing.T) {
	plan, err := Compile([]Spec{{Send: "secret", Sensitive: true}})
	if err != nil {
		t.Fatal(err)
	}
	sink := newRecordingSink()
	if err := plan.Run(t.Context(), Stream{}, nil, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.sensitive) != 1 || sink.sensitive[0] != "secret" || len(sink.writes) != 0 {
		t.Fatalf("sensitive = %#v, writes = %#v", sink.sensitive, sink.writes)
	}
}

func TestRunReportsTimeoutWithoutWriting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		plan, err := Compile([]Spec{{HasExpect: true, Expect: "missing", Send: "never", TimeoutSet: true, Timeout: time.Second}})
		if err != nil {
			t.Fatal(err)
		}
		err = plan.Run(t.Context(), Stream{Output: make(chan []byte)}, nil, newRecordingSink())
		var interactionErr *Error
		if !errors.As(err, &interactionErr) || interactionErr.Kind != FailureTimeout || interactionErr.Index != 1 {
			t.Fatalf("Run() error = %v", err)
		}
	})
}

func TestRunReportsOverflowAndEarlyExit(t *testing.T) {
	tests := []struct {
		name   string
		output func() <-chan []byte
		kind   FailureKind
	}{
		{name: "overflow", kind: FailureOverflow, output: func() <-chan []byte {
			output := make(chan []byte, 1)
			output <- make([]byte, MaxUnmatchedBytes+1)
			return output
		}},
		{name: "exit", kind: FailureExited, output: func() <-chan []byte {
			output := make(chan []byte)
			close(output)
			return output
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := Compile([]Spec{{HasExpect: true, Expect: "missing", Send: "never"}})
			if err != nil {
				t.Fatal(err)
			}
			err = plan.Run(t.Context(), Stream{Output: test.output()}, nil, newRecordingSink())
			var interactionErr *Error
			if !errors.As(err, &interactionErr) || interactionErr.Kind != test.kind {
				t.Fatalf("Run() error = %v", err)
			}
		})
	}
}

func TestRunReportsStreamBufferOverflow(t *testing.T) {
	plan, err := Compile([]Spec{{HasExpect: true, Expect: "missing", Send: "never"}})
	if err != nil {
		t.Fatal(err)
	}
	overflow := make(chan struct{})
	close(overflow)
	err = plan.Run(t.Context(), Stream{Output: make(chan []byte), Overflow: overflow}, nil, newRecordingSink())
	var interactionErr *Error
	if !errors.As(err, &interactionErr) || interactionErr.Kind != FailureOverflow {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunReportsWriteFailureWithoutExposingData(t *testing.T) {
	plan, err := Compile([]Spec{{Send: "super-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{err: io.ErrClosedPipe}
	err = plan.Run(t.Context(), Stream{}, nil, sink)
	if err == nil || strings.Contains(err.Error(), "super-secret") || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestPlanCanBeReused(t *testing.T) {
	plan, err := Compile([]Spec{{Send: "same"}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		sink := newRecordingSink()
		if err := plan.Run(t.Context(), Stream{}, nil, sink); err != nil || len(sink.writes) != 1 || sink.writes[0] != "same" {
			t.Fatalf("Run() error = %v, writes = %#v", err, sink.writes)
		}
	}
}

type recordingSink struct {
	writes    []string
	sensitive []string
	err       error
}

func newRecordingSink() *recordingSink { return &recordingSink{} }

func (sink *recordingSink) Write(data []byte) (int, error) {
	if sink.err != nil {
		return 0, sink.err
	}
	sink.writes = append(sink.writes, string(data))
	return len(data), nil
}

func (sink *recordingSink) WriteSensitive(data []byte) error {
	if sink.err != nil {
		return sink.err
	}
	sink.sensitive = append(sink.sensitive, string(data))
	return nil
}
