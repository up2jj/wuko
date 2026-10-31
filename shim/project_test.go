package shim

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/internal/testhome"
)

func TestMain(main *testing.M) { testhome.Run(main) }

func TestProjectShimForwardsInvocation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	project := filepath.Join(root, "project with 'quotes' $dollars `backticks`\nand newline")
	testfs.MkdirAll(t, project, 0o755)
	project, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	testfs.MkdirAll(t, bin, 0o755)
	testfs.WriteFile(t, filepath.Join(bin, "wuko"), []byte("#!/bin/sh\nprintf '%s\\000' \"$PWD\" \"$@\"\ncat\nprintf 'runtime stderr' >&2\nexit 23\n"), 0o755)
	installed, err := Install(bin, "mytools", Spec{Project: project}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"list"}, {"run", "deploy", "production", "--var", "value=it's $literal `text`", "", "line\nbreak"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			command := exec.CommandContext(t.Context(), installed.Path, args...)
			command.Dir = t.TempDir()
			command.Env = []string{"HOME=" + root, "PATH=" + bin + ":/usr/bin:/bin", "CDPATH=/does/not/exist"}
			command.Stdin = strings.NewReader("stdin payload")
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			var exit *exec.ExitError
			if err := command.Run(); !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("Run = %v, want exit 23", err)
			}
			want := strings.Join(append([]string{project}, args...), "\x00") + "\x00stdin payload"
			if diff := cmp.Diff(want, stdout.String()); diff != "" {
				t.Fatalf("stdout (-want +got):\n%s", diff)
			}
			if stderr.String() != "runtime stderr" {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestProjectShimReportsMissingProjectAndRuntime(t *testing.T) {
	t.Parallel()
	for _, missingProject := range []bool{true, false} {
		t.Run(map[bool]string{true: "project", false: "runtime"}[missingProject], func(t *testing.T) {
			t.Parallel()
			project, bin := t.TempDir(), t.TempDir()
			installed, err := Install(bin, "mytools", Spec{Project: project}, false)
			if err != nil {
				t.Fatal(err)
			}
			want := "wuko is not on PATH"
			if missingProject {
				if err := os.Remove(project); err != nil {
					t.Fatal(err)
				}
				want = "cannot enter project"
			}
			command := exec.CommandContext(t.Context(), installed.Path, "list")
			command.Env = []string{"PATH="}
			out, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(out), want) {
				t.Fatalf("output = %q, error = %v, want %q", out, err, want)
			}
		})
	}
}

func TestProjectSpecValidationAndLegacyMetadata(t *testing.T) {
	t.Parallel()
	for _, spec := range []Spec{
		{Project: "relative"}, {Project: "/project", File: "/a.yaml"},
		{Project: "/project", Workflow: "check"}, {Project: "/project", Locator: "https://example.test/workflow.yaml"},
		{Project: "/project", Target: "prod"}, {Project: "/project", Args: []string{"--no-config"}},
	} {
		if _, err := Render("tool", spec); err == nil {
			t.Fatalf("Render(%+v) succeeded", spec)
		}
	}
	for _, spec := range []Spec{{Project: "/project"}, {File: "/workflow.yaml"}, {Workflow: "check"}, {Locator: "https://example.test/check.yaml"}} {
		data, err := Render("tool", spec)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(spec, got); diff != "" {
			t.Fatalf("round trip (-want +got):\n%s", diff)
		}
	}
	if _, err := Parse([]byte("#!/bin/sh\n# wuko-shim: {\"project\":\"relative\"}\n")); err == nil {
		t.Fatal("invalid project metadata accepted")
	}
}

func TestInvalidShimMetadataCanBeReplacedOrRemoved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "tool")
	invalid := "#!/bin/sh\n# wuko-shim: {\"project\":\"relative\"}\n"
	testfs.Write(t, path, invalid)
	if _, err := Install(dir, "tool", Spec{Project: "/project"}, false); !errors.Is(err, ErrExists) {
		t.Fatalf("Install without force error = %v, want ErrExists", err)
	}
	if _, err := Install(dir, "tool", Spec{Project: "/project"}, true); err != nil {
		t.Fatalf("forced Install: %v", err)
	}
	testfs.Write(t, path, invalid)
	if _, err := Remove(dir, "tool"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid shim left in place: %v", err)
	}
}
