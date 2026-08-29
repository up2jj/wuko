package engine

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/step"
	scaffoldstep "github.com/up2jj/wuko/steps/scaffold"
	"github.com/up2jj/wuko/workflow"
)

func TestScaffoldUsesWorkflowTemplatesAndPackageDirectory(t *testing.T) {
	workflowDir := t.TempDir()
	templateDir := filepath.Join(workflowDir, "templates", "service")
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "{{ .vars.name }}.txt"), []byte(`{{ template "header" . }}{{ .vars.name }}`), 0o644); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "scaffold", Dir: workflowDir,
		Vars:      map[string]any{"name": "billing"},
		Templates: map[string]workflow.TemplateDefinition{"header": {Inline: "service="}},
		Steps: []workflow.Step{{ID: "generate", Type: "scaffold", With: map[string]any{
			"from": "templates/service", "into": "generated",
		}}},
	}
	registry := step.NewRegistry()
	if err := scaffoldstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	state, err := New(registry).Run(t.Context(), definition, Options{RunDir: runDir, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "generated", "billing.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "service=billing" || state.Steps["generate"].(map[string]any)["created"] != 1 {
		t.Fatalf("content = %q, outputs = %#v", data, state.Steps["generate"])
	}
}

func TestScaffoldUsesMaterializedActionArchive(t *testing.T) {
	action := &workflow.Action{
		Version: 1, Name: "generator",
		Inputs:    map[string]workflow.ActionInput{"name": {Type: "string", Required: true}},
		Templates: map[string]workflow.TemplateDefinition{"header": {Inline: "generated="}},
		Steps: []workflow.Step{{ID: "generate", Type: "scaffold", With: map[string]any{
			"from": "templates/service", "into": "services/{{ .inputs.name }}",
		}}},
		Files: map[string]workflow.ActionFile{
			"action.yaml": {Data: []byte("version: 1\nname: generator\nsteps: []\n"), Mode: 0o644},
			"templates/service/{{ .inputs.name }}.txt": {Data: []byte(`{{ template "header" . }}{{ .inputs.name }}`), Mode: 0o755},
		},
		Location: diagnostic.Location{Source: "https://example.test/generator.zip::action.yaml"},
	}
	definition := &workflow.Definition{
		Version: 1, Name: "caller", Dir: t.TempDir(),
		Steps: []workflow.Step{{ID: "call", Uses: workflow.ActionSource{URL: "https://example.test/generator.zip"}, Action: action, With: map[string]any{"name": "billing"}}},
	}
	registry := step.NewRegistry()
	if err := scaffoldstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	if _, err := New(registry).Run(t.Context(), definition, Options{RunDir: runDir, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runDir, "services", "billing", "billing.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "generated=billing" {
		t.Fatalf("content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %04o", info.Mode().Perm())
	}
}

func TestScaffoldRefusesRemoteManifestActionUsingCallerPackage(t *testing.T) {
	callerDir := t.TempDir()
	templateDir := filepath.Join(callerDir, "templates", "service")
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "leak.txt"), []byte("caller secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A remote action fetched as a plain manifest carries no files, so the loader hands it the
	// caller's directory. The scaffold step must refuse it instead of copying the caller's tree.
	action := &workflow.Action{
		Version: 1, Name: "generator", Dir: callerDir, DirBorrowed: true,
		Steps: []workflow.Step{{ID: "generate", Type: "scaffold", With: map[string]any{
			"from": "templates/service", "into": "stolen",
		}}},
		Location: diagnostic.Location{Source: "https://example.test/action.yaml"},
	}
	definition := &workflow.Definition{
		Version: 1, Name: "caller", Dir: callerDir,
		Steps: []workflow.Step{{ID: "call", Uses: workflow.ActionSource{URL: "https://example.test/action.yaml"}, Action: action}},
	}
	registry := step.NewRegistry()
	if err := scaffoldstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	_, err := New(registry).Run(t.Context(), definition, Options{RunDir: runDir, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "requires a packaged action") {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "stolen")); !os.IsNotExist(err) {
		t.Fatalf("caller package was copied: %v", err)
	}
}
