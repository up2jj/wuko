package githubactions

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
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "missing workflow", raw: map[string]any{}, want: "workflow is required"},
		{name: "mutually exclusive selectors", raw: map[string]any{"workflow": "ci.yml", "head_sha": strings.Repeat("a", 40), "pull_request": "12"}, want: "mutually exclusive"},
		{name: "invalid run id", raw: map[string]any{"run_id": "0"}, want: "run_id must be"},
		{name: "invalid pull request", raw: map[string]any{"workflow": "ci.yml", "pull_request": "nope"}, want: "pull_request must be"},
		{name: "invalid SHA", raw: map[string]any{"workflow": "ci.yml", "head_sha": "abc"}, want: "head_sha must be"},
		{name: "interval is not supported", raw: map[string]any{"workflow": "ci.yml", "head_sha": strings.Repeat("a", 40), "interval": 10}, want: "field interval"},
		{name: "unknown field", raw: map[string]any{"workflow": "ci.yml", "unknown": true}, want: "field unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New(%#v) error = %v, want %q", test.raw, err, test.want)
			}
		})
	}
}

func TestRunListsThenViewsExactCommit(t *testing.T) {
	headSHA := strings.Repeat("a", 40)
	executor := &recordingExecutor{steps: []recordedCall{
		{result: process.Result{Stdout: `[{"databaseId":42,"headSha":"` + headSHA + `","status":"in_progress","workflowName":"CI"}]`}},
		{result: process.Result{Stdout: `{"databaseId":42,"workflowName":"CI","workflowDatabaseId":7,"number":18,"status":"completed","conclusion":"success","event":"pull_request","headSha":"` + headSHA + `","headBranch":"feature/login","url":"https://github.com/acme/wuko/actions/runs/42","attempt":1}`}},
	}}
	runnerValue, err := New(map[string]any{"workflow": "ci.yml", "head_sha": headSHA})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor, RunDir: "/workspace", Env: map[string]string{"GITHUB_REPOSITORY": "acme/wuko"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(executor.calls))
	}
	if !slices.Equal(executor.calls[0].Args, []string{"run", "list", "--workflow", "ci.yml", "--commit", headSHA, "--limit", "20", "--json", runJSONFields, "--repo", "acme/wuko"}) {
		t.Fatalf("list args = %#v", executor.calls[0].Args)
	}
	if !slices.Equal(executor.calls[1].Args, []string{"run", "view", "42", "--json", runJSONFields, "--repo", "acme/wuko"}) {
		t.Fatalf("view args = %#v", executor.calls[1].Args)
	}
	if result.Outputs["run_id"] != int64(42) || result.Outputs["status"] != "completed" || result.Outputs["conclusion"] != "success" || result.Outputs["success"] != true || result.Outputs["repository"] != "acme/wuko" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestRunReturnsPendingObservationWhenRunDoesNotExist(t *testing.T) {
	headSHA := strings.Repeat("b", 40)
	executor := &recordingExecutor{steps: []recordedCall{{result: process.Result{Stdout: "[]\n"}}}}
	runnerValue, err := New(map[string]any{"workflow": "ci.yml", "head_sha": headSHA})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["found"] != false || result.Outputs["status"] != "not_found" || result.Outputs["terminal"] != false || result.Outputs["head_sha"] != headSHA {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if got := executor.calls[0].Args; !slices.Equal(got, []string{"run", "list", "--workflow", "ci.yml", "--commit", headSHA, "--limit", "20", "--json", runJSONFields}) {
		t.Fatalf("args = %#v", got)
	}
}

func TestRunResolvesPullRequestWithoutRepository(t *testing.T) {
	headSHA := strings.Repeat("c", 40)
	executor := &recordingExecutor{steps: []recordedCall{
		{result: process.Result{Stdout: `{"headRefOid":"` + headSHA + `"}`}},
		{result: process.Result{Stdout: `[{"databaseId":99,"headSha":"` + headSHA + `"}]`}},
		{result: process.Result{Stdout: `{"databaseId":99,"status":"queued","conclusion":"","headSha":"` + headSHA + `"}`}},
	}}
	runnerValue, err := New(map[string]any{"workflow": "ci.yml", "pull_request": "12"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(executor.calls[0].Args, []string{"pr", "view", "12", "--json", "headRefOid"}) || !slices.Equal(executor.calls[1].Args, []string{"run", "list", "--workflow", "ci.yml", "--commit", headSHA, "--limit", "20", "--json", runJSONFields}) {
		t.Fatalf("calls = %#v", executor.calls)
	}
	if result.Outputs["status"] != "queued" || result.Outputs["success"] != false {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestRunViewsKnownRun(t *testing.T) {
	executor := &recordingExecutor{steps: []recordedCall{{result: process.Result{Stdout: `{"databaseId":77,"status":"completed","conclusion":"failure","workflowName":"CI"}`}}}}
	runnerValue, err := New(map[string]any{"run_id": "77"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor, Env: map[string]string{"GITHUB_REPOSITORY": "acme/wuko"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.calls) != 1 || !slices.Equal(executor.calls[0].Args, []string{"run", "view", "77", "--json", runJSONFields, "--repo", "acme/wuko"}) {
		t.Fatalf("calls = %#v", executor.calls)
	}
	if result.Outputs["terminal"] != true || result.Outputs["success"] != false || result.Outputs["conclusion"] != "failure" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestNewAcceptsTemplatedSelectors(t *testing.T) {
	if _, err := New(map[string]any{
		"workflow": "ci.yml", "pull_request": "{{ .steps.pull_request.number }}",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(map[string]any{
		"run_id": "{{ .vars.run_id }}",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunPreservesCancellation(t *testing.T) {
	executor := &recordingExecutor{steps: []recordedCall{{err: context.Canceled}}}
	runnerValue, err := New(map[string]any{"run_id": "77"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor})
	if !errors.Is(err, context.Canceled) || result.Outputs != nil {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}
