//go:build darwin || linux

package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/testfs"
)

const replaceHelperEnv = "WUKO_TEST_REPLACE_HELPER_DIR"

// TestReplaceHelperProcess is the process that replaces itself; it does nothing in a normal run.
func TestReplaceHelperProcess(t *testing.T) {
	dir := os.Getenv(replaceHelperEnv)
	if dir == "" {
		t.Skip("helper process only")
	}
	replacement, err := ResolveReplacement([]string{"sh", "-c", `echo "$$ $(pwd -P) $FOO"`}, dir, map[string]string{"FOO": "bar", "PATH": os.Getenv("PATH")})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Printf("%d ", os.Getpid())
	err = Replace(replacement)
	fmt.Fprintln(os.Stderr, err)
	os.Exit(3)
}

func TestReplaceKeepsProcessIdentityAndAppliesDirAndEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestReplaceHelperProcess$")
	command.Env = append(os.Environ(), replaceHelperEnv+"="+dir)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("helper failed: %v; output %q", err, output)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 4 {
		t.Fatalf("output = %q, want pid, replaced pid, dir, and env", output)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{fields[0], fields[0], resolved, "bar"}
	if diff := cmp.Diff(want, fields); diff != "" {
		t.Fatalf("replaced process mismatch (-want +got):\n%s", diff)
	}
}

func TestResolveReplacementRejectsUnusablePrograms(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testfs.WriteFile(t, filepath.Join(dir, "plain"), []byte("data"), 0o644)
	tests := []struct {
		name string
		argv []string
		dir  string
		want string
	}{
		{name: "empty", argv: nil, dir: dir, want: "replacement command is empty"},
		{name: "missing", argv: []string{"wuko-missing-program"}, dir: dir, want: "resolving replacement command"},
		{name: "not executable", argv: []string{"./plain"}, dir: dir, want: "is not executable"},
		{name: "directory", argv: []string{dir}, dir: dir, want: "is not executable"},
		{name: "missing dir", argv: []string{"sh"}, dir: filepath.Join(dir, "absent"), want: "replacement working directory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ResolveReplacement(test.argv, test.dir, map[string]string{"PATH": dir})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ResolveReplacement() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestReplaceReportsExecFailure is not parallel: Replace changes into Dir, which is
// process-wide, so it stays in the current directory.
func TestReplaceReportsExecFailure(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	err = Replace(Replacement{Path: filepath.Join(t.TempDir(), "absent"), Argv: []string{"absent"}, Dir: wd})
	if err == nil || !strings.Contains(err.Error(), "replacing wuko with") {
		t.Fatalf("Replace() error = %v", err)
	}
}
