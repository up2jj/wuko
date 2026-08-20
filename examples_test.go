package main

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/agent"
	"github.com/up2jj/wuko/steps/choice"
	inputstep "github.com/up2jj/wuko/steps/input"
	luastep "github.com/up2jj/wuko/steps/lua"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

func TestClickUpTaskExampleValidates(t *testing.T) {
	definition := loadClickUpTaskExample(t)
	registry := clickUpExampleRegistry(t)
	if err := engine.New(registry).Validate(t.Context(), definition, engine.Options{RunDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}

	claude := exampleStep(t, definition, "claude")
	if claude.Type != "agent" || claude.If != `vars.agent == "claude"` {
		t.Fatalf("claude step = %#v", claude)
	}
	codex := exampleStep(t, definition, "codex")
	if codex.Type != "agent" || codex.If != `vars.agent == "codex"` {
		t.Fatalf("codex step = %#v", codex)
	}
}

func TestClickUpTaskExampleFetchesAndWritesMarkdown(t *testing.T) {
	tests := []struct {
		name        string
		teamID      string
		response    string
		wantCustom  bool
		wantContent string
	}{
		{
			name:        "native task ID with empty description",
			response:    `{"name":"Fix Login!","url":"https://app.clickup.com/t/TASK-42"}`,
			wantContent: "_No description provided._",
		},
		{
			name:        "custom task ID with markdown description",
			teamID:      "123456",
			response:    `{"name":"Fix Login!","url":"https://app.clickup.com/t/TEAM-42","markdown_description":"## Acceptance criteria\n\n- It works"}`,
			wantCustom:  true,
			wantContent: "## Acceptance criteria\n\n- It works",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/api/v2/task/TEAM-42" {
					t.Errorf("path = %q", request.URL.Path)
				}
				query := request.URL.Query()
				if query.Get("include_markdown_description") != "true" {
					t.Errorf("include_markdown_description = %q", query.Get("include_markdown_description"))
				}
				if got := query.Get("custom_task_ids") == "true"; got != tt.wantCustom {
					t.Errorf("custom_task_ids present = %v, want %v", got, tt.wantCustom)
				}
				if got := query.Get("team_id"); got != tt.teamID {
					t.Errorf("team_id = %q, want %q", got, tt.teamID)
				}
				if got := request.Header.Get("Authorization"); got != "secret-token" {
					t.Errorf("Authorization = %q", got)
				}
				writer.Header().Set("Content-Type", "application/json")
				fmt.Fprint(writer, tt.response)
			}))
			defer server.Close()

			runner := clickUpFetchRunner(t, server.URL+"/api/v2")
			runDir := t.TempDir()
			environment := map[string]string{"CLICKUP_TOKEN": "secret-token"}
			if tt.teamID != "" {
				environment["CLICKUP_TEAM_ID"] = tt.teamID
			}
			result, err := runner.Run(t.Context(), step.Request{
				RunDir: runDir, Env: environment, Stdout: io.Discard, Stderr: io.Discard,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Outputs["branch"]; got != "TEAM-42_fix-login" {
				t.Errorf("branch = %q", got)
			}
			if got := result.Outputs["markdown_path"]; got != ".wuko/context/TEAM-42.md" {
				t.Errorf("markdown_path = %q", got)
			}
			data, err := os.ReadFile(filepath.Join(runDir, ".wuko", "context", "TEAM-42.md"))
			if err != nil {
				t.Fatal(err)
			}
			content := string(data)
			for _, want := range []string{"# Fix Login!", "**Task ID:** `TEAM-42`", tt.wantContent} {
				if !strings.Contains(content, want) {
					t.Errorf("markdown does not contain %q:\n%s", want, content)
				}
			}
		})
	}
}

