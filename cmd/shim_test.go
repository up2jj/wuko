package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
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

func TestShimInstallProjectUsesExistingLifecycle(t *testing.T) {
	t.Parallel()
	caller, home := t.TempDir(), t.TempDir()
	project := filepath.Join(caller, "automation")
	testfs.MkdirAll(t, filepath.Join(project, ".wuko", "workflows"), 0o755)
	stdout, _, err := runShimCommand(t, caller, home, "/usr/bin:/bin", "shim", "install", "--project", "automation", "--as", "mytools", "--shell", "zsh", "--dir", "commands")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(caller, "commands")
	if !strings.Contains(stdout, "installed shim "+filepath.Join(bin, "mytools")) {
		t.Fatalf("stdout = %q", stdout)
	}
	data, err := os.ReadFile(filepath.Join(bin, "mytools"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := shim.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(shim.Spec{Project: project}, spec); diff != "" {
		t.Fatalf("spec (-want +got):\n%s", diff)
	}
	if _, _, err := runShimCommand(t, caller, home, "", "shim", "install", "--project", project, "--as", "mytools", "--shell", "zsh", "--dir", bin); err == nil {
		t.Fatal("replacement without --force succeeded")
	}
	if _, _, err := runShimCommand(t, caller, home, "", "shim", "install", "--project", project, "--as", "mytools", "--shell", "zsh", "--dir", bin, "--force"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = runShimCommand(t, caller, home, "", "shim", "list", "--dir", bin)
	if err != nil || stdout != "mytools\tproject "+project+"\n" {
		t.Fatalf("list = %q, error = %v", stdout, err)
	}
	if err := os.RemoveAll(project); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = runShimCommand(t, caller, home, "", "shim", "list", "--dir", bin)
	if err != nil || !strings.Contains(stdout, "project workflow directory missing") {
		t.Fatalf("missing project list = %q, error = %v", stdout, err)
	}
	if _, _, err := runShimCommand(t, caller, home, "", "shim", "uninstall", "mytools", "--dir", bin); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil || len(data) != 0 {
		t.Fatalf("PATH entry remained: %q, error = %v", data, err)
	}
}

func TestShimInstallProjectRejectsInvalidInputBeforeWriting(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
	}{
		{"missing name", []string{"--project", ".", "--no-path"}},
		{"empty project", []string{"--project", "", "--as", "tool", "--no-path"}},
		{"missing project", []string{"--project", "missing", "--as", "tool", "--no-path"}},
		{"workflow selector", []string{"--project", ".", "check", "--as", "tool", "--no-path"}},
		{"file selector", []string{"--project", ".", "--file", "check.yaml", "--as", "tool", "--no-path"}},
		{"by name", []string{"--project", ".", "--by-name", "--as", "tool", "--no-path"}},
		{"baked flags", []string{"--project", ".", "--as", "tool", "--no-path", "--", "--no-config"}},
		{"bare separator", []string{"--project", ".", "--as", "tool", "--no-path", "--"}},
		{"unsupported shell", []string{"--project", ".", "--as", "tool", "--shell", "unknown"}},
		{"shell without project", []string{"check", "--shell", "zsh"}},
		{"no path without project", []string{"check", "--no-path"}},
		{"reserved name", []string{"--project", ".", "--as", "wuko", "--no-path"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			project, home := t.TempDir(), t.TempDir()
			testfs.MkdirAll(t, filepath.Join(project, ".wuko", "workflows"), 0o755)
			if _, _, err := runShimCommand(t, project, home, "", append([]string{"shim", "install"}, test.args...)...); err == nil {
				t.Fatal("invalid installation succeeded")
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed preflight modified home: %v, %v", entries, err)
			}
		})
	}
}

func TestShimInstallProjectCanSkipPathSetup(t *testing.T) {
	t.Parallel()
	project, home := t.TempDir(), t.TempDir()
	testfs.MkdirAll(t, filepath.Join(project, ".wuko", "workflows"), 0o755)
	if _, _, err := runShimCommand(t, project, home, "", "shim", "install", "--project", ".", "--as", "mytools", "--no-path"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".zshrc", ".bashrc", "config"} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatalf("--no-path created %s: %v", name, err)
		}
	}
}

func TestProjectShimDirectoryLoadsDefaultsAndInheritedWorkflows(t *testing.T) {
	t.Parallel()
	caller, home := t.TempDir(), t.TempDir()
	project := filepath.Join(caller, "automation")
	testfs.Write(t, filepath.Join(project, ".wuko", "workflows", "check.yaml"), shimTestWorkflow)
	testfs.Write(t, filepath.Join(caller, ".wuko", "workflows", "parent.yaml"), strings.Replace(shimTestWorkflow, "name: check", "name: parent", 1))
	testfs.Write(t, filepath.Join(home, ".wuko", "workflows", "global.yaml"), strings.Replace(shimTestWorkflow, "name: check", "name: global", 1))
	testfs.Write(t, filepath.Join(project, "wuko.yml"), "default: {workflow: check}\nflags: {env-loader: [none]}\ncommands: {list: {flags: {format: json}}}\n")
	if _, _, err := runShimCommand(t, caller, home, "", "shim", "install", "--project", project, "--as", "mytools", "--no-path"); err != nil {
		t.Fatal(err)
	}
	shims, err := shim.List(filepath.Join(home, ".wuko", "bin"))
	if err != nil || len(shims) != 1 {
		t.Fatalf("shims = %+v, error = %v", shims, err)
	}
	stdout, _, err := runShimCommand(t, shims[0].Spec.Project, home, "", "list")
	if err != nil || !strings.Contains(stdout, `"schema_version"`) {
		t.Fatalf("configured list = %q, error = %v", stdout, err)
	}
	var listing struct {
		Workflows []struct{ Name string }
	}
	if err := json.Unmarshal([]byte(stdout), &listing); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, item := range listing.Workflows {
		names = append(names, item.Name)
	}
	if diff := cmp.Diff([]string{"check", "global", "parent"}, names); diff != "" {
		t.Fatalf("inherited workflows (-want +got):\n%s", diff)
	}
	var output, diagnostics bytes.Buffer
	deps := configTestDependencies(shims[0].Spec.Project, &output)
	deps.stderr = &diagnostics
	deps.registry = step.NewRegistry()
	if err := registerBuiltinSteps(deps.registry); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd(deps)
	root.SetArgs([]string{})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String()+diagnostics.String(), "hi") {
		t.Fatalf("default workflow output = %q, diagnostics = %q", output.String(), diagnostics.String())
	}
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

func TestShimUninstallSucceedsWithoutConfigDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := shim.Install(dir, "check", shim.Spec{Workflow: "check"}, false); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &stdout, stderr: &stdout,
		cwd:       func() (string, error) { return dir, nil },
		homeDir:   func() (string, error) { return dir, nil },
		configDir: func() (string, error) { return "", errors.New("$HOME is not defined") },
		registry:  step.NewRegistry(),
	})
	command.SetArgs([]string{"shim", "uninstall", "check", "--dir", dir})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "check")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shim still present: %v", err)
	}
}
