// Package gittest creates and drives throwaway Git repositories for tests. A package using it
// calls testhome.Run from its TestMain to isolate the home directory and global Git defaults.
package gittest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Init creates an empty repository with a test identity in a new temporary directory and
// returns its root. The local identity also works for commands with an explicit environment
// that cannot read testhome's global Git configuration.
func Init(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	Run(t, root, "init", "--quiet")
	Run(t, root, "config", "--local", "user.name", "Wuko Test")
	Run(t, root, "config", "--local", "user.email", "wuko@example.test")
	return root
}

// InitWithCommit creates a repository whose current branch holds one commit adding README.md,
// and returns its root.
func InitWithCommit(t testing.TB) string {
	t.Helper()
	root := Init(t)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Run(t, root, "add", "README.md")
	Run(t, root, "commit", "--quiet", "-m", "initial")
	return root
}

// Run runs git in dir and fails the test, reporting git's output, when it exits unsuccessfully.
// It takes no context, so it also works from a cleanup after the test's context is done.
func Run(t testing.TB, dir string, args ...string) {
	t.Helper()
	run(t, dir, args)
}

// Output runs git in dir like Run and returns its standard output without surrounding
// whitespace.
func Output(t testing.TB, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(run(t, dir, args))
}

func run(t testing.TB, dir string, args []string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}
