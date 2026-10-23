package agent

import (
	"io"
	"os"
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
)

func TestAgentSendsPromptOnStdin(t *testing.T) {
	runner := steptest.Build(t, New, map[string]any{"command": "sh", "args": []any{"-c", "cat"}, "prompt": "do work"})
	result, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir(), Env: map[string]string{"PATH": os.Getenv("PATH")}, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["stdout"] != "do work\n" {
		t.Fatalf("stdout = %q", result.Outputs["stdout"])
	}
}
