package githubactions

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	enginepkg "github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type recordedCall struct {
	result process.Result
	err    error
}

type recordingExecutor struct {
	calls []process.Options
	steps []recordedCall
}

type runnerFunc func(context.Context, step.Request) (step.Result, error)

func (run runnerFunc) Run(ctx context.Context, request step.Request) (step.Result, error) {
	return run(ctx, request)
}

type executorFunc func(context.Context, process.Options) (process.Result, error)

func (run executorFunc) Run(ctx context.Context, options process.Options) (process.Result, error) {
	return run(ctx, options)
}

func (executor *recordingExecutor) Run(_ context.Context, options process.Options) (process.Result, error) {
	executor.calls = append(executor.calls, options)
	if len(executor.calls) > len(executor.steps) {
		return process.Result{}, errors.New("unexpected command")
	}
	call := executor.steps[len(executor.calls)-1]
	if options.StdoutPolicy.Streams() && options.Stdout != nil {
		_, _ = io.WriteString(options.Stdout, call.result.Stdout)
	}
	if options.StderrPolicy.Streams() && options.Stderr != nil {
		_, _ = io.WriteString(options.Stderr, call.result.Stderr)
	}
	return call.result, call.err
}

func TestNewValidatesConfiguration(t *testing.T) {
	headSHA := strings.Repeat("a", 40)
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "missing operation", raw: map[string]any{"workflow": "ci"}, want: "operation is required"},
		{name: "unsupported operation", raw: map[string]any{"operation": "observe", "workflow": "ci"}, want: "operation must be watch"},
		{name: "templated operation", raw: map[string]any{"operation": "{{ .vars.operation }}", "workflow": "ci"}, want: "operation must not be templated"},
		{name: "missing workflow", raw: watchConfig(), want: "workflow is required"},
		{name: "mutually exclusive selectors", raw: map[string]any{"operation": "watch", "workflow": "ci", "head_sha": headSHA, "pull_request": "12"}, want: "mutually exclusive"},
		{name: "invalid run id", raw: map[string]any{"operation": "watch", "run_id": "0"}, want: "run_id must be"},
		{name: "invalid pull request", raw: map[string]any{"operation": "watch", "workflow": "ci", "pull_request": "nope"}, want: "pull_request must be"},
		{name: "invalid SHA", raw: map[string]any{"operation": "watch", "workflow": "ci", "head_sha": "abc"}, want: "head_sha must be"},
		{name: "zero interval", raw: map[string]any{"operation": "watch", "workflow": "ci", "interval": 0}, want: "interval must be"},
		{name: "negative interval", raw: map[string]any{"operation": "watch", "workflow": "ci", "interval": -1}, want: "interval must be"},
		{name: "step without job", raw: map[string]any{"operation": "watch", "workflow": "ci", "step": "build"}, want: "job is required"},
		{name: "empty job", raw: map[string]any{"operation": "watch", "workflow": "ci", "job": "  "}, want: "job must not be empty"},
		{name: "empty step", raw: map[string]any{"operation": "watch", "workflow": "ci", "job": "test", "step": "  "}, want: "step must not be empty"},
		{name: "unknown field", raw: map[string]any{"operation": "watch", "workflow": "ci", "unknown": true}, want: "field unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New(%#v) error = %v, want %q", test.raw, err, test.want)
			}
		})
	}
}

func TestNewAcceptsSupportedSelectorsAndTemplates(t *testing.T) {
	tests := []map[string]any{
		{"operation": "watch", "workflow": "ci"},
		{"operation": "watch", "run_id": "42"},
		{"operation": "watch", "workflow": "ci", "pull_request": "{{ .steps.pull_request.number }}"},
		{"operation": "watch", "workflow": "ci", "head_sha": "{{ .vars.head_sha }}"},
		{"operation": "watch", "workflow": "ci", "job": "action ({{ .matrix.os }})", "step": "{{ .vars.build_step }}"},
	}
	for _, raw := range tests {
		if _, err := New(raw); err != nil {
			t.Fatalf("New(%#v) error = %v", raw, err)
		}
	}
}

