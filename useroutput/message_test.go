package useroutput

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestWriteMessageLevelsAndLines(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		level  Level
		marker string
	}{{Info, "•"}, {Success, "✓"}, {Warn, "!"}, {Error, "✗"}} {
		t.Run(string(test.level), func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			Write(&output, Message{Text: "one\ntwo\n", Level: test.level}, false)
			want := test.marker + " one\n" + test.marker + " two\n"
			if diff := cmp.Diff(want, output.String()); diff != "" {
				t.Fatalf("output (-want +got):\n%s", diff)
			}
			output.Reset()
			Write(&output, Message{Text: "plain", Level: test.level, Plain: true}, true)
			if output.String() != "plain\n" {
				t.Fatalf("plain output = %q", output.String())
			}
		})
	}
}

func TestInspectPreservesJoinedOrdinaryFailures(t *testing.T) {
	t.Parallel()
	plain := &Failure{Message: "fix setup", Code: 2, Reported: true}
	ordinary := errors.New("cleanup failed")
	wrapped := fmt.Errorf("workflow: %w", errors.Join(fmt.Errorf("step: %w", plain), ordinary))
	view := Inspect(wrapped)
	if len(view.Failures) != 1 || view.Failures[0] != *plain || !errors.Is(view.Ordinary, ordinary) {
		t.Fatalf("view = %#v", view)
	}
	if OnlyPlain(wrapped) || !OnlyPlain(fmt.Errorf("step: %w", plain)) {
		t.Fatal("incorrect plain-only classification")
	}
	if Inspect(fmt.Errorf("context: %w", ordinary)).Ordinary.Error() != "context: cleanup failed" {
		t.Fatal("ordinary error context lost")
	}
}
