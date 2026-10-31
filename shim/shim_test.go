package shim

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/testfs"
)

func TestRenderBuildsRunCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		spec Spec
		exec string
	}{
		{"file", Spec{File: "/work/check.yaml"}, `exec wuko 'run' '--file' '/work/check.yaml' "$@"`},
		{"file with target and flags", Spec{File: "/work/deploy.yaml", Target: "staging", Args: []string{"--var", "x=it's"}},
			`exec wuko 'run' '--file' '/work/deploy.yaml' 'staging' '--var' 'x=it'\''s' "$@"`},
		{"by name", Spec{Workflow: "check"}, `exec wuko 'run' 'check' "$@"`},
		{"locator", Spec{Locator: "github:acme/flows@v1:ci.yaml"}, `exec wuko 'run' 'github:acme/flows@v1:ci.yaml' "$@"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data, err := Render("tool", test.spec)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			if lines[0] != "#!/bin/sh" {
				t.Fatalf("first line = %q", lines[0])
			}
			if got := lines[len(lines)-1]; got != test.exec {
				t.Fatalf("exec line = %q, want %q", got, test.exec)
			}
			parsed, err := Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(test.spec, parsed); diff != "" {
				t.Fatalf("parsed spec (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRenderRejectsAmbiguousSpec(t *testing.T) {
	t.Parallel()
	for _, spec := range []Spec{{}, {File: "/a.yaml", Workflow: "a"}, {File: "relative.yaml"}} {
		if _, err := Render("tool", spec); err == nil {
			t.Fatalf("Render(%+v) succeeded", spec)
		}
	}
}

func TestValidateNameRejectsUnsafeNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "-x", ".hidden", "a/b", "a b", "wuko"} {
		if err := ValidateName(name); err == nil {
			t.Fatalf("ValidateName(%q) succeeded", name)
		}
	}
	for _, name := range []string{"check", "deploy-staging", "v1.2", "_x"} {
		if err := ValidateName(name); err != nil {
			t.Fatalf("ValidateName(%q) = %v", name, err)
		}
	}
}

func TestInstallWritesExecutableAndRequiresForceToReplace(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "bin")
	installed, err := Install(dir, "check", Spec{File: "/work/check.yaml"}, false)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(installed.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("shim mode = %v, want executable", info.Mode())
	}
	if _, err := Install(dir, "check", Spec{File: "/work/other.yaml"}, false); !errors.Is(err, ErrExists) {
		t.Fatalf("second install error = %v, want ErrExists", err)
	}
	if _, err := Install(dir, "check", Spec{File: "/work/other.yaml"}, true); err != nil {
		t.Fatal(err)
	}
	shims, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(shims) != 1 || shims[0].Spec.File != "/work/other.yaml" {
		t.Fatalf("shims = %+v", shims)
	}
}

func TestInstallAndRemoveLeaveForeignFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	foreign := filepath.Join(dir, "tool")
	testfs.WriteFile(t, foreign, []byte("#!/bin/sh\necho mine\n"), 0o755)
	if _, err := Install(dir, "tool", Spec{Workflow: "tool"}, true); !errors.Is(err, ErrNotShim) {
		t.Fatalf("install error = %v, want ErrNotShim", err)
	}
	if _, err := Remove(dir, "tool"); !errors.Is(err, ErrNotShim) {
		t.Fatalf("remove error = %v, want ErrNotShim", err)
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "#!/bin/sh\necho mine\n" {
		t.Fatalf("foreign file changed: %q, %v", data, err)
	}
}

func TestListSkipsOtherFilesAndRemoveDeletesShim(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testfs.Write(t, filepath.Join(dir, "notes.txt"), "hello\n")
	for _, name := range []string{"zeta", "alpha"} {
		if _, err := Install(dir, name, Spec{Workflow: name}, false); err != nil {
			t.Fatal(err)
		}
	}
	shims, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, installed := range shims {
		names = append(names, installed.Name)
	}
	if diff := cmp.Diff([]string{"alpha", "zeta"}, names); diff != "" {
		t.Fatalf("names (-want +got):\n%s", diff)
	}
	if _, err := Remove(dir, "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "alpha")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("alpha still present: %v", err)
	}
	if _, err := Remove(dir, "alpha"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("second remove error = %v", err)
	}
}

func TestListMissingDirectoryIsEmpty(t *testing.T) {
	t.Parallel()
	shims, err := List(filepath.Join(t.TempDir(), "missing"))
	if err != nil || len(shims) != 0 {
		t.Fatalf("List = %v, %v", shims, err)
	}
}