func TestEngineRendersJobAndStepSelectorsFromRuntimeContext(t *testing.T) {
	registry := step.NewRegistry()
	if err := registry.Register("producer", func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			return step.Result{Outputs: map[string]any{"suffix": "from-step"}}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	var rendered []Config
	if err := registry.Register("github_actions", func(raw map[string]any) (step.Runner, error) {
		built, err := New(raw)
		if err != nil {
			return nil, err
		}
		config := built.(*Runner).config
		if !templated(config.Job) && !templated(config.Step) {
			rendered = append(rendered, config)
		}
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			return step.Result{Outputs: map[string]any{"log": "ok"}}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1,
		Name:    "render-github-actions-selectors",
		Dir:     t.TempDir(),
		Vars:    map[string]any{"prefix": "action", "build_step": "build action test binary"},
		Steps: []workflow.Step{
			{ID: "producer", Type: "producer", With: map[string]any{}},
			{
				ID: "platforms",
				Matrix: &workflow.MatrixGroup{
					Axes:           workflow.MatrixAxes{{Name: "os", Values: []any{"ubuntu-latest"}}},
					MaxConcurrency: 1,
					Steps: []workflow.Step{{
						ID: "watch", Type: "github_actions",
						With: map[string]any{
							"operation": "watch", "workflow": "ci",
							"job":  `{{ .vars.prefix }} ({{ .matrix.os }}) {{ .steps.producer.suffix }} {{ .dependencies.base.suffix }} {{ .env.MODE }}`,
							"step": `{{ .vars.build_step }}`,
						},
					}},
				},
			},
		},
	}
	_, err := enginepkg.New(registry).Run(t.Context(), definition, enginepkg.Options{
		RunDir: t.TempDir(), Env: map[string]string{"MODE": "local"},
		Dependencies: map[string]map[string]any{"base": {"suffix": "from-dependency"}},
		Stdout:       io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered) != 1 {
		t.Fatalf("rendered configs = %#v", rendered)
	}
	if rendered[0].Job != "action (ubuntu-latest) from-step from-dependency local" || rendered[0].Step != "build action test binary" {
		t.Fatalf("rendered config = %#v", rendered[0])
	}
}

func TestRunDiscoversLatestWorkflowRunAndWatchesIt(t *testing.T) {
	executor := &recordingExecutor{steps: []recordedCall{
		{result: process.Result{Stdout: `[{"databaseId":42,"status":"in_progress","workflowName":"ci"}]`}},
		{result: process.Result{Stdout: "watch progress\n"}},
		{result: process.Result{Stdout: `{"databaseId":42,"workflowName":"ci","workflowDatabaseId":7,"number":18,"status":"completed","conclusion":"success","event":"push","headSha":"abc","headBranch":"master","url":"https://github.com/acme/wuko/actions/runs/42","attempt":1}`}},
	}}
	runnerValue := newRunner(t, map[string]any{"operation": "watch", "workflow": "ci"})
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor, RunDir: "/workspace", Env: map[string]string{"GITHUB_REPOSITORY": "acme/wuko"}})
	if err != nil {
		t.Fatal(err)
	}
	assertArgs(t, executor.calls[0], "run", "list", "--workflow", "ci", "--limit", "1", "--json", runJSONFields, "--repo", "acme/wuko")
	assertArgs(t, executor.calls[1], "run", "watch", "42", "--compact", "--repo", "acme/wuko")
	assertArgs(t, executor.calls[2], "run", "view", "42", "--json", runJSONFields, "--repo", "acme/wuko")
	if result.Outputs["run_id"] != int64(42) || result.Outputs["success"] != true || result.Outputs["repository"] != "acme/wuko" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestResolveRepositoryUsesConfiguredEnvironmentAndLocalContexts(t *testing.T) {
	tests := []struct {
		name        string
		configured  string
		environment map[string]string
		want        string
	}{
		{name: "configured", configured: "configured/repo", environment: map[string]string{"GITHUB_REPOSITORY": "actions/repo", "GH_REPO": "cli/repo"}, want: "configured/repo"},
		{name: "actions", environment: map[string]string{"GITHUB_REPOSITORY": "actions/repo", "GH_REPO": "cli/repo"}, want: "actions/repo"},
		{name: "gh repo", environment: map[string]string{"GH_REPO": "cli/repo"}, want: "cli/repo"},
		{name: "local git context", environment: map[string]string{}, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveRepository(test.configured, test.environment); got != test.want {
				t.Fatalf("resolveRepository() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRunListsThenWatchesExactCommit(t *testing.T) {
	headSHA := strings.Repeat("a", 40)
	executor := &recordingExecutor{steps: []recordedCall{
		{result: process.Result{Stdout: `[{"databaseId":42,"headSha":"` + headSHA + `"}]`}},
		{},
		{result: process.Result{Stdout: `{"databaseId":42,"status":"completed","conclusion":"success"}`}},
	}}
	runnerValue := newRunner(t, map[string]any{"operation": "watch", "workflow": "ci.yml", "head_sha": headSHA})
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	assertArgs(t, executor.calls[0], "run", "list", "--workflow", "ci.yml", "--commit", headSHA, "--limit", "20", "--json", runJSONFields)
	assertArgs(t, executor.calls[1], "run", "watch", "42", "--compact")
	if result.Outputs["head_sha"] != headSHA {
		t.Fatalf("head_sha = %#v", result.Outputs["head_sha"])
	}
}

func TestRunResolvesPullRequestBeforeWatching(t *testing.T) {
	headSHA := strings.Repeat("c", 40)
	executor := &recordingExecutor{steps: []recordedCall{
		{result: process.Result{Stdout: `{"headRefOid":"` + headSHA + `"}`}},
		{result: process.Result{Stdout: `[{"databaseId":99,"headSha":"` + headSHA + `"}]`}},
		{},
		{result: process.Result{Stdout: `{"databaseId":99,"status":"completed","conclusion":"failure","headSha":"` + headSHA + `"}`}},
	}}
	runnerValue := newRunner(t, map[string]any{"operation": "watch", "workflow": "ci.yml", "pull_request": "12"})
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	assertArgs(t, executor.calls[0], "pr", "view", "12", "--json", "headRefOid")
	assertArgs(t, executor.calls[1], "run", "list", "--workflow", "ci.yml", "--commit", headSHA, "--limit", "20", "--json", runJSONFields)
	if result.Outputs["terminal"] != true || result.Outputs["success"] != false || result.Outputs["conclusion"] != "failure" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestRunFailsWhenLatestDiscoveredRunAlreadyCompleted(t *testing.T) {
	executor := &recordingExecutor{steps: []recordedCall{
		{result: process.Result{Stdout: `[{"databaseId":41,"status":"completed","conclusion":"success"}]`}},
	}}
	runnerValue := newRunner(t, map[string]any{"operation": "watch", "workflow": "ci"})
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor})
	if err == nil || !strings.Contains(err.Error(), "run 41") || !strings.Contains(err.Error(), "has already completed") || result.Outputs != nil {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("calls = %#v", executor.calls)
	}
}

func TestRunWatchesCompletedRunSelectedByCommit(t *testing.T) {
	headSHA := strings.Repeat("b", 40)
	executor := &recordingExecutor{steps: []recordedCall{
		{result: process.Result{Stdout: `[{"databaseId":42,"status":"completed","headSha":"` + headSHA + `"}]`}},
		{},
		{result: process.Result{Stdout: `{"databaseId":42,"status":"completed","conclusion":"success"}`}},
	}}
	runnerValue := newRunner(t, map[string]any{"operation": "watch", "workflow": "ci", "head_sha": headSHA})
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["run_id"] != int64(42) || result.Outputs["success"] != true {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestRunFailsWhenDiscoveryFindsNoRun(t *testing.T) {
	executor := &recordingExecutor{steps: []recordedCall{{result: process.Result{Stdout: "[]\n"}}}}
	runnerValue := newRunner(t, map[string]any{"operation": "watch", "workflow": "ci"})
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor})
	if err == nil || !strings.Contains(err.Error(), `no GitHub Actions run found for workflow "ci"`) || result.Outputs != nil {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestRunWatchesKnownRunWithIntervalAndStreamsProgress(t *testing.T) {
	var stdout, stderr bytes.Buffer
	executor := &recordingExecutor{steps: []recordedCall{
		{result: process.Result{Stdout: "progress\n", Stderr: "warning\n"}},
		{result: process.Result{Stdout: `{"databaseId":77,"status":"completed","conclusion":"failure","workflowName":"CI"}`}},
	}}
	runnerValue := newRunner(t, map[string]any{"operation": "watch", "run_id": "77", "interval": 5})
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	assertArgs(t, executor.calls[0], "run", "watch", "77", "--compact", "--interval", "5")
	watch := executor.calls[0]
	if watch.StdoutPolicy != process.OutputTee || watch.StderrPolicy != process.OutputTee || watch.CaptureLimit != watchCaptureLimit {
		t.Fatalf("watch output options = %#v", watch)
	}
	if stdout.String() != "progress\n" || stderr.String() != "warning\n" {
		t.Fatalf("streamed stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
	if result.Outputs["success"] != false {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if _, exists := result.Outputs["found"]; exists {
		t.Fatalf("found output unexpectedly present: %#v", result.Outputs)
	}
}

func TestRunFailsWhenWatchCommandFails(t *testing.T) {
	executor := &recordingExecutor{steps: []recordedCall{{
		result: process.Result{Stderr: "authentication required\n"}, err: errors.New("exit status 1"),
	}}}
	runnerValue := newRunner(t, map[string]any{"operation": "watch", "run_id": "77"})
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor})
	if err == nil || !strings.Contains(err.Error(), "watching GitHub Actions run") || !strings.Contains(err.Error(), "authentication required") || result.Outputs != nil {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("calls = %#v", executor.calls)
	}
}

func TestRunReturnsCompleteJobLog(t *testing.T) {
	view := `{"databaseId":77,"status":"completed","conclusion":"success","jobs":[{"databaseId":101,"name":"action (ubuntu-latest)","status":"completed","conclusion":"success"}]}`
	rawLog := "\ufeff2026-09-05T18:18:46.6404147Z job log\n"
	executor := &recordingExecutor{steps: []recordedCall{{}, {result: process.Result{Stdout: view}}, {result: process.Result{Stdout: rawLog}}}}
	runnerValue := newRunner(t, map[string]any{
		"operation": "watch", "repository": "up2jj/wuko", "run_id": "77", "job": "action (ubuntu-latest)",
	})
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor, Env: map[string]string{"GH_TOKEN": "token"}})
	if err != nil {
		t.Fatal(err)
	}
	assertArgs(t, executor.calls[1], "run", "view", "77", "--json", runJSONFields+",jobs", "--repo", "up2jj/wuko")
	assertArgs(t, executor.calls[2], "api", "--allow-escape-sequences", "repos/{owner}/{repo}/actions/jobs/101/logs")
	if executor.calls[2].Env["GH_REPO"] != "up2jj/wuko" || executor.calls[2].Env["GH_TOKEN"] != "token" {
		t.Fatalf("api environment = %#v", executor.calls[2].Env)
	}
	if result.Outputs["log"] != rawLog || result.Outputs["log_scope"] != "job" || result.Outputs["job_id"] != int64(101) || result.Outputs["job"] != "action (ubuntu-latest)" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if executor.calls[2].CaptureLimit != logCaptureLimit {
		t.Fatalf("job log capture limit = %d, want %d", executor.calls[2].CaptureLimit, int64(logCaptureLimit))
	}
	if result.Outputs["log_truncated"] != false {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if _, exists := result.Outputs["step"]; exists {
		t.Fatalf("step output unexpectedly present: %#v", result.Outputs)
	}
}

func TestRunReportsTruncatedJobLog(t *testing.T) {
	view := `{"databaseId":77,"status":"completed","conclusion":"success","jobs":[{"databaseId":101,"name":"test"}]}`
	rawLog := "\ufeff2026-09-05T18:18:46.6404147Z job log"
	executor := &recordingExecutor{steps: []recordedCall{
		{}, {result: process.Result{Stdout: view}},
		{result: process.Result{Stdout: rawLog, StdoutTruncated: true}},
	}}
	runnerValue := newRunner(t, map[string]any{"operation": "watch", "run_id": "77", "job": "test"})
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["log"] != rawLog || result.Outputs["log_truncated"] != true {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestRunReportsTruncationWhenStepScopingFails(t *testing.T) {
	view := `{"databaseId":77,"status":"completed","conclusion":"success","jobs":[{"databaseId":101,"name":"test","steps":[{"name":"build","startedAt":"2026-09-05T18:18:50Z","completedAt":"2026-09-05T18:18:52Z"}]}]}`
	executor := &recordingExecutor{steps: []recordedCall{
		{}, {result: process.Result{Stdout: view}},
		{result: process.Result{Stdout: "2026-09-05T18:18:40.000Z earlier\n", StdoutTruncated: true}},
	}}
	runnerValue := newRunner(t, map[string]any{"operation": "watch", "run_id": "77", "job": "test", "step": "build"})
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor})
	if err == nil || !strings.Contains(err.Error(), "job log was truncated") || !strings.Contains(err.Error(), "no lines") || result.Outputs != nil {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestRunFailsWhenJobLogIsUnavailable(t *testing.T) {
	view := `{"databaseId":77,"status":"completed","conclusion":"success","jobs":[{"databaseId":101,"name":"test"}]}`
	executor := &recordingExecutor{steps: []recordedCall{
		{},
		{result: process.Result{Stdout: view}},
		{result: process.Result{Stderr: "log has expired\n"}, err: errors.New("exit status 1")},
	}}
	runnerValue := newRunner(t, map[string]any{"operation": "watch", "run_id": "77", "job": "test"})
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor})
	if err == nil || !strings.Contains(err.Error(), "downloading GitHub Actions job log") || !strings.Contains(err.Error(), "log has expired") || result.Outputs != nil {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestRunReturnsTimestampScopedStepLog(t *testing.T) {
	view := `{"databaseId":77,"status":"completed","conclusion":"success","jobs":[{"databaseId":101,"name":"action (ubuntu-latest)","steps":[{"name":"build action test binary","number":4,"startedAt":"2026-09-05T18:18:50Z","completedAt":"2026-09-05T18:18:59Z","status":"completed","conclusion":"success"}]}]}`
	rawLog := "\ufeff2026-09-05T18:18:49.9000000Z before\n" +
		"2026-09-05T18:18:50.9000000Z shared start bucket\n" +
		"2026-09-05T18:18:51.0000000Z build\n" +
		"continued output\n" +
		"2026-09-05T18:18:59.9999999Z shared completion bucket\n" +
		"2026-09-05T18:19:00.0000000Z after\n"
	wantLog := "2026-09-05T18:18:50.9000000Z shared start bucket\n" +
		"2026-09-05T18:18:51.0000000Z build\n" +
		"continued output\n" +
		"2026-09-05T18:18:59.9999999Z shared completion bucket\n"
	executor := &recordingExecutor{steps: []recordedCall{{}, {result: process.Result{Stdout: view}}, {result: process.Result{Stdout: rawLog}}}}
	runnerValue := newRunner(t, map[string]any{
		"operation": "watch", "run_id": "77", "job": "action (ubuntu-latest)", "step": "build action test binary",
	})
	result, err := runnerValue.Run(t.Context(), step.Request{Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["log"] != wantLog || result.Outputs["log_scope"] != "step" || result.Outputs["step"] != "build action test binary" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestJobAndStepSelectionFailClosed(t *testing.T) {
	jobs := []jobRecord{
		{DatabaseID: 1, Name: "test", Steps: []stepRecord{{Name: "build"}, {Name: "build"}}},
		{DatabaseID: 2, Name: "test"},
	}
	if _, err := selectJob(jobs, "missing"); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing job error = %v", err)
	}
	if _, err := selectJob(jobs, "test"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate job error = %v", err)
	}
	if _, err := selectStep(jobs[0].Steps, "missing"); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing step error = %v", err)
	}
	if _, err := selectStep(jobs[0].Steps, "build"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate step error = %v", err)
	}
}

func TestScopeStepLogKeepsSubSecondSteps(t *testing.T) {
	log := "2026-09-05T18:19:03.9000000Z earlier\n" +
		"2026-09-05T18:19:04.3000000Z run\n" +
		"2026-09-05T18:19:04.9000000Z done\n" +
		"2026-09-05T18:19:05.0000000Z later\n"
	want := "2026-09-05T18:19:04.3000000Z run\n2026-09-05T18:19:04.9000000Z done\n"
	scoped, err := scopeStepLog(log, stepRecord{StartedAt: "2026-09-05T18:19:04Z", CompletedAt: "2026-09-05T18:19:04Z"})
	if err != nil || scoped != want {
		t.Fatalf("scopeStepLog() = %q, %v; want %q", scoped, err, want)
	}
}

func TestScopeStepLogRejectsUnusableBoundaries(t *testing.T) {
	validLog := "2026-09-05T18:18:50.500Z output\n"
	tests := []struct {
		name string
		log  string
		step stepRecord
		want string
	}{
		{name: "invalid start", log: validLog, step: stepRecord{StartedAt: "bad", CompletedAt: "2026-09-05T18:18:51Z"}, want: "parsing startedAt"},
		{name: "invalid completion", log: validLog, step: stepRecord{StartedAt: "2026-09-05T18:18:50Z", CompletedAt: "bad"}, want: "parsing completedAt"},
		{name: "reversed interval", log: validLog, step: stepRecord{StartedAt: "2026-09-05T18:18:51Z", CompletedAt: "2026-09-05T18:18:50Z"}, want: "must not complete before"},
		{name: "skipped step", log: validLog, step: stepRecord{Status: "completed", Conclusion: "skipped"}, want: "parsing startedAt"},
		{name: "no timestamps", log: "plain output\n", step: stepRecord{StartedAt: "2026-09-05T18:18:50Z", CompletedAt: "2026-09-05T18:18:52Z"}, want: "starts without"},
		{name: "no lines in interval", log: "2026-09-05T18:18:49Z before\n", step: stepRecord{StartedAt: "2026-09-05T18:18:50Z", CompletedAt: "2026-09-05T18:18:52Z"}, want: "no lines"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := scopeStepLog(test.log, test.step); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("scopeStepLog() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunRejectsUnresolvedTemplate(t *testing.T) {
	runnerValue := newRunner(t, map[string]any{"operation": "watch", "workflow": "ci", "job": "{{ .vars.job }}"})
	result, err := runnerValue.Run(t.Context(), step.Request{})
	if err == nil || !strings.Contains(err.Error(), "unresolved template") || result.Outputs != nil {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestRunPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	executor := executorFunc(func(context.Context, process.Options) (process.Result, error) {
		cancel()
		return process.Result{}, context.Canceled
	})
	runnerValue := newRunner(t, map[string]any{"operation": "watch", "run_id": "77"})
	result, err := runnerValue.Run(ctx, step.Request{Executor: executor})
	if !errors.Is(err, context.Canceled) || result.Outputs != nil {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func watchConfig() map[string]any { return map[string]any{"operation": "watch"} }

func newRunner(t *testing.T, raw map[string]any) step.Runner {
	t.Helper()
	runner, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func assertArgs(t *testing.T, call process.Options, want ...string) {
	t.Helper()
	if call.Command != "gh" || !slices.Equal(call.Args, want) {
		t.Fatalf("command = %q, args = %#v, want gh %#v", call.Command, call.Args, want)
	}
}
