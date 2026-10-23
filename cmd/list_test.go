package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
)

func TestListCommandPreservesTextOutput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	testfs.MkdirAll(t, workflowDir, 0o755)
	path := filepath.Join(workflowDir, "check.yaml")
	writeTestWorkflow(t, path, "Run checks")

	var output bytes.Buffer
	command := newRootCmd(listTestDependencies(root, &output))
	command.SetArgs([]string{"list"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := "check\tlocal\tRun checks\t" + path + "\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestListCommandWritesVersionedJSON(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	testfs.MkdirAll(t, workflowDir, 0o755)
	dependencyPath := filepath.Join(workflowDir, "build.yaml")
	dependency := `version: 1
package_version: 1.4.0
name: build
description: Build artifacts
invokable: false
depends_on: {source: prepare-source}
form: {title: Build}
steps:
  - return: {outputs: {}}
`
	testfs.WriteFile(t, dependencyPath, []byte(dependency), 0o644)
	deployPath := filepath.Join(workflowDir, "deploy.yaml")
	deploy := `version: 1
name: deploy
description: Deploy
targets:
  production:
    description: Deploy production
    steps:
      - return: {outputs: {}}
  staging:
    description: Deploy staging
    steps:
      - return: {outputs: {}}
`
	testfs.WriteFile(t, deployPath, []byte(deploy), 0o644)

	var output bytes.Buffer
	command := newRootCmd(listTestDependencies(root, &output))
	command.SetArgs([]string{"list", "--format", "json"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	var document workflowListDocument
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatalf("decoding output %q: %v", output.String(), err)
	}
	if document.SchemaVersion != workflowListSchemaVersion || len(document.Workflows) != 3 {
		t.Fatalf("document = %#v", document)
	}
	build := document.Workflows[0]
	if build.Name != "build" || build.Target != "" || build.Description != "Build artifacts" || build.Scope != "local" || build.Path != dependencyPath ||
		build.Invokable || !build.HasForm || build.PackageVersion != "1.4.0" || build.DependsOn["source"] != "prepare-source" {
		t.Fatalf("build source = %#v", build)
	}
	if document.Workflows[1].Name != "deploy" || document.Workflows[1].Target != "production" || document.Workflows[1].Description != "Deploy production" ||
		document.Workflows[2].Name != "deploy" || document.Workflows[2].Target != "staging" || document.Workflows[2].Description != "Deploy staging" {
		t.Fatalf("ordered targets = %#v", document.Workflows[1:])
	}
	for _, source := range document.Workflows[1:] {
		if source.DependsOn == nil || len(source.DependsOn) != 0 || source.PackageVersion != "" {
			t.Fatalf("empty fields = %#v, want empty object and string", source)
		}
	}
	for _, forbidden := range []string{"locator", "package_dir", "marketplace_url", "effective"} {
		if strings.Contains(output.String(), `"`+forbidden+`"`) {
			t.Fatalf("output exposes internal field %q: %s", forbidden, output.String())
		}
	}
}

func TestListCommandWritesEmptyJSONArray(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var output bytes.Buffer
	command := newRootCmd(listTestDependencies(root, &output))
	command.SetArgs([]string{"list", "--format=json"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"workflows":[]`) {
		t.Fatalf("output = %q, want empty workflow array", output.String())
	}
}

func TestListCommandAllIncludesShadowedTargets(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(home, "project")
	paths := []string{
		filepath.Join(project, ".wuko", "workflows", "deploy.yaml"),
		filepath.Join(home, ".wuko", "workflows", "deploy.yaml"),
	}
	for _, path := range paths {
		testfs.MkdirAll(t, filepath.Dir(path), 0o755)
		data := "version: 1\nname: deploy\ntargets:\n  production:\n    steps: [{return: {outputs: {}}}]\n  staging:\n    steps: [{return: {outputs: {}}}]\n"
		testfs.WriteFile(t, path, []byte(data), 0o644)
	}
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			deps := listTestDependencies(project, &output)
			deps.homeDir = func() (string, error) { return home, nil }
			command := newRootCmd(deps)
			command.SetArgs([]string{"list", "--all", "--format", format})
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if format == "text" {
				if strings.Count(output.String(), "effective\tdeploy\tlocal\t") != 2 || strings.Count(output.String(), "shadowed\tdeploy\tglobal\t") != 2 {
					t.Fatalf("output = %q", output.String())
				}
				for _, path := range paths {
					if strings.Count(output.String(), path) != 2 {
						t.Fatalf("output = %q, want both targets for %s", output.String(), path)
					}
				}
				return
			}
			var document workflowListDocument
			if err := json.Unmarshal(output.Bytes(), &document); err != nil {
				t.Fatal(err)
			}
			if document.SchemaVersion != 1 || len(document.Workflows) != 4 {
				t.Fatalf("document = %#v", document)
			}
			for i, entry := range document.Workflows {
				wantEffective := i%2 == 0
				if entry.Effective == nil || *entry.Effective != wantEffective || entry.Path != paths[i%2] {
					t.Fatalf("entry %d = %#v", i, entry)
				}
				wantScope := "global"
				if wantEffective {
					wantScope = "local"
				}
				if entry.Scope != wantScope {
					t.Fatalf("entry %d scope = %q, want %q", i, entry.Scope, wantScope)
				}
				wantTarget := "production"
				if i >= 2 {
					wantTarget = "staging"
				}
				if entry.Target != wantTarget {
					t.Fatalf("entry %d target = %q, want %q", i, entry.Target, wantTarget)
				}
			}
		})
	}
}

func TestListCommandRejectsInvalidFormatBeforeDiscovery(t *testing.T) {
	t.Parallel()
	discovered := false
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: io.Discard,
		cwd: func() (string, error) {
			discovered = true
			return t.TempDir(), nil
		},
		homeDir: func() (string, error) { return "", nil }, configDir: func() (string, error) { return "", nil },
		registry: step.NewRegistry(),
	})
	command.SetArgs([]string{"list", "--format", "yaml"})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "format must be text or json") {
		t.Fatalf("error = %v, want invalid format", err)
	}
	if discovered || output.Len() != 0 {
		t.Fatalf("discovered = %v, output = %q", discovered, output.String())
	}
}

func listTestDependencies(root string, stdout io.Writer) dependencies {
	return dependencies{
		stdin: bytes.NewReader(nil), stdout: stdout, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil },
		configDir: func() (string, error) { return "", nil }, registry: step.NewRegistry(),
	}
}
