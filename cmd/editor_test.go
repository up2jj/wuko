package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestEditorCommandParsesQuotedArguments(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		spec string
		want []string
	}{
		{`code --wait`, []string{"code", "--wait"}},
		{`"/Applications/Example Editor/editor" --wait`, []string{"/Applications/Example Editor/editor", "--wait"}},
		{`editor --title 'My project' "" pre"joined"`, []string{"editor", "--title", "My project", "", "prejoined"}},
		{`my\ editor escaped\ space "say \"hello\""`, []string{"my editor", "escaped space", `say "hello"`}},
		{`"C:\Program Files\Editor\editor.exe" --wait`, []string{`C:\Program Files\Editor\editor.exe`, "--wait"}},
		{`C:\Editor\editor.exe --wait`, []string{`C:\Editor\editor.exe`, "--wait"}},
		{`editor '$HOME; $(echo literal)'`, []string{"editor", "$HOME; $(echo literal)"}},
	} {
		t.Run(test.spec, func(t *testing.T) {
			got, err := splitEditorCommand(test.spec)
			if err != nil || !slices.Equal(got, test.want) {
				t.Fatalf("splitEditorCommand() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestEditorCommandRejectsMalformedQuoting(t *testing.T) {
	t.Parallel()
	for _, spec := range []string{"", "  ", `"" --wait`, `editor 'unfinished`, `editor "unfinished`, "editor trailing\\"} {
		t.Run(spec, func(t *testing.T) {
			if _, err := splitEditorCommand(spec); err == nil {
				t.Fatal("expected editor command parsing error")
			}
		})
	}
}

func TestOpenWorkflowEditorPreservesQuotedPathAndArguments(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("fake editor requires a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "fake editor")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, variable := range []string{"VISUAL", "EDITOR"} {
		t.Run(variable, func(t *testing.T) {
			var output bytes.Buffer
			editor := openWorkflowEditor(func(name string) string {
				if name == variable {
					return `"` + path + `" --title 'My project' ""`
				}
				return ""
			})
			if err := editor(t.Context(), strings.NewReader(""), &output, io.Discard, "/tmp/workflow name.yaml"); err != nil {
				t.Fatal(err)
			}
			if got, want := output.String(), "--title\x00My project\x00\x00/tmp/workflow name.yaml\x00"; got != want {
				t.Fatalf("editor arguments = %q, want %q", got, want)
			}
		})
	}
}
