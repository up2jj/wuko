package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/shim"
)

func TestShimCreateResolvesSourcesAndNames(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		path string
		spec shim.Spec
	}{
		{"discovered", []string{"check"}, "check", shim.Spec{File: ".wuko/workflows/check.yaml"}},
		{"target", []string{"check", "ci"}, "check-ci", shim.Spec{File: ".wuko/workflows/check.yaml", Target: "ci"}},
		{"file", []string{"--file", "release.yaml", "prod"}, "release-prod", shim.Spec{File: "release.yaml", Target: "prod"}},
		{"override", []string{"check", "--as", "wcheck"}, "wcheck", shim.Spec{File: ".wuko/workflows/check.yaml"}},
		{"output", []string{"check", "--output", "bin/wcheck", "--as", "wcheck"}, "bin/wcheck", shim.Spec{File: ".wuko/workflows/check.yaml"}},
		{"by name", []string{"check", "--by-name"}, "check", shim.Spec{Workflow: "check"}},
		{"project", []string{"--project", "automation", "--output", "dev"}, "dev", shim.Spec{Project: "automation"}},
		{"project inferred", []string{"--project", "."}, "my-app", shim.Spec{Project: "."}},
		{"project override", []string{"--project", ".", "--as", "tools"}, "tools", shim.Spec{Project: "."}},
		{"remote named", []string{"github:acme/flows@v1:ci.yaml", "--as", "ci"}, "ci", shim.Spec{Locator: "github:acme/flows@v1:ci.yaml"}},
		{"remote output", []string{"https://example.test/ci.yaml", "-o", "bin/ci"}, "bin/ci", shim.Spec{Locator: "https://example.test/ci.yaml"}},
		{"baked flags", []string{"check", "ci", "--", "--no-config", "--var", "x=it's $literal"}, "check-ci",
			shim.Spec{File: ".wuko/workflows/check.yaml", Target: "ci", Args: []string{"--no-config", "--var", "x=it's $literal"}}},
		{"custom message", []string{"check", "--missing-message", "Install with: mise install"}, "check",
			shim.Spec{File: ".wuko/workflows/check.yaml", MissingMessage: "Install with: mise install"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			project, home := filepath.Join(root, "my-app"), t.TempDir()
			testfs.Write(t, filepath.Join(project, ".wuko", "workflows", "check.yaml"), shimTestWorkflow)
			testfs.Write(t, filepath.Join(project, "release.yaml"), shimTestWorkflow)
			testfs.MkdirAll(t, filepath.Join(project, "automation", ".wuko", "workflows"), 0o755)
			stdout, stderr, err := runShimCommand(t, project, home, "", append([]string{"shim", "create"}, test.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(project, test.path)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := shim.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			want := test.spec
			if want.File != "" {
				want.File = filepath.Join(project, filepath.FromSlash(want.File))
			}
			if want.Project != "" {
				want.Project = filepath.Join(project, want.Project)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("spec (-want +got):\n%s", diff)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("file = %v, error = %v, want mode 0755", info, err)
			}
			if !strings.Contains(stdout, "created shim "+path+" -> ") || stderr != "" {
				t.Fatalf("stdout = %q, stderr = %q", stdout, stderr)
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 0 {
				t.Fatalf("creation changed home: %v, %v", entries, err)
			}
		})
	}
}

func TestShimCreateStdoutOnlyContainsScript(t *testing.T) {
	t.Parallel()
	for _, selector := range [][]string{
		{"check"}, {"--project", "."}, {"github:acme/flows@v1:ci.yaml", "--as", "ci"},
	} {
		t.Run(strings.Join(selector, " "), func(t *testing.T) {
			t.Parallel()
			project, home := t.TempDir(), t.TempDir()
			testfs.Write(t, filepath.Join(project, ".wuko", "workflows", "check.yaml"), shimTestWorkflow)
			args := append([]string{"shim", "create"}, selector...)
			stdout, stderr, err := runShimCommand(t, project, home, "", append(args, "--output", "-")...)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(stdout, "#!/bin/sh\n") || !strings.HasSuffix(stdout, "\"$@\"\n") || stderr != "" {
				t.Fatalf("stdout = %q, stderr = %q", stdout, stderr)
			}
			if _, err := shim.Parse([]byte(stdout)); err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{project, home} {
				entries, err := os.ReadDir(dir)
				want := 0
				if dir == project {
					want = 1 // The preexisting .wuko directory.
				}
				if err != nil || len(entries) != want {
					t.Fatalf("stdout generation changed %s: %v, %v", dir, entries, err)
				}
			}
		})
	}
}

func TestShimCreateRejectsInvalidInputBeforeWriting(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"missing selector", nil, "workflow name"},
		{"unknown workflow", []string{"chek"}, "did you mean"},
		{"missing file", []string{"--file", "missing.yaml"}, "checking workflow file"},
		{"unknown baked flag", []string{"check", "--", "--bogus"}, "invalid wuko run flags"},
		{"baked positional", []string{"check", "--", "ci"}, "only wuko run flags"},
		{"remote without name", []string{"https://example.test/ci.yaml"}, "--as is required"},
		{"remote stdout without name", []string{"https://example.test/ci.yaml", "-o", "-"}, "--as is required"},
		{"conflicting name", []string{"check", "--as", "tool", "-o", "other"}, "conflicts with output filename"},
		{"empty output", []string{"check", "--output", ""}, "--output requires"},
		{"reserved output", []string{"check", "-o", "wuko"}, "cannot be named"},
		{"invalid name", []string{"check", "--as", "bad name"}, "choose another name with --as"},
		{"reserved inferred file name", []string{"--file", "fixtures/wuko.yaml"}, "choose another name with --as"},
		{"empty project", []string{"--project", ""}, "--project requires"},
		{"missing project", []string{"--project", "missing"}, "must contain"},
		{"project workflow", []string{"--project", ".", "check"}, "--project requires"},
		{"project file", []string{"--project", ".", "--file", "fixtures/wuko.yaml"}, "none of the others"},
		{"project by name", []string{"--project", ".", "--by-name"}, "none of the others"},
		{"project flags", []string{"--project", ".", "--", "--no-config"}, "--project requires"},
		{"project bare separator", []string{"--project", ".", "--"}, "--project requires"},
		{"file by name", []string{"--file", "fixtures/wuko.yaml", "--by-name"}, "--by-name cannot"},
		{"remote by name", []string{"https://example.test/ci.yaml", "--by-name"}, "--by-name cannot"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			project, home := t.TempDir(), t.TempDir()
			testfs.Write(t, filepath.Join(project, ".wuko", "workflows", "check.yaml"), shimTestWorkflow)
			testfs.Write(t, filepath.Join(project, "fixtures", "wuko.yaml"), shimTestWorkflow)
			stdout, _, err := runShimCommand(t, project, home, "", append([]string{"shim", "create"}, test.args...)...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if stdout != "" {
				t.Fatalf("failed creation wrote stdout: %q", stdout)
			}
			for _, dir := range []string{project, home} {
				entries, err := os.ReadDir(dir)
				want := 0
				if dir == project {
					want = 2 // The preexisting .wuko and fixtures directories.
				}
				if err != nil || len(entries) != want {
					t.Fatalf("failed creation changed %s: %v, %v", dir, entries, err)
				}
			}
		})
	}
}

