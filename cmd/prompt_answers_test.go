package cmd

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
	confirmstep "github.com/up2jj/wuko/steps/confirm"
	inputstep "github.com/up2jj/wuko/steps/input"
)

const promptAnswersWorkflow = `version: 1
name: bootstrap
steps:
  - id: offer
    type: tui_confirm
    with: {variable: install, message: "Install now?"}
  - id: branch
    type: tui_input
    with: {variable: branch, message: Branch, value: main}
  - id: record
    type: record
    with: {interactive: "{{ .run.interactive }}"}
`

func TestRunAnswersPromptsFromFlags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		want    map[string]any
		wantErr string
	}{
		{name: "no flags fail without a terminal", wantErr: `variable "install" is required`},
		{name: "yes answers confirmations only", args: []string{"--yes"}, wantErr: `variable "branch" is required`},
		{name: "defaults answer prompts with defaults", args: []string{"--defaults", "--var", "install=true"}, want: map[string]any{"install": true, "branch": "main", "interactive": "false"}},
		{name: "yes and defaults", args: []string{"--yes", "--defaults"}, want: map[string]any{"install": true, "branch": "main", "interactive": "false"}},
		{name: "var wins over both", args: []string{"--yes", "--defaults", "--var", "install=false", "--var", `branch="dev"`}, want: map[string]any{"install": false, "branch": "dev", "interactive": "false"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "workflow.yaml")
			testfs.WriteFile(t, path, []byte(promptAnswersWorkflow), 0o644)
			deps := listTestDependencies(root, io.Discard)
			for _, register := range []func(*step.Registry) error{confirmstep.Register, inputstep.Register} {
				if err := register(deps.registry); err != nil {
					t.Fatal(err)
				}
			}
			var got map[string]any
			if err := deps.registry.Register("record", func(raw map[string]any) (step.Runner, error) {
				return steptest.RunnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
					got = map[string]any{"install": request.Vars["install"], "branch": request.Vars["branch"], "interactive": raw["interactive"]}
					return step.Result{}, nil
				}), nil
			}); err != nil {
				t.Fatal(err)
			}
			command := newRootCmd(deps)
			command.SetArgs(append([]string{"run", "--file", path}, test.args...))
			err := command.ExecuteContext(t.Context())
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Fatalf("answers (-want +got):\n%s", diff)
			}
		})
	}
}
