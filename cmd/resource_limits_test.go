package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/localresource"
	"github.com/up2jj/wuko/process"
)

func TestResourceWorkerArgumentsPreserveValuesAndInheritedFlags(t *testing.T) {
	t.Parallel()
	root := newRootCmd(dependencies{})
	var got []string
	command, _, err := root.Find([]string{"run"})
	if err != nil {
		t.Fatal(err)
	}
	command.RunE = func(command *cobra.Command, args []string) error {
		got = resourceWorkerArgs(command, args)
		return nil
	}
	root.SetArgs([]string{"--debug", "--env-loader=none", "run", "--cpus=1.5", "--memory=512MiB", "--var=a=x,y", "--var=b=$HOME", "--env=TOKEN=a=b", "--reporter=plain", "--reporter=github", "--", "-workflow", "target"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--debug=true", "--env=TOKEN=a=b", "--env-loader=none", "--reporter=plain", "--reporter=github", "--var=a=x,y", "--var=b=$HOME", "--", "-workflow", "target"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("worker arguments = %#v, want %#v", got, want)
	}
}

func TestResourceRunPreservesHostEnvironmentAndCLIStreams(t *testing.T) {
	t.Setenv("WUKO_RESOURCE_TEST_VALUE", "a=$literal,b")
	var output, diagnostics bytes.Buffer
	input := strings.NewReader("stdin workflow")
	wantErr := errors.New("worker result")
	calls := 0
	dir := t.TempDir()
	root := newRootCmd(dependencies{
		stdin: input, stdout: &output, stderr: &diagnostics,
		cwd:        func() (string, error) { return dir, nil },
		executable: func() (string, error) { return "/usr/bin/wuko", nil },
		resourceRunner: func(_ context.Context, limits localresource.Limits, options process.Options) (process.Result, error) {
			calls++
			if limits.CPUs != 1.5 || limits.Memory != 512<<20 {
				t.Errorf("limits = %#v", limits)
			}
			if options.Env["WUKO_RESOURCE_TEST_VALUE"] != "a=$literal,b" {
				t.Error("worker lost the host environment")
			}
			if options.Dir != dir || options.Command != "/usr/bin/wuko" {
				t.Errorf("worker location = %q, %q", options.Dir, options.Command)
			}
			if options.Stdin != input || options.Stdout != &output || options.Stderr != &diagnostics {
				t.Error("worker lost CLI streams")
			}
			if options.StdoutPolicy != process.OutputInherit || options.StderrPolicy != process.OutputInherit {
				t.Error("worker output should stream without capture")
			}
			return process.Result{}, wantErr
		},
	})
	root.SetArgs([]string{"run", "--file=-", "--cpus=1.5", "--memory=512MiB"})
	if err := root.ExecuteContext(t.Context()); !errors.Is(err, wantErr) {
		t.Fatalf("Execute() = %v", err)
	}
	if calls != 1 {
		t.Fatalf("resource runner called %d times", calls)
	}
}

func TestRunResourceFlagsRejectInvalidValuesBeforeExecution(t *testing.T) {
	t.Parallel()
	for _, flags := range [][]string{{"--cpus=0"}, {"--cpus=NaN"}, {"--cpus="}, {"--memory=512MB"}, {"--memory="}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			var output bytes.Buffer
			root := newRootCmd(dependencies{stdout: &output, stderr: &output})
			root.SetArgs(append([]string{"run", "missing"}, flags...))
			if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "--") {
				t.Fatalf("Execute() = %v", err)
			}
		})
	}
}

func TestResourceDryRunRequiresNoSupervisor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	if err := os.WriteFile(path, []byte(`version: 1
name: dry
steps:
  - return:
      outputs:
        result: '"ready"'
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	root := newRootCmd(dependencies{
		stdin: strings.NewReader(""), stdout: &output, stderr: &diagnostics,
		cwd:       func() (string, error) { return dir, nil },
		homeDir:   func() (string, error) { return dir, nil },
		configDir: func() (string, error) { return dir, nil },
		executable: func() (string, error) {
			t.Error("dry run attempted re-execution")
			return "", errors.New("unavailable")
		},
		resourceRunner: func(context.Context, localresource.Limits, process.Options) (process.Result, error) {
			t.Error("dry run started resource supervision")
			return process.Result{}, errors.New("unavailable")
		},
	})
	root.SetArgs([]string{"--env-loader=none", "run", "--file=" + path, "--dry-run", "--cpus=1.5", "--memory=512MiB"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Workflow dry") {
		t.Fatalf("missing dry-run plan: %q", output.String())
	}
}
