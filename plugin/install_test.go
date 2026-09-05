package plugin

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallIsTransactionalAndWritesMarker(t *testing.T) {
	source := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	archive := makeArchive(t, "wuko-plugin-acme", 0755, []byte("#!/bin/sh\nWUKO_PLUGIN_TEST_HELPER=1 exec \""+executable+"\"\n"))
	if err := os.WriteFile(filepath.Join(source, "plugin.tar.gz"), archive, 0600); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(Manifest{Version: 1, Namespace: "acme", PluginVersion: "1.0.0", Protocol: Protocol, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: digest(archive)}}})
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	marker, err := Install(context.Background(), source, root, false, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if marker.Namespace != "acme" {
		t.Fatal(marker)
	}
	directory := filepath.Join(root, "acme")
	if _, err := ValidateInstallation(directory, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), source, root, false, nil, io.Discard); err == nil {
		t.Fatal("expected existing installation error")
	}
	if err := os.WriteFile(filepath.Join(directory, MarkerName), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateInstallation(directory, "acme"); err == nil {
		t.Fatal("expected corrupted marker error")
	}
}
