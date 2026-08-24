package githubpr

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

type recordedCall struct {
	result process.Result
	err    error
}

type recordingExecutor struct {
	calls []process.Options
	steps []recordedCall
}

func (executor *recordingExecutor) Run(_ context.Context, options process.Options) (process.Result, error) {
	executor.calls = append(executor.calls, options)
	if len(executor.calls) > len(executor.steps) {
		return process.Result{}, errors.New("unexpected command")
	}
	call := executor.steps[len(executor.calls)-1]
	return call.result, call.err
}

func TestNewValidatesConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"missing operation", map[string]any{}, "operation is required"},
		{"unsupported operation", map[string]any{"operation": "open"}, `operation must be "find"`},
		{"blank repository", map[string]any{"operation": "find", "repository": "  "}, "repository must not be blank"},
		{"blank branch", map[string]any{"operation": "find", "branch": "  "}, "branch must not be blank"},
		{"unknown field", map[string]any{"operation": "find", "unknown": true}, "field"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.raw); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New(%#v) error = %v, want %q", tt.raw, err, tt.want)
			}
		})
	}
}

func TestExplicitBranchAndRepositoryTakePrecedence(t *testing.T) {
	executor := &recordingExecutor{steps: []recordedCall{{result: process.Result{Stdout: `[{"number":17,"url":"https://github.com/acme/wuko/pull/17","title":"Improve release","state":"OPEN","isDraft":true,"headRefName":"feature/release","baseRefName":"main"}]`}}}}
	runner, err := New(map[string]any{"operation": "find", "repository": "acme/wuko", "branch": "feature/release"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		Executor: executor, RunDir: "/workspace", Env: map[string]string{
			"GITHUB_REPOSITORY": "other/repository", "GITHUB_REF": "refs/pull/99/merge", "GITHUB_HEAD_REF": "other",
		}, Attempt: 2, MaxAttempts: 3, OperationID: "lookup-pr",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(executor.calls))
	}
	call := executor.calls[0]
	if call.Command != "gh" || !slices.Equal(call.Args, []string{"pr", "list", "--state", "open", "--head", "feature/release", "--limit", "2", "--json", pullRequestJSONFields, "--repo", "acme/wuko"}) || call.Dir != "/workspace" {
		t.Fatalf("call = %#v", call)
	}
	if call.Env["GITHUB_REPOSITORY"] != "other/repository" || call.Env[step.AttemptEnv] != "2" || call.Env[step.OperationIDEnv] != "lookup-pr" {
		t.Fatalf("environment = %#v", call.Env)
	}
	if result.Outputs["number"] != 17 || result.Outputs["repository"] != "acme/wuko" || result.Outputs["is_draft"] != true {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestPullRequestEventRefUsesExactNumber(t *testing.T) {
	executor := &recordingExecutor{steps: []recordedCall{{result: process.Result{Stdout: `{"number":42,"url":"https://github.com/acme/wuko/pull/42","title":"Fix CI","state":"OPEN","isDraft":false,"headRefName":"ci/fix","headRefOid":"0123456789abcdef0123456789abcdef01234567","baseRefName":"main"}`}}}}
	runner, err := New(map[string]any{"operation": "find"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Executor: executor, Env: map[string]string{
		"GITHUB_REF": "refs/pull/42/merge", "GITHUB_REPOSITORY": "acme/wuko",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.calls) != 1 || executor.calls[0].Command != "gh" || !slices.Equal(executor.calls[0].Args, []string{"pr", "view", "42", "--json", pullRequestJSONFields, "--repo", "acme/wuko"}) {
		t.Fatalf("calls = %#v", executor.calls)
	}
	if result.Outputs["number"] != 42 || result.Outputs["head_branch"] != "ci/fix" || result.Outputs["head_sha"] != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestHeadRefIsUsedBeforeCurrentBranch(t *testing.T) {
	executor := &recordingExecutor{steps: []recordedCall{{result: process.Result{Stdout: `[{"number":8,"url":"url","title":"title","state":"OPEN","isDraft":false,"headRefName":"feature/head","baseRefName":"main"}]`}}}}
	runner, err := New(map[string]any{"operation": "find"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{Executor: executor, Env: map[string]string{"GITHUB_HEAD_REF": "feature/head"}}); err != nil {
		t.Fatal(err)
	}
	if len(executor.calls) != 1 || !slices.Equal(executor.calls[0].Args, []string{"pr", "list", "--state", "open", "--head", "feature/head", "--limit", "2", "--json", pullRequestJSONFields}) {
		t.Fatalf("calls = %#v", executor.calls)
	}
}

func TestCurrentBranchIsUsedWhenNoSelectorIsAvailable(t *testing.T) {
	executor := &recordingExecutor{steps: []recordedCall{
		{result: process.Result{Stdout: "feature/local\n"}},
		{result: process.Result{Stdout: `[{"number":9,"url":"url","title":"title","state":"OPEN","isDraft":false,"headRefName":"feature/local","baseRefName":"main"}]`}},
	}}
	runner, err := New(map[string]any{"operation": "find"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{Executor: executor, RunDir: "/repo"}); err != nil {
		t.Fatal(err)
	}
	if len(executor.calls) != 2 || !slices.Equal(executor.calls[0].Args, []string{"branch", "--show-current"}) || !slices.Equal(executor.calls[1].Args, []string{"pr", "list", "--state", "open", "--head", "feature/local", "--limit", "2", "--json", pullRequestJSONFields}) {
		t.Fatalf("calls = %#v", executor.calls)
	}
}

func TestNoMatchesReturnEmptyResult(t *testing.T) {
	executor := &recordingExecutor{steps: []recordedCall{{result: process.Result{Stdout: "[]\n"}}}}
	runner, err := New(map[string]any{"operation": "find", "branch": "feature/missing"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	want := noPullRequestOutputs()
	for key, value := range want {
		if result.Outputs[key] != value {
			t.Fatalf("outputs[%q] = %#v, want %#v; outputs = %#v", key, result.Outputs[key], value, result.Outputs)
		}
	}
}

func TestMultipleMatchesFailAsAmbiguous(t *testing.T) {
	executor := &recordingExecutor{steps: []recordedCall{{result: process.Result{Stdout: `[{"number":1},{"number":2}]`}}}}
	runner, err := New(map[string]any{"operation": "find", "branch": "feature/shared"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{Executor: executor}); err == nil || !strings.Contains(err.Error(), "matches multiple open GitHub pull requests") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunReportsMalformedAndCommandFailures(t *testing.T) {
	for _, tt := range []struct {
		name string
		call recordedCall
		want string
	}{
		{"malformed JSON", recordedCall{result: process.Result{Stdout: "not json"}}, "decoding GitHub pull requests"},
		{"authentication failure", recordedCall{result: process.Result{Stderr: "not logged in"}, err: &process.ExitError{Command: "gh", Code: 4}}, "listing GitHub pull requests: not logged in"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := New(map[string]any{"operation": "find", "branch": "feature/failure"})
			if err != nil {
				t.Fatal(err)
			}
			executor := &recordingExecutor{steps: []recordedCall{tt.call}}
			result, err := runner.Run(t.Context(), step.Request{Executor: executor})
			if err == nil || !strings.Contains(err.Error(), tt.want) || result.Outputs != nil {
				t.Fatalf("result = %#v, error = %v, want %q", result, err, tt.want)
			}
		})
	}
}

func TestRunPreservesCancellation(t *testing.T) {
	executor := &recordingExecutor{steps: []recordedCall{{err: context.Canceled}}}
	runner, err := New(map[string]any{"operation": "find", "branch": "feature/canceled"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Executor: executor})
	if !errors.Is(err, context.Canceled) || result.Outputs != nil {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestDetachedHeadFailsWithoutSelector(t *testing.T) {
	executor := &recordingExecutor{steps: []recordedCall{{result: process.Result{}}}}
	runner, err := New(map[string]any{"operation": "find"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{Executor: executor}); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestTemplatedConfigurationIsAcceptedBeforeRendering(t *testing.T) {
	if _, err := New(map[string]any{"operation": "find", "repository": "{{ .vars.repository }}", "branch": "{{ .vars.branch }}"}); err != nil {
		t.Fatal(err)
	}
}
