package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/tui"
)

func TestWorkflowListingsSkipInvalidDraftsAndWarnOnStderr(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		all  bool
		json bool
	}{
		{name: "list", args: []string{"list"}},
		{name: "list JSON", args: []string{"list", "--format", "json"}, json: true},
		{name: "list all", args: []string{"list", "--all"}, all: true},
		{name: "list all JSON", args: []string{"list", "--all", "--format", "json"}, all: true, json: true},
		{name: "bare command"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			home := filepath.Join(root, "home")
			draft := filepath.Join(root, ".wuko", "workflows", "draft.yaml")
			testfs.Write(t, draft, "version: [")
			testfs.Write(t, filepath.Join(root, ".wuko", "workflows", "check.yaml"), "version: 1\nname: check\nsteps: [{return: {outputs: {}}}]\n")
			testfs.Write(t, filepath.Join(home, ".wuko", "workflows", "draft.yaml"), "version: 1\nname: draft\nsteps: [{return: {outputs: {}}}]\n")
			var stdout, stderr bytes.Buffer
			deps := listTestDependencies(root, &stdout)
			deps.stderr = &stderr
			deps.homeDir = func() (string, error) { return home, nil }
			command := newRootCmd(deps)
			command.SetArgs(test.args)
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if strings.Count(stderr.String(), "Warning:") != 1 || !strings.Contains(stderr.String(), draft) {
				t.Fatalf("stderr = %q, want one warning identifying the broken file", stderr.String())
			}
			if test.json {
				var document workflowListDocument
				if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
					t.Fatalf("invalid JSON stdout %q: %v", stdout.String(), err)
				}
				wantCount := 1
				if test.all {
					wantCount = 2
				}
				if len(document.Workflows) != wantCount || document.Workflows[0].Name != "check" {
					t.Fatalf("workflows = %#v", document.Workflows)
				}
				if test.all && (document.Workflows[1].Effective == nil || *document.Workflows[1].Effective) {
					t.Fatalf("global draft was promoted: %#v", document.Workflows[1])
				}
			} else if !strings.Contains(stdout.String(), "check\tlocal\t") || strings.Contains(stdout.String(), "draft\t") != test.all || strings.Contains(stdout.String(), "Warning:") {
				t.Fatalf("stdout = %q", stdout.String())
			}
		})
	}
}

func TestWorkflowPickerRunsValidWorkflowDespiteBrokenNamesake(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	for path, data := range map[string]string{
		filepath.Join(root, ".wuko", "workflows", "draft.yaml"): "version: [",
		filepath.Join(root, ".wuko", "workflows", "check.yaml"): "version: 1\nname: check\nsteps: [{return: {outputs: {}}}]\n",
		filepath.Join(home, ".wuko", "workflows", "draft.yaml"): "version: 1\nname: draft\nsteps: [{return: {outputs: {}}}]\n",
	} {
		testfs.Write(t, path, data)
	}
	deps := listTestDependencies(root, io.Discard)
	deps.homeDir = func() (string, error) { return home, nil }
	deps.isInteractive = func(io.Reader) bool { return true }
	selected := false
	deps.selectWorkflow = func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		if len(config.Options) != 1 || config.Options[0].Label != "check" || !strings.Contains(config.Notice, "Skipped 1 invalid workflow") {
			t.Fatalf("picker config = %#v", config)
		}
		selected = true
		return tui.Selection{Option: config.Options[0], Intent: tui.SelectionPrimary}, nil
	}
	command := newRootCmd(deps)
	command.SetArgs(nil)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !selected {
		t.Fatal("valid workflow was not offered")
	}
}

func TestWorkflowPickerKeepsEffectiveWorkflowWhenShadowedCopyIsInvalid(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	local := filepath.Join(root, ".wuko", "workflows", "check.yaml")
	testfs.Write(t, local, "version: 1\nname: check\nsteps: [{return: {outputs: {}}}]\n")
	testfs.Write(t, filepath.Join(home, ".wuko", "workflows", "check.yaml"), "version: [")
	sources, warnings, err := discoverWorkflowPickerSources(root, home, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Path != local || !sources[0].Effective {
		t.Fatalf("sources = %#v", sources)
	}
	if len(warnings) != 1 || warnings[0].Source.Effective {
		t.Fatalf("warnings = %#v", warnings)
	}
}

func TestWorkflowPickerRefreshSkipsInvalidEditedWorkflow(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, ".wuko", "workflows")
	for _, name := range []string{"check", "release"} {
		testfs.Write(t, filepath.Join(dir, name+".yaml"), "version: 1\nname: "+name+"\nsteps: [{return: {outputs: {}}}]\n")
	}
	deps := listTestDependencies(root, io.Discard)
	deps.isInteractive = func(io.Reader) bool { return true }
	deps.openEditor = func(_ context.Context, _ io.Reader, _, _ io.Writer, path string) error {
		testfs.Write(t, path, "version: [")
		return nil
	}
	selections := 0
	deps.selectWorkflow = func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		selections++
		if selections == 1 {
			if len(config.Options) != 2 || config.Options[1].Label != "release" {
				t.Fatalf("initial picker config = %#v", config)
			}
			return tui.Selection{Option: config.Options[1], Intent: tui.SelectionEditor, Filter: "rel"}, nil
		}
		if selections != 2 || len(config.Options) != 1 || config.Options[0].Label != "check" || config.InitialFilter != "rel" || !strings.Contains(config.Notice, "Skipped 1 invalid workflow") {
			t.Fatalf("refreshed picker config = %#v", config)
		}
		return tui.Selection{}, context.Canceled
	}
	command := newRootCmd(deps)
	command.SetArgs(nil)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if selections != 2 {
		t.Fatalf("selections = %d, want 2", selections)
	}
}

func TestWorkflowPickerReportsOnlyInvalidWorkflows(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testfs.Write(t, filepath.Join(root, ".wuko", "workflows", "draft.yaml"), "version: [")
	var stdout, stderr bytes.Buffer
	deps := listTestDependencies(root, &stdout)
	deps.stderr = &stderr
	deps.isInteractive = func(io.Reader) bool { return true }
	deps.selectWorkflow = func(context.Context, io.Reader, io.Writer, tui.SelectionPickerConfig) (tui.Selection, error) {
		t.Fatal("picker opened without any valid workflows")
		return tui.Selection{}, nil
	}
	command := newRootCmd(deps)
	command.SetArgs(nil)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != noValidWorkflowsHelp || !strings.Contains(stderr.String(), "draft.yaml") {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}
