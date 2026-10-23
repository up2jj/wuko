package testfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteTreeCreatesNestedFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	WriteTree(t, root, map[string]string{"top.txt": "top", "a/b/nested.txt": "nested"})
	for name, want := range map[string]string{"top.txt": "top", "a/b/nested.txt": "nested"} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatalf("%s = %q, want %q", name, data, want)
		}
	}
}

func TestWriteFileKeepsPermissions(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "private")
	MkdirAll(t, dir, 0o700)
	path := filepath.Join(dir, "secret")
	WriteFile(t, path, []byte("x"), 0o600)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}
