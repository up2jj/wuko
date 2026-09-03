package git

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"gopkg.in/yaml.v3"
)

type scriptedGitResult struct {
	result process.Result
	err    error
}

type scriptedGitExecutor struct {
	calls   []process.Options
	results []scriptedGitResult
}

func (executor *scriptedGitExecutor) Run(_ context.Context, options process.Options) (process.Result, error) {
	options.Args = slices.Clone(options.Args)
	options.Env = maps.Clone(options.Env)
	executor.calls = append(executor.calls, options)
	index := len(executor.calls) - 1
	if index >= len(executor.results) {
		return process.Result{}, nil
	}
	return executor.results[index].result, executor.results[index].err
}

func TestNewCommitValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"missing message", map[string]any{}, "message is required"},
		{"blank message", map[string]any{"message": "  "}, "message is required"},
		{"message NUL", map[string]any{"message": "bad\x00message"}, "must not contain NUL"},
		{"blank body", map[string]any{"message": "subject", "body": "  "}, "body must not be blank"},
		{"empty paths", map[string]any{"message": "subject", "paths": []any{}}, "at least one pathspec"},
		{"blank path", map[string]any{"message": "subject", "paths": []any{"ok", " "}}, "paths[1]"},
		{"null author", map[string]any{"message": "subject", "author": nil}, "author must be an object"},
		{"author name", map[string]any{"message": "subject", "author": map[string]any{"email": "bot@example.com"}}, "author name is required"},
		{"author email", map[string]any{"message": "subject", "author": map[string]any{"name": "Bot"}}, "author email is required"},
		{"author line break", map[string]any{"message": "subject", "author": map[string]any{"name": "Bad\nBot", "email": "bot@example.com"}}, "single line"},
		{"committer email", map[string]any{"message": "subject", "committer": map[string]any{"name": "Bot"}}, "committer email is required"},
		{"empty trailers", map[string]any{"message": "subject", "trailers": []any{}}, "at least one trailer"},
		{"trailer token", map[string]any{"message": "subject", "trailers": []any{map[string]any{"value": "WUKO-1"}}}, "token is required"},
		{"trailer separator", map[string]any{"message": "subject", "trailers": []any{map[string]any{"token": "Refs:", "value": "WUKO-1"}}}, "must not contain"},
		{"trailer value", map[string]any{"message": "subject", "trailers": []any{map[string]any{"token": "Refs"}}}, "value is required"},
		{"trailer multiline value", map[string]any{"message": "subject", "trailers": []any{map[string]any{"token": "Refs", "value": "one\ntwo"}}}, "single line"},
		{"templated empty policy", map[string]any{"message": "subject", "on_empty": "{{ .vars.policy }}"}, "must not be templated"},
		{"blank empty policy", map[string]any{"message": "subject", "on_empty": ""}, "must not be blank"},
		{"empty policy", map[string]any{"message": "subject", "on_empty": "ignore"}, "skip, fail, or commit"},
		{"null signoff", map[string]any{"message": "subject", "signoff": nil}, "signoff must be a boolean"},
		{"null verify", map[string]any{"message": "subject", "verify": nil}, "verify must be a boolean"},
		{"unknown field", map[string]any{"message": "subject", "unknown": true}, "field unknown"},
		{"unknown identity field", map[string]any{"message": "subject", "author": map[string]any{"name": "Bot", "email": "bot@example.com", "login": "bot"}}, "field login"},
		{"unknown trailer field", map[string]any{"message": "subject", "trailers": []any{map[string]any{"token": "Refs", "value": "1", "extra": true}}}, "field extra"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCommit(tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewCommit(%#v) error = %v, want %q", tt.raw, err, tt.want)
			}
		})
	}
}

