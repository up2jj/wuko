// Package testfs prepares files and directories for tests. Each function mirrors its os
// counterpart but fails the test instead of returning an error.
package testfs

import (
	"os"
	"path/filepath"
	"testing"
)

// WriteFile writes data to path with os.WriteFile.
func WriteFile(t testing.TB, path string, data []byte, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, perm); err != nil {
		t.Fatal(err)
	}
}

// MkdirAll creates path and any missing parents with os.MkdirAll.
func MkdirAll(t testing.TB, path string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, perm); err != nil {
		t.Fatal(err)
	}
}

// Write writes content to path with mode 0o644, creating missing parent directories with
// mode 0o755.
func Write(t testing.TB, path, content string) {
	t.Helper()
	MkdirAll(t, filepath.Dir(path), 0o755)
	WriteFile(t, path, []byte(content), 0o644)
}

// WriteTree writes files under root, keyed by slash-separated relative path, as Write does.
func WriteTree(t testing.TB, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		Write(t, filepath.Join(root, filepath.FromSlash(name)), content)
	}
}