func TestShimCreateUsesExistingLifecycle(t *testing.T) {
	t.Parallel()
	project, home := t.TempDir(), t.TempDir()
	testfs.Write(t, filepath.Join(project, "release.yaml"), shimTestWorkflow)
	output := filepath.Join(project, "bin", "ship")
	args := []string{"shim", "create", "--file", "release.yaml", "--output", output}
	if _, _, err := runShimCommand(t, project, home, "", args...); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runShimCommand(t, project, home, "", args...); !errors.Is(err, shim.ErrExists) {
		t.Fatalf("replacement = %v, want ErrExists", err)
	}
	if _, _, err := runShimCommand(t, project, home, "", append(args, "--force")...); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runShimCommand(t, project, home, "", "shim", "list", "--dir", filepath.Dir(output))
	if err != nil || !strings.HasPrefix(stdout, "ship\t") || strings.Contains(stdout, "missing") {
		t.Fatalf("list = %q, %v", stdout, err)
	}
	if _, _, err := runShimCommand(t, project, home, "", "shim", "uninstall", "ship", "--dir", filepath.Dir(output)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uninstall left output: %v", err)
	}
}

func TestShimCreateProtectsExistingFiles(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"foreign file", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			project, home := t.TempDir(), t.TempDir()
			testfs.Write(t, filepath.Join(project, "release.yaml"), shimTestWorkflow)
			path := filepath.Join(project, "ship")
			switch kind {
			case "foreign file":
				testfs.Write(t, path, "mine")
			case "directory":
				testfs.MkdirAll(t, path, 0o755)
			case "symlink":
				if _, err := shim.Install(project, "original", shim.Spec{Workflow: "check"}, false); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("original", path); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = runShimCommand(t, project, home, "", "shim", "create", "--file", "release.yaml", "-o", path, "--force")
			if kind == "directory" {
				if err == nil || !strings.Contains(err.Error(), "is a directory") {
					t.Fatalf("forced creation = %v, want directory error", err)
				}
			} else if !errors.Is(err, shim.ErrNotShim) {
				t.Fatalf("forced creation = %v, want ErrNotShim", err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("existing entry was replaced: %v", err)
			}
		})
	}
}

func TestShimInstallProjectInfersNameAndConfiguresPath(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	project := filepath.Join(root, "my-app")
	testfs.MkdirAll(t, filepath.Join(project, ".wuko", "workflows"), 0o755)
	if _, _, err := runShimCommand(t, project, home, "", "shim", "install", "--project", ".", "--shell", "zsh"); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".wuko", "bin")
	data, err := os.ReadFile(filepath.Join(bin, "my-app"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := shim.Parse(data)
	if err != nil || spec.Project != project {
		t.Fatalf("project spec = %+v, %v", spec, err)
	}
	startup, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil || !strings.Contains(string(startup), bin) {
		t.Fatalf("startup = %q, %v", startup, err)
	}
	if _, _, err := runShimCommand(t, project, home, "", "shim", "uninstall", "my-app"); err != nil {
		t.Fatal(err)
	}
	startup, err = os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil || len(startup) != 0 {
		t.Fatalf("PATH entry remained: %q, %v", startup, err)
	}
}
