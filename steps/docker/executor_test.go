package docker

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/mount"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
)

func TestDockerExecutorSharesWorkspaceAndRunsCommands(t *testing.T) {
	runDir := t.TempDir()
	client := &fakeClient{output: multiplexedOutput("built\n", "warning\n")}
	providerValue, err := NewExecutor(map[string]any{
		"image":  "golang:1.26",
		"mounts": []any{map[string]any{"type": "volume", "source": "go-cache", "target": "/go/pkg/mod"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := providerValue.(*ExecutorProvider)
	provider.newClient = func() (dockerClient, error) { return client, nil }
	session, err := provider.Open(t.Context(), executor.Request{WorkflowName: "mixed", RunDir: runDir, Env: map[string]string{"BASE": "workflow"}, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.Run(t.Context(), process.Options{
		Command: "go", Args: []string{"build", "./..."}, Dir: filepath.Join(runDir, "backend"),
		Env: map[string]string{"BASE": "workflow", "STEP": "build"}, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "built\n" || result.Stderr != "warning\n" || result.ExitCode != 0 {
		t.Fatalf("result = %#v", result)
	}
	if client.created.Config.WorkingDir != defaultExecutorWorkspace || client.created.Config.Entrypoint[0] != "/bin/sh" {
		t.Fatalf("container config = %#v", client.created.Config)
	}
	if len(client.created.HostConfig.Mounts) != 2 || client.created.HostConfig.Mounts[0].Type != mount.TypeBind || client.created.HostConfig.Mounts[0].Source != runDir || client.created.HostConfig.Mounts[0].Target != defaultExecutorWorkspace {
		t.Fatalf("mounts = %#v", client.created.HostConfig.Mounts)
	}
	if client.execOptions.WorkingDir != "/workspace/backend" || strings.Join(client.execOptions.Cmd, " ") != "go build ./..." {
		t.Fatalf("exec options = %#v", client.execOptions)
	}
	if err := session.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !client.removed || !client.removedOptions.Force || !client.removedOptions.RemoveVolumes || !client.closed {
		t.Fatalf("cleanup removed=%v options=%#v closed=%v", client.removed, client.removedOptions, client.closed)
	}
}

func TestDockerExecutorRejectsTTYBeforeStartingSession(t *testing.T) {
	client := &fakeClient{}
	session := &dockerExecutorSession{client: client}
	_, err := session.Run(t.Context(), process.Options{Command: "sh", TTY: true})
	if err == nil || !strings.Contains(err.Error(), "tty is not supported") {
		t.Fatalf("Run() error = %v", err)
	}
	if session.containerID != "" || client.created.Config != nil {
		t.Fatalf("TTY rejection started session: id=%q config=%#v", session.containerID, client.created.Config)
	}
}

func TestDockerExecutorHonorsOutputPolicies(t *testing.T) {
	tests := []struct {
		name          string
		policy        process.OutputPolicy
		wantCapture   string
		wantStream    string
		wantTruncated bool
	}{
		{name: "tee", policy: process.OutputTee, wantCapture: "bui", wantStream: "built", wantTruncated: true},
		{name: "inherit", policy: process.OutputInherit, wantStream: "built"},
		{name: "capture", policy: process.OutputCapture, wantCapture: "bui", wantTruncated: true},
		{name: "discard", policy: process.OutputDiscard},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeClient{output: multiplexedOutput("built", "built")}
			session := &dockerExecutorSession{client: client, containerID: "container-id"}
			var stdout, stderr bytes.Buffer
			result, err := session.Run(t.Context(), process.Options{
				Command: "generate", Stdout: &stdout, Stderr: &stderr, CaptureLimit: 3,
				StdoutPolicy: test.policy, StderrPolicy: test.policy,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Stdout != test.wantCapture || result.Stderr != test.wantCapture ||
				stdout.String() != test.wantStream || stderr.String() != test.wantStream ||
				result.StdoutTruncated != test.wantTruncated || result.StderrTruncated != test.wantTruncated {
				t.Fatalf("result = %#v, stdout = %q, stderr = %q", result, stdout.String(), stderr.String())
			}
		})
	}
}

func TestDockerExecutorConfigurationValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "image", raw: map[string]any{}, want: "image is required"},
		{name: "pull", raw: map[string]any{"image": "alpine", "pull": "sometimes"}, want: "pull must be"},
		{name: "workspace target", raw: map[string]any{"image": "alpine", "workspace": map[string]any{"target": "relative"}}, want: "workspace target must be absolute"},
		{name: "duplicate target", raw: map[string]any{"image": "alpine", "mounts": []any{map[string]any{"source": ".", "target": "/workspace"}}}, want: "configured more than once"},
		{name: "init", raw: map[string]any{"image": "alpine", "init": map[string]any{"command": ""}}, want: "init command is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewExecutor(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestDockerExecutorAllowsTemplatedMountTypeBeforeRendering(t *testing.T) {
	_, err := NewExecutor(map[string]any{
		"image": "alpine",
		"mounts": []any{map[string]any{
			"type": "{{ .vars.mount_type }}", "source": ".", "target": "/source",
		}},
	})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
}

func TestDockerExecutorRetainsContainerAfterFailedRemoval(t *testing.T) {
	removeErr := errors.New("Docker unavailable")
	client := &fakeClient{removeErr: removeErr}
	session := &dockerExecutorSession{
		client: client, containerID: "container-id", mappings: []pathMapping{{source: "/host", target: "/workspace"}},
	}

	if err := session.removeLocked(t.Context()); !errors.Is(err, removeErr) {
		t.Fatalf("removeLocked() error = %v, want %v", err, removeErr)
	}
	if session.containerID != "container-id" || len(session.mappings) != 1 {
		t.Fatalf("failed removal discarded session state: id=%q mappings=%#v", session.containerID, session.mappings)
	}

	client.removeErr = nil
	if err := session.removeLocked(t.Context()); err != nil {
		t.Fatalf("retrying removeLocked(): %v", err)
	}
	if session.containerID != "" || session.mappings != nil || len(client.removedIDs) != 2 {
		t.Fatalf("successful retry state: id=%q mappings=%#v removals=%#v", session.containerID, session.mappings, client.removedIDs)
	}
}

func TestDockerExecutorRejectsUnmappedHostWorkingDirectory(t *testing.T) {
	runDir := t.TempDir()
	session := &dockerExecutorSession{
		config:  ExecutorConfig{Workspace: &WorkspaceConfig{Enabled: false}},
		request: executor.Request{RunDir: runDir},
	}

	if got, err := session.translatePath(runDir); err != nil || got != "/" {
		t.Fatalf("run directory = %q, %v", got, err)
	}
	if _, err := session.translatePath(filepath.Join(runDir, "backend")); err == nil || !strings.Contains(err.Error(), "not covered by a bind mount") {
		t.Fatalf("nested host path error = %v", err)
	}
	if got, err := session.translatePath("/opt/project"); err != nil || got != "/opt/project" {
		t.Fatalf("container path = %q, %v", got, err)
	}
}
