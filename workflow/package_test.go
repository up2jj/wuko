package workflow

import (
	"bytes"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBuildWorkflowPackageEnforcesRemotePackageLimits(t *testing.T) {
	t.Run("entry count", func(t *testing.T) {
		directory := t.TempDir()
		writePackageTestManifest(t, directory, "version: 1\nname: crowded\nsteps: []\n")
		for index := 0; index < maxEntries; index++ {
			name := filepath.Join(directory, strings.Repeat("0", 6-len(strconv.Itoa(index)))+strconv.Itoa(index)+".txt")
			if err := os.WriteFile(name, nil, 0o644); err != nil {
				t.Fatal(err)
			}
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
		if err := os.WriteFile(filepath.Join(directory, "data.bin"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := BuildWorkflowPackage(directory, filepath.Join(t.TempDir(), "large.tar.gz")); err == nil || !strings.Contains(err.Error(), "download limit") {
			t.Fatalf("error = %v, want archive-size failure", err)
		}
	})
}

func writePackageTestManifest(t *testing.T, directory, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, "wuko.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
