package engine

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/step"
	processstep "github.com/up2jj/wuko/steps/process"
	"github.com/up2jj/wuko/steps/shell"
	sshstep "github.com/up2jj/wuko/steps/ssh"
	"github.com/up2jj/wuko/workflow"
)

func TestSSHRestrictionsApplyInsideControlsAndCleanup(t *testing.T) {
	registry := newTestRegistry(t, nil)
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	if err := processstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	executors := executor.NewRegistry()
	if err := sshstep.RegisterExecutor(executors); err != nil {
		t.Fatal(err)
	}
	for _, location := range []string{"steps", "conditional", "directory", "finally", "defer"} {
		t.Run(location, func(t *testing.T) {
			bad := workflow.Step{ID: "bad", Type: "shell", With: map[string]any{"command": "true", "tty": true}}
			block := workflow.Step{Executor: &workflow.ExecutorScope{Type: "ssh", With: map[string]any{"host": "never-connect.invalid", "directory": "/remote"}}}
			switch location {
			case "steps":
				block.Steps = []workflow.Step{bad}
			case "conditional":
				block.Steps = []workflow.Step{{If: workflow.Condition("true"), Steps: []workflow.Step{bad}}}
			case "directory":
				block.Steps = []workflow.Step{{WorkingDirectory: "/remote/nested", Steps: []workflow.Step{bad}}}
			case "finally":
				block.Steps = []workflow.Step{{ID: "ok", Type: "shell", With: map[string]any{"command": "true"}}}
				block.Finally = []workflow.Step{bad}
			case "defer":
				block.Steps = []workflow.Step{{ID: "ok", Type: "shell", With: map[string]any{"command": "true"}, Defer: []workflow.Step{bad}}}
			}
			definition := testDefinition(t, "ssh-invalid", block)
			err := New(registry, WithExecutors(executors)).Validate(t.Context(), definition, Options{RunDir: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), "SSH executor does not support tty") {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
	definition := testDefinition(t, "ssh-process", workflow.Step{
		Executor: &workflow.ExecutorScope{Type: "ssh", With: map[string]any{"host": "never-connect.invalid", "directory": "/remote"}},
		Steps:    []workflow.Step{{ID: "server", Type: "process", With: map[string]any{"command": "sleep", "args": []string{"10"}}}},
	})
	if err := New(registry, WithExecutors(executors)).Validate(t.Context(), definition, Options{RunDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), `step type "process" is not supported`) {
		t.Fatalf("managed process validation = %v", err)
	}
}

type directoryExecutor struct {
	recordingExecutor
	directory string
	statPaths []string
}

func (s *directoryExecutor) Stat(_ context.Context, path string) (executor.FileInfo, error) {
	s.statPaths = append(s.statPaths, path)
	if path != s.directory {
		return executor.FileInfo{}, fs.ErrNotExist
	}
	return executor.FileInfo{Mode: fs.ModeDir | 0o755}, nil
}
func (*directoryExecutor) ReadFile(context.Context, string, int64) ([]byte, error) {
	return nil, fmt.Errorf("unused")
}
func (*directoryExecutor) WriteFile(context.Context, string, []byte, executor.WriteOptions) error {
	return fmt.Errorf("unused")
}
func (*directoryExecutor) MkdirAll(context.Context, string, fs.FileMode) error {
	return fmt.Errorf("unused")
}

type directoryProvider struct{ session *directoryExecutor }

func (p directoryProvider) Open(context.Context, executor.Request) (executor.Session, error) {
	return p.session, nil
}
func (directoryProvider) SupportsFileSystem() bool { return true }

func TestExecutorWorkingDirectoryUsesTargetAndRestoresLocalExecution(t *testing.T) {
	root := t.TempDir()
	remoteOnly := filepath.Join(root, "exists-only-on-target")
	session := &directoryExecutor{directory: remoteOnly}
	registry := newTestRegistry(t, map[string]step.Builder{"capture_dir": func(map[string]any) (step.Runner, error) { return directoryCaptureRunner{}, nil }})
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	executors := executor.NewRegistry()
	if err := executors.Register("target", func(map[string]any) (executor.Provider, error) { return directoryProvider{session}, nil }); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "remote-directory",
		workflow.Step{Executor: &workflow.ExecutorScope{Type: "target"}, Steps: []workflow.Step{{WorkingDirectory: "exists-only-on-target", Steps: []workflow.Step{{ID: "remote", Type: "shell", With: map[string]any{"command": "pwd"}}}}}},
		workflow.Step{ID: "local", Type: "capture_dir"},
	)
	state, err := New(registry, WithExecutors(executors)).Run(t.Context(), definition, Options{RunDir: root, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.statPaths) != 1 || session.statPaths[0] != remoteOnly || session.closed != 1 {
		t.Fatalf("target paths = %v, closes = %d", session.statPaths, session.closed)
	}
	if state.Steps["local"].(map[string]any)["dir"] != root {
		t.Fatalf("local run directory = %#v", state.Steps["local"])
	}
}
