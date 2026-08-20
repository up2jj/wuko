package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestAgentListDiscoversAvailableAgents(t *testing.T) {
	home := t.TempDir()
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
		cwd:       func() (string, error) { return t.TempDir(), nil },
		homeDir:   func() (string, error) { return home, nil },
		configDir: func() (string, error) { return filepath.Join(home, "config"), nil },
		agentLookPath: func(name string) (string, error) {
			if name == "codex" {
				return "/opt/codex", nil
			}
			return "", errors.New("not found")
		},
		registry: step.NewRegistry(),
	})
	command.SetArgs([]string{"agent", "list"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := "codex\t/opt/codex\t" + filepath.Join(home, ".agents", "skills") + "\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestAgentInstallCopiesEmbeddedSkills(t *testing.T) {
	home := t.TempDir()
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
		cwd:       func() (string, error) { return t.TempDir(), nil },
		homeDir:   func() (string, error) { return home, nil },
		configDir: func() (string, error) { return filepath.Join(home, "config"), nil },
		agentLookPath: func(name string) (string, error) {
			if name == "claude" {
				return "/opt/claude", nil
			}
			return "", errors.New("not found")
		},
		registry: step.NewRegistry(),
	})
	command.SetArgs([]string{"agent", "install", "claude"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, skill := range []string{"wuko-workflow-author", "wuko-workflow-debugger", "wuko-agent-handoff"} {
		path := filepath.Join(home, ".claude", "skills", skill, "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("skill %s was not installed: %v", skill, err)
		}
	}
	if !strings.Contains(output.String(), "installed 3 skills for claude") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestAgentInstallRejectsUnavailableAgent(t *testing.T) {
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
		cwd:           func() (string, error) { return t.TempDir(), nil },
		homeDir:       func() (string, error) { return t.TempDir(), nil },
		configDir:     func() (string, error) { return t.TempDir(), nil },
		agentLookPath: func(string) (string, error) { return "", errors.New("not found") },
		registry:      step.NewRegistry(),
	})
	command.SetArgs([]string{"agent", "install", "claude"})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "no supported coding agents") {
		t.Fatalf("error = %v, want no-agent error", err)
	}
}
