package workflow

import (
	"bytes"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
)

func TestBuildWorkflowPackageEnforcesRemotePackageLimits(t *testing.T) {
	t.Parallel()
	t.Run("entry count", func(t *testing.T) {
		directory := t.TempDir()
		writePackageTestManifest(t, directory, "version: 1\nname: crowded\nsteps: []\n")
		for index := 0; index < maxEntries; index++ {
			name := filepath.Join(directory, strings.Repeat("0", 6-len(strconv.Itoa(index)))+strconv.Itoa(index)+".txt")
			testfs.WriteFile(t, name, nil, 0o644)
		}
		if _, _, err := BuildWorkflowPackage(directory, filepath.Join(t.TempDir(), "crowded.tar.gz")); err == nil || !strings.Contains(err.Error(), "entry limit") {
			t.Fatalf("error = %v, want entry-limit failure", err)
		}
	})

	t.Run("manifest size", func(t *testing.T) {
		directory := t.TempDir()
		writePackageTestManifest(t, directory, string(bytes.Repeat([]byte("x"), maxManifestSize+1)))
		if _, _, err := BuildWorkflowPackage(directory, filepath.Join(t.TempDir(), "large-manifest.tar.gz")); err == nil || !strings.Contains(err.Error(), "manifest exceeds") {
			t.Fatalf("error = %v, want manifest-size failure", err)
		}
	})

	t.Run("archive size", func(t *testing.T) {
		directory := t.TempDir()
		writePackageTestManifest(t, directory, "version: 1\nname: large\nsteps: []\n")
		data := make([]byte, maxArchiveSize+1)
		random := rand.NewChaCha8([32]byte{1, 2})
		if _, err := random.Read(data); err != nil {
			t.Fatal(err)
		}
		testfs.WriteFile(t, filepath.Join(directory, "data.bin"), data, 0o644)
		if _, _, err := BuildWorkflowPackage(directory, filepath.Join(t.TempDir(), "large.tar.gz")); err == nil || !strings.Contains(err.Error(), "download limit") {
			t.Fatalf("error = %v, want archive-size failure", err)
		}
	})
}

func writePackageTestManifest(t *testing.T, directory, data string) {
	t.Helper()
	testfs.WriteFile(t, filepath.Join(directory, "wuko.yaml"), []byte(data), 0o644)
}

func TestBuildWorkflowPackageExcludesLocalValueStore(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	writePackageTestManifest(t, directory, "version: 1\nname: stateful\nsteps: []\n")
	values := filepath.Join(directory, ".wuko", "values")
	testfs.MkdirAll(t, values, 0o755)
	// once completions and key_value stores are the machine's run state. Packaging
	// them shipped one author's state, and an installed once completion made another
	// machine skip a bootstrap it had never performed.
	for _, name := range []string{"once.json", "build.json", "changed.json"} {
		testfs.WriteFile(t, filepath.Join(values, name), []byte(`{"seeded": true}`), 0o644)
	}
	testfs.WriteFile(t, filepath.Join(directory, "README.md"), []byte("kept\n"), 0o644)

	files, err := collectPackageFiles(directory)
	if err != nil {
		t.Fatalf("collectPackageFiles() error = %v", err)
	}
	var names []string
	for _, file := range files {
		names = append(names, file.name)
	}
	want := []string{"README.md", "wuko.yaml"}
	if len(names) != len(want) {
		t.Fatalf("packaged files = %v, want %v", names, want)
	}
	for index, name := range want {
		if names[index] != name {
			t.Fatalf("packaged files = %v, want %v", names, want)
		}
	}

	// The digest is taken over the same file set, so run state cannot make a
	// package look stale and force a pointless rebuild either.
	withState, err := WorkflowPackageDigest(directory)
	if err != nil {
		t.Fatalf("WorkflowPackageDigest() error = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(directory, ".wuko")); err != nil {
		t.Fatal(err)
	}
	withoutState, err := WorkflowPackageDigest(directory)
	if err != nil {
		t.Fatalf("WorkflowPackageDigest() error = %v", err)
	}
	if withState != withoutState {
		t.Fatalf("digest with run state = %s, without = %s; want equal", withState, withoutState)
	}
}
