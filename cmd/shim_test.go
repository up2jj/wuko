package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/shim"
	"github.com/up2jj/wuko/step"
)

const shimTestWorkflow = "version: 1\nname: check\nsteps:\n  - id: hello\n    type: print\n    with: {message: hi}\n"

func runShimCommand(t *testing.T, project, home, path string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &stdout, stderr: &stderr,
		cwd:       func() (string, error) { return project, nil },
		homeDir:   func() (string, error) { return home, nil },
		configDir: func() (string, error) { return filepath.Join(home, "config"), nil },
		getenv: func(name string) string {
			if name == "PATH" {
				return path
			}
			return ""
		},
		registry: step.NewRegistry(),
	})
	command.SetArgs(args)
	err := command.ExecuteContext(t.Context())
	return stdout.String(), stderr.String(), err
}

func TestShimInstallPinsDiscoveredWorkflow(t *testing.T) {
	t.Parallel()
	project, home := t.TempDir(), t.TempDir()
	workflowPath := filepath.Join(project, ".wuko", "workflows", "check.yaml")
	testfs.Write(t, workflowPath, shimTestWorkflow)
	bin := filepath.Join(home, ".wuko", "bin")

	stdout, stderr, err := runShimCommand(t, project, home, "/usr/bin", "shim", "install", "check", "--", "--no-config", "--var", "x=1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "installed shim "+filepath.Join(bin, "check")) {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "is not on PATH") {
		t.Fatalf("stderr = %q, want PATH hint", stderr)
	}
	data, err := os.ReadFile(filepath.Join(bin, "check"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := shim.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := filepath.EvalSymlinks(spec.File); got != resolved || strings.Join(spec.Args, " ") != "--no-config --var x=1" {
		t.Fatalf("spec = %+v", spec)
	}

	_, stderr, err = runShimCommand(t, project, home, "/usr/bin"+string(os.PathListSeparator)+bin, "shim", "install", "check", "--force")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want no PATH hint", stderr)
	}
}

func TestShimInstallNamesTargetsAndByName(t *testing.T) {
	t.Parallel()
	project, home := t.TempDir(), t.TempDir()
	testfs.Write(t, filepath.Join(project, ".wuko", "workflows", "check.yaml"), shimTestWorkflow)
	if _, _, err := runShimCommand(t, project, home, "", "shim", "install", "check", "ci", "--by-name"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runShimCommand(t, project, home, "", "shim", "list")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "check-ci\tcheck ci\n" {
		t.Fatalf("list = %q", stdout)
	}
	if _, _, err := runShimCommand(t, project, home, "", "shim", "uninstall", "check-ci"); err != nil {
		t.Fatal(err)
	}
	if stdout, _, _ := runShimCommand(t, project, home, "", "shim", "list"); stdout != "" {
		t.Fatalf("list after uninstall = %q", stdout)
	}
}

func TestShimInstallRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	project, home := t.TempDir(), t.TempDir()
	testfs.Write(t, filepath.Join(project, ".wuko", "workflows", "check.yaml"), shimTestWorkflow)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown workflow", []string{"shim", "install", "chek"}, `did you mean "check"`},
		{"unknown run flag", []string{"shim", "install", "check", "--", "--bogus"}, "invalid wuko run flags"},
		{"positional after dash", []string{"shim", "install", "check", "--", "ci"}, "only wuko run flags"},
		{"remote without name", []string{"shim", "install", "github:acme/flows@v1:ci.yaml"}, "--as is required"},
		{"reserved name", []string{"shim", "install", "check", "--as", "wuko"}, `cannot be named "wuko"`},
		{"missing file", []string{"shim", "install", "--file", "missing.yaml"}, "checking workflow file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := runShimCommand(t, project, home, "", test.args...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestShimInstallFileUsesStemAndTarget(t *testing.T) {
	t.Parallel()
	project, home := t.TempDir(), t.TempDir()
	testfs.Write(t, filepath.Join(project, "release.yaml"), shimTestWorkflow)
	if _, _, err := runShimCommand(t, project, home, "", "shim", "install", "--file", "release.yaml", "prod"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runShimCommand(t, project, home, "", "shim", "list")
	if err != nil {
		t.Fatal(err)
	}
	if want := "release-prod\t" + filepath.Join(project, "release.yaml") + " prod\n"; stdout != want {
		t.Fatalf("list = %q, want %q", stdout, want)
	}
}
