package docker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultCommandRunnerStopsDescendantsOnCancel(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-docker")
	// The background sleep inherits stdout, as a buildx plugin process would.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30 &\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := defaultCommandRunner(ctx, script, nil, new(bytes.Buffer), new(bytes.Buffer))
	if err == nil {
		t.Fatal("canceled command succeeded")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("runner returned after %s, want prompt cancellation", elapsed)
	}
}
