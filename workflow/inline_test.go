package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/shim"
)

func TestInlineShimLoadsResourcesBesideScript(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	testfs.Write(t, filepath.Join(bin, "message.tmpl"), "hello from {{ .workflow.dir }}")
	testfs.Write(t, filepath.Join(bin, "steps.yaml"), "steps:\n  - id: hello\n    type: print\n    with: {message: hello}\n")
	content := "version: 1\nname: embedded\ntemplates:\n  greeting:\n    file: message.tmpl\nsteps:\n  - require: steps.yaml\n"
	installed, err := shim.Install(bin, "hello", shim.Spec{
		File: filepath.Join(root, "missing", "original.yaml"), Inline: true, Content: content,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewLoader(nil).Load(t.Context(), installed.Path, LoadOptions{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if definition.Path != installed.Path || definition.Dir != bin {
		t.Fatalf("path = %q, dir = %q", definition.Path, definition.Dir)
	}
	if definition.Templates["greeting"].Body != "hello from {{ .workflow.dir }}" {
		t.Fatalf("templates = %#v", definition.Templates)
	}
	if len(definition.Steps) != 1 || definition.Steps[0].ID != "hello" {
		t.Fatalf("steps = %#v", definition.Steps)
	}
}

func TestInlineShimLoaderReportsInvalidBlock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "tool")
	testfs.Write(t, path, "#!/bin/sh\n# wuko-shim: {\"file\":\"/source.yaml\",\"inline\":true}\nexec wuko\n")
	if _, err := NewLoader(nil).DecodeContext(t.Context(), path, LoadOptions{}); err == nil || !strings.Contains(err.Error(), "inline workflow") {
		t.Fatalf("DecodeContext = %v, want invalid inline block", err)
	}
}

func TestWorkflowWithOrdinaryShebangStillLoads(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	testfs.Write(t, path, "#!/usr/bin/env wuko\nversion: 1\nname: ordinary\nsteps:\n  - id: hello\n    type: print\n    with: {message: hello}\n")
	definition, err := NewLoader(nil).DecodeContext(t.Context(), path, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if definition.Name != "ordinary" {
		t.Fatalf("name = %q", definition.Name)
	}
}

func TestInlineShimResolvesResourcesThroughSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	testfs.Write(t, filepath.Join(bin, "steps.yaml"), "steps:\n  - id: hello\n    type: print\n    with: {message: hello}\n")
	installed, err := shim.Install(bin, "hello", shim.Spec{
		File: filepath.Join(root, "original.yaml"), Inline: true, Content: "version: 1\nname: embedded\nsteps:\n  - require: steps.yaml\n",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "path", "hello")
	testfs.MkdirAll(t, filepath.Dir(link), 0o755)
	if err := os.Symlink(installed.Path, link); err != nil {
		t.Fatal(err)
	}
	definition, err := NewLoader(nil).Load(t.Context(), link, LoadOptions{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Steps) != 1 || definition.Steps[0].ID != "hello" {
		t.Fatalf("steps = %#v", definition.Steps)
	}
}