func TestClickUpTaskExampleFetchErrors(t *testing.T) {
	runner := clickUpFetchRunner(t, "http://127.0.0.1:1")
	_, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir(), Env: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "CLICKUP_TOKEN is required") {
		t.Fatalf("missing token error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	runner = clickUpFetchRunner(t, server.URL)
	_, err = runner.Run(t.Context(), step.Request{
		RunDir: t.TempDir(), Env: map[string]string{"CLICKUP_TOKEN": "secret-token"},
	})
	if err == nil || !strings.Contains(err.Error(), "ClickUp request failed with HTTP 403") {
		t.Fatalf("HTTP error = %v", err)
	}
}

func TestClickUpTaskExampleCreatesSafeBranch(t *testing.T) {
	const branch = "TEAM-42_fix-login"
	tests := []struct {
		name      string
		branch    string
		prepare   func(*testing.T, string)
		wantError string
	}{
		{name: "creates branch"},
		{name: "rejects invalid branch", branch: "bad branch", wantError: "generated branch name is invalid"},
		{
			name: "rejects dirty tree",
			prepare: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "working tree has uncommitted changes",
		},
		{
			name: "rejects local branch",
			prepare: func(t *testing.T, dir string) {
				runGit(t, dir, "branch", branch)
			},
			wantError: "local branch already exists",
		},
		{
			name: "rejects remote branch",
			prepare: func(t *testing.T, dir string) {
				runGit(t, dir, "update-ref", "refs/remotes/origin/"+branch, "HEAD")
			},
			wantError: "remote branch already exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := initExampleGitRepository(t)
			if tt.prepare != nil {
				tt.prepare(t, dir)
			}
			selectedBranch := tt.branch
			if selectedBranch == "" {
				selectedBranch = branch
			}
			runner := clickUpBranchRunner(t, selectedBranch)
			result, err := runner.Run(t.Context(), step.Request{
				RunDir: dir, Env: map[string]string{"PATH": os.Getenv("PATH")},
				Stdout: io.Discard, Stderr: io.Discard,
			})
			if tt.wantError != "" {
				if err == nil || !strings.Contains(fmt.Sprint(result.Outputs["stderr"]), tt.wantError) {
					t.Fatalf("error = %v, stderr = %q", err, result.Outputs["stderr"])
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(runGit(t, dir, "branch", "--show-current")); got != branch {
				t.Fatalf("current branch = %q", got)
			}
		})
	}
}

func loadClickUpTaskExample(t *testing.T) *workflow.Definition {
	t.Helper()
	definition, err := workflow.NewLoader(nil).Load(t.Context(), filepath.Join("examples", "clickup-task.yaml"), workflow.LoadOptions{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func clickUpExampleRegistry(t *testing.T) *step.Registry {
	t.Helper()
	registry := step.NewRegistry()
	for _, register := range []func(*step.Registry) error{
		inputstep.Register, choice.Register, luastep.Register, shell.Register, agent.Register,
	} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func exampleStep(t *testing.T, definition *workflow.Definition, id string) workflow.Step {
	t.Helper()
	for _, workflowStep := range definition.Steps {
		if workflowStep.ID == id {
			return workflowStep
		}
	}
	t.Fatalf("step %q not found", id)
	return workflow.Step{}
}

func clickUpFetchRunner(t *testing.T, apiBaseURL string) step.Runner {
	t.Helper()
	fetch := exampleStep(t, loadClickUpTaskExample(t), "fetch")
	raw := maps.Clone(fetch.With)
	raw["args"] = map[string]any{"task_id": "TEAM-42", "api_base_url": apiBaseURL}
	runner, err := luastep.New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func clickUpBranchRunner(t *testing.T, branch string) step.Runner {
	t.Helper()
	workflowStep := exampleStep(t, loadClickUpTaskExample(t), "branch")
	raw := maps.Clone(workflowStep.With)
	raw["args"] = []any{branch}
	runner, err := shell.New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func initExampleGitRepository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "config", "user.name", "Wuko Test")
	runGit(t, dir, "config", "user.email", "wuko@example.test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/.wuko/context/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md", ".gitignore")
	runGit(t, dir, "commit", "--quiet", "-m", "initial")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