func TestCommitUsesExecutorWithStructuredConfiguration(t *testing.T) {
	diff := &process.ExitError{Command: "git", Code: 1}
	executor := &scriptedGitExecutor{results: []scriptedGitResult{
		{},
		{err: diff},
		{},
		{result: process.Result{Stdout: "abc123\n"}},
	}}
	runner, err := NewCommit(map[string]any{
		"message": "feat: add commit", "body": "Body text.", "paths": []any{"src", "-literal"},
		"trailers": []any{
			map[string]any{"token": "Refs", "value": "WUKO-1"},
			map[string]any{"token": "Refs", "value": "WUKO-2"},
		},
		"author":    map[string]any{"name": "Author", "email": "author@example.com"},
		"committer": map[string]any{"name": "Committer", "email": "committer@example.com"},
		"signoff":   true,
		"verify":    false,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		Executor: executor, RunDir: "/repo", Env: map[string]string{"MODE": "test", "GIT_AUTHOR_NAME": "Old"},
		Attempt: 2, MaxAttempts: 3, OperationID: "commit-op",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.calls) != 4 {
		t.Fatalf("calls = %#v", executor.calls)
	}
	wantArgs := [][]string{
		{"add", "-A", "--", "src", "-literal"},
		{"diff", "--cached", "--quiet", "--exit-code", "--"},
		{"commit", "--signoff", "--no-verify", "-m", "feat: add commit", "-m", "Body text.", "--trailer", "Refs=WUKO-1", "--trailer", "Refs=WUKO-2"},
		{"rev-parse", "--verify", "--quiet", "HEAD^{commit}"},
	}
	for index, want := range wantArgs {
		if executor.calls[index].Command != "git" || executor.calls[index].Dir != "/repo" || !slices.Equal(executor.calls[index].Args, want) {
			t.Errorf("call %d = %#v, want args %#v", index, executor.calls[index], want)
		}
		if executor.calls[index].Env["MODE"] != "test" || executor.calls[index].Env[step.AttemptEnv] != "2" || executor.calls[index].Env[step.OperationIDEnv] != "commit-op" {
			t.Errorf("call %d environment = %#v", index, executor.calls[index].Env)
		}
	}
	commitEnv := executor.calls[2].Env
	if commitEnv["GIT_AUTHOR_NAME"] != "Author" || commitEnv["GIT_AUTHOR_EMAIL"] != "author@example.com" ||
		commitEnv["GIT_COMMITTER_NAME"] != "Committer" || commitEnv["GIT_COMMITTER_EMAIL"] != "committer@example.com" {
		t.Fatalf("commit environment = %#v", commitEnv)
	}
	for _, index := range []int{0, 1, 3} {
		if executor.calls[index].Env["GIT_AUTHOR_NAME"] != "Old" {
			t.Fatalf("identity leaked to call %d: %#v", index, executor.calls[index].Env)
		}
	}
	if result.Outputs["created"] != true || result.Outputs["commit"] != "abc123" {
		t.Fatalf("result = %#v", result)
	}
	if _, ok := runner.(step.ExecutorAware); !ok {
		t.Fatal("commit runner is not executor-aware")
	}
}

func TestCommitEmptyPolicies(t *testing.T) {
	tests := []struct {
		name        string
		onEmpty     string
		results     []scriptedGitResult
		wantArgs    [][]string
		wantCreated bool
		wantCommit  string
		wantError   string
	}{
		{
			name: "default skip", results: []scriptedGitResult{{}, {result: process.Result{Stdout: "current\n"}}},
			wantArgs: [][]string{{"diff", "--cached", "--quiet", "--exit-code", "--"}, {"rev-parse", "--verify", "--quiet", "HEAD^{commit}"}}, wantCommit: "current",
		},
		{
			name: "skip unborn", results: []scriptedGitResult{{}, {err: &process.ExitError{Command: "git", Code: 1}}},
			wantArgs: [][]string{{"diff", "--cached", "--quiet", "--exit-code", "--"}, {"rev-parse", "--verify", "--quiet", "HEAD^{commit}"}},
		},
		{
			name: "fail", onEmpty: "fail", results: []scriptedGitResult{{}},
			wantArgs: [][]string{{"diff", "--cached", "--quiet", "--exit-code", "--"}}, wantError: "nothing to commit",
		},
		{
			name: "commit", onEmpty: "commit", results: []scriptedGitResult{{}, {}, {result: process.Result{Stdout: "empty\n"}}},
			wantArgs:    [][]string{{"diff", "--cached", "--quiet", "--exit-code", "--"}, {"commit", "--allow-empty", "-m", "subject"}, {"rev-parse", "--verify", "--quiet", "HEAD^{commit}"}},
			wantCreated: true, wantCommit: "empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := map[string]any{"message": "subject"}
			if tt.onEmpty != "" {
				raw["on_empty"] = tt.onEmpty
			}
			runner, err := NewCommit(raw)
			if err != nil {
				t.Fatal(err)
			}
			executor := &scriptedGitExecutor{results: tt.results}
			result, err := runner.Run(t.Context(), step.Request{Executor: executor})
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v, want %q", err, tt.wantError)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(executor.calls) != len(tt.wantArgs) {
				t.Fatalf("calls = %#v, want %#v", executor.calls, tt.wantArgs)
			}
			for index, want := range tt.wantArgs {
				if !slices.Equal(executor.calls[index].Args, want) {
					t.Errorf("call %d args = %#v, want %#v", index, executor.calls[index].Args, want)
				}
			}
			if tt.wantError == "" && (result.Outputs["created"] != tt.wantCreated || result.Outputs["commit"] != tt.wantCommit) {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestCommitCreatesStructuredCommitAndStagesWholeIndex(t *testing.T) {
	dir := initGitRepository(t)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "README.md")
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner, err := NewCommit(map[string]any{
		"message": "feat: structured commit", "body": "Body text.", "paths": []any{"new.txt"},
		"author":    map[string]any{"name": "Original Author", "email": "author@example.test"},
		"committer": map[string]any{"name": "Commit Bot", "email": "bot@example.test"},
		"signoff":   true,
		"trailers": []any{
			map[string]any{"token": "Refs", "value": "WUKO-1"},
			map[string]any{"token": "Co-authored-by", "value": "Jane <jane@example.test>"},
			map[string]any{"token": "Co-authored-by", "value": "John <john@example.test>"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	if result.Outputs["created"] != true || result.Outputs["commit"] != commit {
		t.Fatalf("result = %#v, HEAD = %q", result, commit)
	}
	metadata := runGitTest(t, dir, "show", "-s", "--format=%an|%ae|%cn|%ce%n%B", "HEAD")
	for _, want := range []string{
		"Original Author|author@example.test|Commit Bot|bot@example.test",
		"feat: structured commit\n\nBody text.",
		"Signed-off-by: Commit Bot <bot@example.test>",
		"Refs: WUKO-1",
		"Co-authored-by: Jane <jane@example.test>",
		"Co-authored-by: John <john@example.test>",
	} {
		if !strings.Contains(metadata, want) {
			t.Errorf("commit metadata does not contain %q:\n%s", want, metadata)
		}
	}
	changed := strings.Fields(runGitTest(t, dir, "show", "--pretty=format:", "--name-only", "HEAD"))
	if !slices.Equal(changed, []string{"README.md", "new.txt"}) {
		t.Fatalf("committed paths = %#v", changed)
	}
}

func TestCommitSkipsIdempotentlyAndCanCreateEmptyCommit(t *testing.T) {
	dir := initGitRepository(t)
	before := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	skip, err := NewCommit(map[string]any{"message": "no changes"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := skip.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["created"] != false || result.Outputs["commit"] != before {
		t.Fatalf("skip result = %#v", result)
	}
	fail, err := NewCommit(map[string]any{"message": "no changes", "on_empty": "fail"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fail.Run(t.Context(), step.Request{RunDir: dir}); err == nil || !strings.Contains(err.Error(), "nothing to commit") {
		t.Fatalf("on_empty fail error = %v", err)
	}

	empty, err := NewCommit(map[string]any{"message": "empty", "on_empty": "commit"})
	if err != nil {
		t.Fatal(err)
	}
	result, err = empty.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	after := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	if result.Outputs["created"] != true || result.Outputs["commit"] != after || after == before {
		t.Fatalf("empty result = %#v, before = %q, after = %q", result, before, after)
	}
}

func TestCommitStagesTrackedDeletion(t *testing.T) {
	dir := initGitRepository(t)
	if err := os.Remove(filepath.Join(dir, "README.md")); err != nil {
		t.Fatal(err)
	}
	runner, err := NewCommit(map[string]any{"message": "remove README", "paths": []any{"README.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{RunDir: dir}); err != nil {
		t.Fatal(err)
	}
	if tracked := strings.TrimSpace(runGitTest(t, dir, "ls-files", "--", "README.md")); tracked != "" {
		t.Fatalf("deleted path is still tracked: %q", tracked)
	}
}

func TestCommitIgnoresPathsThatMatchNothing(t *testing.T) {
	dir := initGitRepository(t)
	before := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	skip, err := NewCommit(map[string]any{"message": "no output", "paths": []any{"dist", "generated/*.go"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := skip.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["created"] != false || result.Outputs["commit"] != before {
		t.Fatalf("unmatched paths result = %#v", result)
	}

	fail, err := NewCommit(map[string]any{"message": "no output", "paths": []any{"dist"}, "on_empty": "fail"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fail.Run(t.Context(), step.Request{RunDir: dir}); err == nil || !strings.Contains(err.Error(), "nothing to commit") {
		t.Fatalf("on_empty fail error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "present.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mixed, err := NewCommit(map[string]any{"message": "add present", "paths": []any{"dist", "present.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err = mixed.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	after := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	if result.Outputs["created"] != true || result.Outputs["commit"] != after || after == before {
		t.Fatalf("mixed paths result = %#v", result)
	}
	if tracked := strings.TrimSpace(runGitTest(t, dir, "ls-files", "--", "present.txt")); tracked != "present.txt" {
		t.Fatalf("present.txt is not tracked: %q", tracked)
	}
}

func TestCommitReportsMissingGitIdentity(t *testing.T) {
	dir := t.TempDir()
	runGitTest(t, dir, "init", "--quiet")
	runGitTest(t, dir, "config", "user.useConfigOnly", "true")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner, err := NewCommit(map[string]any{"message": "initial", "paths": []any{"file.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{RunDir: dir, Env: map[string]string{
		"HOME": t.TempDir(), "XDG_CONFIG_HOME": t.TempDir(), "GIT_CONFIG_NOSYSTEM": "1",
	}})
	if err == nil || !strings.Contains(err.Error(), "creating Git commit") {
		t.Fatalf("missing identity error = %v", err)
	}
}

func TestCommitVerifyControlsHooks(t *testing.T) {
	dir := initGitRepository(t)
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hooked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	verified, err := NewCommit(map[string]any{"message": "hooks run", "paths": []any{"README.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verified.Run(t.Context(), step.Request{RunDir: dir}); err == nil || !strings.Contains(err.Error(), "creating Git commit") {
		t.Fatalf("verified commit error = %v", err)
	}
	bypassed, err := NewCommit(map[string]any{"message": "hooks bypassed", "verify": false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bypassed.Run(t.Context(), step.Request{RunDir: dir}); err != nil {
		t.Fatal(err)
	}
}

func TestCommitPropagatesCancellationAndGitFailures(t *testing.T) {
	runner, err := NewCommit(map[string]any{"message": "subject"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runner.Run(ctx, step.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	executor := &scriptedGitExecutor{results: []scriptedGitResult{{
		result: process.Result{Stderr: "fatal: not a repository\n"}, err: &process.ExitError{Command: "git", Code: 128},
	}}}
	if _, err := runner.Run(t.Context(), step.Request{Executor: executor}); err == nil || !strings.Contains(err.Error(), "inspecting staged Git changes: fatal: not a repository") {
		t.Fatalf("Git failure = %v", err)
	}
}

func TestCommitDocumentationExamples(t *testing.T) {
	data, err := os.ReadFile("../../docs/steps-system.md")
	if err != nil {
		t.Fatal(err)
	}
	section := regexp.MustCompile(`(?s)## \x60git_commit\x60\n(.*?)\n## \x60git_clean\x60`).FindSubmatch(data)
	if section == nil {
		t.Fatal("git_commit documentation section not found")
	}
	blocks := regexp.MustCompile("(?s)```yaml\\n(.*?)```").FindAllSubmatch(section[1], -1)
	if len(blocks) < 5 {
		t.Fatalf("found %d YAML examples, want at least 5", len(blocks))
	}
	for blockIndex, block := range blocks {
		var steps []struct {
			Type string         `yaml:"type"`
			With map[string]any `yaml:"with"`
		}
		if err := yaml.Unmarshal(block[1], &steps); err != nil {
			t.Fatalf("example %d: %v", blockIndex+1, err)
		}
		for stepIndex, documented := range steps {
			var buildErr error
			switch documented.Type {
			case "git_commit":
				_, buildErr = NewCommit(documented.With)
			case "git_conventional_commit":
				_, buildErr = NewConventionalCommit(documented.With)
			default:
				buildErr = errors.New("unexpected documented step type")
			}
			if buildErr != nil {
				t.Fatalf("example %d step %d (%s): %v", blockIndex+1, stepIndex+1, documented.Type, buildErr)
			}
		}
	}
}
