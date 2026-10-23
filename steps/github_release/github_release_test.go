package githubrelease

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/process/processtest"
	"github.com/up2jj/wuko/step"
)

func TestNewValidatesConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "missing operation", raw: map[string]any{"repository": "acme/wuko"}, want: "operation is required"},
		{name: "unsupported operation", raw: map[string]any{"operation": "find", "repository": "acme/wuko"}, want: `operation must be "check_drift"`},
		{name: "missing repository", raw: map[string]any{"operation": operationCheckDrift}, want: "repository is required"},
		{name: "blank repository", raw: map[string]any{"operation": operationCheckDrift, "repository": "  "}, want: "repository is required"},
		{name: "invalid repository", raw: map[string]any{"operation": operationCheckDrift, "repository": "acme"}, want: "owner/repository"},
		{name: "repository whitespace", raw: map[string]any{"operation": operationCheckDrift, "repository": "acme/wuko project"}, want: "whitespace"},
		{name: "unknown field", raw: map[string]any{"operation": operationCheckDrift, "repository": "acme/wuko", "unknown": true}, want: "field"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.raw); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New(%#v) error = %v, want %q", tt.raw, err, tt.want)
			}
		})
	}
}

func TestRunReportsCurrentRepositoryAndUsesDefaultBranch(t *testing.T) {
	executor := &processtest.Executor{Strict: true, Replies: []processtest.Reply{
		{Result: process.Result{Stdout: `{"default_branch":"main"}`}},
		{Result: process.Result{Stdout: `{"tag_name":"v1.2.3","html_url":"https://github.com/acme/wuko/releases/tag/v1.2.3","published_at":"2026-08-20T10:00:00Z"}`}},
		{Result: process.Result{Stdout: `{"html_url":"https://github.com/acme/wuko/compare/v1.2.3...main","ahead_by":0,"behind_by":2,"total_commits":0}`}},
	}}
	runner := steptest.Build(t, New, map[string]any{"operation": operationCheckDrift, "repository": "acme/wuko"})
	result, err := runner.Run(t.Context(), step.Request{
		Executor: executor, RunDir: "/workspace", Env: map[string]string{"GITHUB_TOKEN": "secret"},
		Attempt: 2, MaxAttempts: 3, OperationID: "release-check",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.Calls()) != 3 {
		t.Fatalf("calls = %d, want 3", len(executor.Calls()))
	}
	wantArgs := [][]string{
		{"api", "repos/acme/wuko"},
		{"api", "repos/acme/wuko/releases/latest"},
		{"api", "repos/acme/wuko/compare/v1.2.3...main"},
	}
	for index, want := range wantArgs {
		if executor.Calls()[index].Command != "gh" || !slices.Equal(executor.Calls()[index].Args, want) || executor.Calls()[index].Dir != "/workspace" {
			t.Fatalf("call %d = %#v, want args %#v", index, executor.Calls()[index], want)
		}
	}
	if executor.Calls()[0].Env[step.AttemptEnv] != "2" || executor.Calls()[0].Env[step.MaxAttemptsEnv] != "3" || executor.Calls()[0].Env[step.OperationIDEnv] != "release-check" {
		t.Fatalf("environment = %#v", executor.Calls()[0].Env)
	}
	want := map[string]any{
		"repository":    "acme/wuko",
		"found":         true,
		"status":        "current",
		"has_changes":   false,
		"release_tag":   "v1.2.3",
		"release_url":   "https://github.com/acme/wuko/releases/tag/v1.2.3",
		"published_at":  "2026-08-20T10:00:00Z",
		"branch":        "main",
		"ahead_by":      0,
		"behind_by":     2,
		"total_commits": 0,
		"compare_url":   "https://github.com/acme/wuko/compare/v1.2.3...main",
	}
	for key, value := range want {
		if result.Outputs[key] != value {
			t.Fatalf("outputs[%q] = %#v, want %#v; outputs = %#v", key, result.Outputs[key], value, result.Outputs)
		}
	}
}

func TestRunReportsChangedRepository(t *testing.T) {
	executor := &processtest.Executor{Strict: true, Replies: []processtest.Reply{
		{Result: process.Result{Stdout: `{"default_branch":"trunk"}`}},
		{Result: process.Result{Stdout: `{"tag_name":"v2.0.0"}`}},
		{Result: process.Result{Stdout: `{"ahead_by":4,"behind_by":0,"total_commits":4}`}},
	}}
	runner := steptest.Build(t, New, map[string]any{"operation": operationCheckDrift, "repository": "acme/project"})
	result, err := runner.Run(t.Context(), step.Request{Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["status"] != "changed" || result.Outputs["has_changes"] != true || result.Outputs["ahead_by"] != 4 || result.Outputs["total_commits"] != 4 {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestRunReportsRepositoryWithoutStableRelease(t *testing.T) {
	executor := &processtest.Executor{Strict: true, Replies: []processtest.Reply{
		{Result: process.Result{Stdout: `{"default_branch":"main"}`}},
		{Result: process.Result{Stderr: "gh: Not Found (HTTP 404)"}, Err: &process.ExitError{Command: "gh", Code: 1}},
	}}
	runner := steptest.Build(t, New, map[string]any{"operation": operationCheckDrift, "repository": "acme/project"})
	result, err := runner.Run(t.Context(), step.Request{Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.Calls()) != 2 {
		t.Fatalf("calls = %d, want 2", len(executor.Calls()))
	}
	want := noReleaseOutputs("acme/project", "main")
	for key, value := range want {
		if result.Outputs[key] != value {
			t.Fatalf("outputs[%q] = %#v, want %#v; outputs = %#v", key, result.Outputs[key], value, result.Outputs)
		}
	}
}

func TestRunReportsMalformedResponsesAndCommandFailures(t *testing.T) {
	tests := []struct {
		name  string
		steps []processtest.Reply
		want  string
	}{
		{
			name:  "malformed repository JSON",
			steps: []processtest.Reply{{Result: process.Result{Stdout: "not json"}}},
			want:  "decoding GitHub repository",
		},
		{
			name:  "repository command failure",
			steps: []processtest.Reply{{Result: process.Result{Stderr: "not logged in"}, Err: &process.ExitError{Command: "gh", Code: 4}}},
			want:  "reading GitHub repository: not logged in",
		},
		{
			name:  "missing gh",
			steps: []processtest.Reply{{Err: errors.New(`starting gh: executable file not found in $PATH`)}},
			want:  "reading GitHub repository: gh api repos/acme/project: starting gh",
		},
		{
			name:  "malformed release JSON",
			steps: []processtest.Reply{{Result: process.Result{Stdout: `{"default_branch":"main"}`}}, {Result: process.Result{Stdout: "not json"}}},
			want:  "decoding latest GitHub release",
		},
		{
			name:  "release command failure",
			steps: []processtest.Reply{{Result: process.Result{Stdout: `{"default_branch":"main"}`}}, {Result: process.Result{Stderr: "permission denied"}, Err: &process.ExitError{Command: "gh", Code: 4}}},
			want:  "reading latest GitHub release: permission denied",
		},
		{
			name:  "malformed comparison JSON",
			steps: []processtest.Reply{{Result: process.Result{Stdout: `{"default_branch":"main"}`}}, {Result: process.Result{Stdout: `{"tag_name":"v1.0.0"}`}}, {Result: process.Result{Stdout: "not json"}}},
			want:  "decoding GitHub comparison",
		},
		{
			name:  "comparison command failure",
			steps: []processtest.Reply{{Result: process.Result{Stdout: `{"default_branch":"main"}`}}, {Result: process.Result{Stdout: `{"tag_name":"v1.0.0"}`}}, {Result: process.Result{Stderr: "comparison failed"}, Err: &process.ExitError{Command: "gh", Code: 1}}},
			want:  "comparing GitHub release: comparison failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := steptest.Build(t, New, map[string]any{"operation": operationCheckDrift, "repository": "acme/project"})
			executor := &processtest.Executor{Strict: true, Replies: tt.steps}
			result, err := runner.Run(t.Context(), step.Request{Executor: executor})
			if err == nil || !strings.Contains(err.Error(), tt.want) || result.Outputs != nil {
				t.Fatalf("result = %#v, error = %v, want %q", result, err, tt.want)
			}
		})
	}
}

func TestRunPreservesCancellation(t *testing.T) {
	executor := &processtest.Executor{Strict: true, Replies: []processtest.Reply{{Err: context.Canceled}}}
	runner := steptest.Build(t, New, map[string]any{"operation": operationCheckDrift, "repository": "acme/project"})
	result, err := runner.Run(t.Context(), step.Request{Executor: executor})
	if !errors.Is(err, context.Canceled) || result.Outputs != nil {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestRunRejectsUnresolvedRepositoryTemplate(t *testing.T) {
	runner := steptest.Build(t, New, map[string]any{"operation": operationCheckDrift, "repository": "{{ .vars.repository }}"})
	if _, err := runner.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "unresolved template") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestNewAcceptsTemplatedRepository(t *testing.T) {
	if _, err := New(map[string]any{"operation": operationCheckDrift, "repository": "{{ .vars.repository }}"}); err != nil {
		t.Fatal(err)
	}
}
