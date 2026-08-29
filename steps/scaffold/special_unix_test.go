//go:build darwin || linux

package scaffold

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestRunRejectsSpecialSourceFile(t *testing.T) {
	workflowDir := t.TempDir()
	source := filepath.Join(workflowDir, "source")
	mustMkdir(t, source, 0o755)
	if err := syscall.Mkfifo(filepath.Join(source, "pipe"), 0o600); err != nil {
		t.Skipf("creating FIFO: %v", err)
	}
	runner := newRunner(t, map[string]any{"from": "source", "into": "target"})
	_, err := runner.Run(t.Context(), step.Request{
		WorkflowDir: workflowDir, RunDir: t.TempDir(), TemplateRenderer: newRenderer(t, nil, map[string]any{}),
	})
	if err == nil || !strings.Contains(err.Error(), "regular file or directory") {
		t.Fatalf("Run() error = %v", err)
	}
}
