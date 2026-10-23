package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestJustRunPreservesQuotedArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Go executable requires a POSIX shell")
	}
	just, err := exec.LookPath("just")
	if err != nil {
		t.Skip("just is not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\nprintf '%s\\000' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	args := []string{"run", "check", "--var", "message=hello world", "--var", "pattern=*.go", "--var", "literal=$(printf expanded)", "--env", `QUOTE=a'b"c`}
	command := exec.CommandContext(t.Context(), just, append([]string{"run"}, args...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("just run: %v", err)
	}
	want := strings.Join(append([]string{"run", "."}, args...), "\x00") + "\x00"
	if !bytes.Equal(output, []byte(want)) {
		t.Fatalf("forwarded arguments = %q, want %q", output, want)
	}
}
