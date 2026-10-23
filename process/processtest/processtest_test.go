package processtest

import (
	"errors"
	"strings"
	"testing"

	"github.com/up2jj/wuko/process"
)

func TestExecutorAnswersRepliesInOrderThenDefault(t *testing.T) {
	t.Parallel()
	failure := errors.New("failed")
	executor := &Executor{
		Replies: []Reply{{Result: process.Result{Stdout: "first"}}, {Err: failure}},
		Reply:   Reply{Result: process.Result{Stdout: "rest"}},
	}
	var outputs []string
	var errs []error
	for _, command := range []string{"one", "two", "three"} {
		result, err := executor.Run(t.Context(), process.Options{Command: command})
		outputs = append(outputs, result.Stdout)
		errs = append(errs, err)
	}
	if strings.Join(outputs, ",") != "first,,rest" || errs[0] != nil || !errors.Is(errs[1], failure) || errs[2] != nil {
		t.Fatalf("outputs = %q, errors = %v", outputs, errs)
	}
	if calls := executor.Calls(); len(calls) != 3 || executor.Last().Command != "three" {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestExecutorStrictRejectsUnexpectedCommands(t *testing.T) {
	t.Parallel()
	executor := &Executor{Strict: true, Replies: []Reply{{}}}
	if _, err := executor.Run(t.Context(), process.Options{Command: "git"}); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Run(t.Context(), process.Options{Command: "git", Args: []string{"push"}}); err == nil || !strings.Contains(err.Error(), "unexpected command 2: git push") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecutorStreamsOutputAndIsolatesRecordedOptions(t *testing.T) {
	t.Parallel()
	executor := &Executor{Reply: Reply{Result: process.Result{Stdout: "out", Stderr: "err"}}}
	var stdout, stderr strings.Builder
	args := []string{"a"}
	if _, err := executor.Run(t.Context(), process.Options{Args: args, Stdout: &stdout, Stderr: &stderr, StderrPolicy: process.OutputCapture}); err != nil {
		t.Fatal(err)
	}
	args[0] = "changed"
	if stdout.String() != "out" || stderr.String() != "" {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
	if got := executor.Last().Args[0]; got != "a" {
		t.Fatalf("recorded args = %q, want a copy", got)
	}
}
