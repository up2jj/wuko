package workflow

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
)

func TestSelectSourceSuggestsWorkflowNames(t *testing.T) {
	t.Parallel()
	sources := []Source{{Name: "deploy", Path: "deploy.yaml"}, {Name: "check", Path: "check.yaml"}, {Name: "deploy", Target: "staging"}}
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "depoly", want: `did you mean "deploy"?`},
		{name: "chek", want: `did you mean "check"?`},
		{name: "unrelated-workflow", want: "run 'wuko list'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := SelectSource(sources, test.name)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "wuko list") {
				t.Fatalf("SelectSource() error = %v, want %q and list hint", err, test.want)
			}
			if test.name == "unrelated-workflow" && strings.Contains(err.Error(), "did you mean") {
				t.Fatalf("unexpected weak suggestion: %v", err)
			}
		})
	}
	selected, err := SelectSource(sources, "deploy")
	if err != nil || selected.Path != "deploy.yaml" {
		t.Fatalf("exact selection = %#v, %v", selected, err)
	}
	if _, err := SelectSource(sources, "../deploy"); err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("invalid selector error = %v", err)
	}
	if _, err := SelectSource(nil, "deploy"); err == nil || strings.Contains(err.Error(), "did you mean") || !strings.Contains(err.Error(), "wuko list") {
		t.Fatalf("empty source error = %v", err)
	}
}

func TestFindLocalFileHintPreservesNameLookup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home, config := filepath.Join(root, "home"), filepath.Join(root, "config")
	local := filepath.Join(root, "tools", "deploy.YML")
	installed := filepath.Join(root, ".wuko", "workflows", "tools", "deploy.YML.yaml")
	for _, directory := range []string{filepath.Dir(local), filepath.Dir(installed), filepath.Join(root, "directory.yaml")} {
		testfs.MkdirAll(t, directory, 0o755)
	}
	writeWorkflow(t, local, "Local file")
	_, err := Find(root, home, config, "tools/deploy.YML")
	if err == nil || !strings.Contains(err.Error(), "--file") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Find() error = %v, want original error and file hint", err)
	}
	for _, selector := range []string{"missing.yaml", "directory.yaml", "unknown"} {
		_, err := Find(root, home, config, selector)
		if err == nil || strings.Contains(err.Error(), "--file") {
			t.Fatalf("Find(%q) error = %v, want no file hint", selector, err)
		}
	}
	writeWorkflow(t, installed, "Installed workflow")
	source, err := Find(root, home, config, "tools/deploy.YML")
	if err != nil || source.Path != installed {
		t.Fatalf("Find() = %#v, %v, want named workflow", source, err)
	}
}

func TestFindLocalFileHintPreservesDiscoveryErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeWorkflow(t, filepath.Join(root, "workflow.yaml"), "Local file")
	testfs.MkdirAll(t, filepath.Join(root, ".wuko"), 0o755)
	// A workflow directory that is actually a file must retain its discovery error.
	testfs.WriteFile(t, filepath.Join(root, ".wuko", "workflows"), nil, 0o600)
	_, err := Find(root, filepath.Join(root, "home"), filepath.Join(root, "config"), "workflow.yaml")
	if err == nil || strings.Contains(err.Error(), "--file") {
		t.Fatalf("Find() error = %v, want unmodified discovery failure", err)
	}
}

func TestDiscoverPrecedence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	nested := filepath.Join(project, "nested")
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	for _, dir := range []string{nested, filepath.Join(project, ".wuko", "workflows"), filepath.Join(home, ".wuko", "workflows"), filepath.Join(config, "wuko", "workflows")} {
		testfs.MkdirAll(t, dir, 0o755)
	}
	writeWorkflow(t, filepath.Join(config, "wuko", "workflows", "shared.yaml"), "config")
	writeWorkflow(t, filepath.Join(home, ".wuko", "workflows", "shared.yaml"), "home")
	projectPath := filepath.Join(project, ".wuko", "workflows", "shared.yaml")
	writeWorkflow(t, projectPath, "project")

	sources, err := Discover(nested, home, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Path != projectPath || sources[0].Description != "project" {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestDiscoverAllIncludesScopesAndEffectivePrecedence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	nested := filepath.Join(project, "nested")
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	for _, dir := range []string{
		nested,
		filepath.Join(project, ".wuko", "workflows"),
		filepath.Join(home, ".wuko", "workflows"),
		filepath.Join(config, "wuko", "workflows"),
	} {
		testfs.MkdirAll(t, dir, 0o755)
	}
	writeWorkflow(t, filepath.Join(project, ".wuko", "workflows", "shared.yaml"), "local")
	writeWorkflow(t, filepath.Join(home, ".wuko", "workflows", "shared.yaml"), "global home")
	writeWorkflow(t, filepath.Join(config, "wuko", "workflows", "config.yaml"), "global config")

	sources, err := DiscoverAll(nested, home, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 {
		t.Fatalf("sources = %#v, want 3 entries", sources)
	}
	if sources[0].Name != "config" || sources[0].Scope != "global" || !sources[0].Effective {
		t.Fatalf("sources[0] = %#v", sources[0])
	}
	if sources[1].Name != "shared" || sources[1].Scope != "local" || !sources[1].Effective {
		t.Fatalf("sources[1] = %#v", sources[1])
	}
	if sources[2].Name != "shared" || sources[2].Scope != "global" || sources[2].Effective {
		t.Fatalf("sources[2] = %#v", sources[2])
	}
}

func TestDiscoverHomeWorkflowsKeepGlobalScope(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(home, "project")
	nested := filepath.Join(project, "nested")
	config := filepath.Join(root, "config")
	homeOnly := filepath.Join(home, ".wuko", "workflows", "home-only.yaml")
	homeShared := filepath.Join(home, ".wuko", "workflows", "shared.yaml")
	projectShared := filepath.Join(project, ".wuko", "workflows", "shared.yaml")
	configOnly := filepath.Join(config, "wuko", "workflows", "config-only.yaml")
	for _, dir := range []string{nested, filepath.Dir(homeOnly), filepath.Dir(projectShared), filepath.Dir(configOnly)} {
		testfs.MkdirAll(t, dir, 0o755)
	}
	for _, path := range []string{homeOnly, homeShared, projectShared, configOnly} {
		writeWorkflow(t, path, "Scope regression")
	}
	for _, location := range []struct {
		name      string
		cwd       string
		withLocal bool
	}{
		{name: "home", cwd: home},
		{name: "project inside home", cwd: project, withLocal: true},
		{name: "nested project directory", cwd: nested, withLocal: true},
	} {
		for _, discovery := range []struct {
			name string
			run  func(string, string, string) ([]Source, error)
			all  bool
		}{
			{name: "effective", run: Discover},
			{name: "all", run: DiscoverAll, all: true},
			{name: "validation", run: DiscoverForValidation},
			{name: "completion", run: DiscoverForCompletion},
		} {
			t.Run(location.name+"/"+discovery.name, func(t *testing.T) {
				t.Parallel()
				sources, err := discovery.run(location.cwd, home, config)
				if err != nil {
					t.Fatal(err)
				}
				wantScopes := map[string]string{homeOnly: "global", configOnly: "global"}
				if location.withLocal {
					wantScopes[projectShared] = "local"
				}
				if !location.withLocal || discovery.all {
					wantScopes[homeShared] = "global"
				}
				for _, source := range sources {
					wantScope, found := wantScopes[source.Path]
					wantEffective := source.Path != homeShared || !location.withLocal
					if !found || source.Scope != wantScope || source.Effective != wantEffective {
						t.Fatalf("source = %#v, want scope %q, effective %t, present %t", source, wantScope, wantEffective, found)
					}
					delete(wantScopes, source.Path)
				}
				if len(wantScopes) != 0 {
					t.Fatalf("missing sources: %v", wantScopes)
				}
			})
		}
	}
}

func TestDiscoverIncludesWorkflowDependencies(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, ".wuko", "workflows")
	testfs.MkdirAll(t, dir, 0o755)
	data := "version: 1\npackage_version: 1.4.0\nname: release\ndepends_on: {build: build-artifacts}\nsteps:\n  - return: {outputs: {}}\n"
	testfs.WriteFile(t, filepath.Join(dir, "release.yaml"), []byte(data), 0o644)
	sources, err := Discover(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].DependsOn["build"] != "build-artifacts" || sources[0].PackageVersion != "1.4.0" {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestDiscoverExpandsWorkflowTargets(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, ".wuko", "workflows")
	testfs.MkdirAll(t, dir, 0o755)
	data := `version: 1
package_version: 1.4.0
name: deploy
description: shared
targets:
  production:
    description: production
    steps:
      - id: run
        type: shell
        with: {script: true}
  staging:
    description: staging
    steps:
      - id: run
        type: shell
        with: {script: true}
`
	testfs.WriteFile(t, filepath.Join(dir, "deploy.yaml"), []byte(data), 0o644)
	sources, err := Discover(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0].Target != "production" || sources[1].Target != "staging" {
		t.Fatalf("sources = %#v", sources)
	}
	if sources[0].Description != "production" || sources[1].Description != "staging" {
		t.Fatalf("target descriptions = %#v", sources)
	}
	if sources[0].PackageVersion != "1.4.0" || sources[1].PackageVersion != "1.4.0" {
		t.Fatalf("target package versions = %#v", sources)
	}
}

func TestDiscoverIncludesDependencyOnlyWorkflowMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, ".wuko", "workflows")
	testfs.MkdirAll(t, dir, 0o755)
	data := "version: 1\nname: build\ninvokable: false\nsteps:\n  - return: {outputs: {}}\n"
	testfs.WriteFile(t, filepath.Join(dir, "build.yaml"), []byte(data), 0o644)
	sources, err := Discover(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Invokable {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestDependencyOnlyWorkflowPreservesEffectivePrecedence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	home := filepath.Join(root, "home")
	projectDir := filepath.Join(project, ".wuko", "workflows")
	homeDir := filepath.Join(home, ".wuko", "workflows")
	for _, dir := range []string{projectDir, homeDir} {
		testfs.MkdirAll(t, dir, 0o755)
	}
	writeWorkflow(t, filepath.Join(homeDir, "build.yaml"), "global")
	data := "version: 1\nname: build\ndescription: local\ninvokable: false\nsteps:\n  - return: {outputs: {}}\n"
	localPath := filepath.Join(projectDir, "build.yaml")
	testfs.WriteFile(t, localPath, []byte(data), 0o644)
	sources, err := Discover(project, home, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Path != localPath || sources[0].Invokable {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestDiscoverRejectsDuplicateExtensions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, ".wuko", "workflows")
	testfs.MkdirAll(t, dir, 0o755)
	writeWorkflow(t, filepath.Join(dir, "same.yaml"), "one")
	writeWorkflow(t, filepath.Join(dir, "same.yml"), "two")
	if _, err := Discover(root, "", ""); err == nil {
		t.Fatal("expected duplicate workflow error")
	}
}

func TestDiscoverRecursesIntoWorkflowDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, ".wuko", "workflows")
	testfs.MkdirAll(t, filepath.Join(directory, "marketplace"), 0o755)
	writeWorkflow(t, filepath.Join(directory, "flat.yaml"), "flat")
	writeWorkflow(t, filepath.Join(directory, "marketplace", "release.yaml"), "nested")
	sources, err := Discover(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0].Name != "flat" || sources[1].Name != "marketplace/release" {
		t.Fatalf("sources = %#v", sources)
	}
	if source, err := Find(root, "", "", "marketplace/release"); err != nil || source.Path != filepath.Join(directory, "marketplace", "release.yaml") {
		t.Fatalf("Find() = %#v, %v", source, err)
	}
}

func TestDiscoverTreatsInstalledPackagesAsSingleWorkflowUnits(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, ".wuko", "workflows", "marketplace", "release")
	testfs.MkdirAll(t, directory, 0o755)
	testfs.WriteFile(t, filepath.Join(directory, "wuko.yaml"), []byte("version: 1\nname: packaged\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n"), 0o644)
	testfs.WriteFile(t, filepath.Join(directory, WorkflowPackageMarkerName), []byte(`{"version":1,"name":"packaged"}`), 0o644)
	sources, err := Discover(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Name != "packaged" || sources[0].PackageDir != directory || sources[0].Path != filepath.Join(directory, "wuko.yaml") || sources[0].MarketplaceURL != "" {
		t.Fatalf("sources = %#v", sources)
	}
	if source, err := Find(root, "", "", "packaged"); err != nil || source.PackageDir != directory {
		t.Fatalf("Find() = %#v, %v", source, err)
	}
}

func TestDiscoverIncludesInstalledMarketplaceURL(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, ".wuko", "workflows", "marketplace", "release")
	testfs.MkdirAll(t, directory, 0o755)
	testfs.WriteFile(t, filepath.Join(directory, "wuko.yaml"), []byte("version: 1\nname: release\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n"), 0o644)
	testfs.WriteFile(t, filepath.Join(directory, WorkflowPackageMarkerName), []byte(`{"version":1,"marketplace":"https://example.test/marketplace?token=ignored","name":"release"}`), 0o644)

	sources, err := Discover(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].MarketplaceURL != "https://example.test/marketplace/" {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestDiscoverDeduplicatesWorkflowRootsOverlappingWorkingDirectory(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	workflowRoot := filepath.Join(home, ".wuko", "workflows")
	packageDir := filepath.Join(workflowRoot, ".wuko", "workflows", "marketplace", "release")
	testfs.MkdirAll(t, packageDir, 0o755)
	testfs.WriteFile(t, filepath.Join(packageDir, "wuko.yaml"), []byte("version: 1\nname: packaged\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n"), 0o644)
	testfs.WriteFile(t, filepath.Join(packageDir, WorkflowPackageMarkerName), []byte(`{"version":1,"name":"packaged"}`), 0o644)

	sources, err := Discover(workflowRoot, home, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Name != "packaged" || sources[0].Path != filepath.Join(packageDir, "wuko.yaml") {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestLoadRejectsNonStringEnvironment(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bad.yaml")
	data := "version: 1\nname: bad\nenv:\n  PORT: 123\nsteps:\n  - id: run\n    type: shell\n    with: {}\n"
	testfs.WriteFile(t, path, []byte(data), 0o644)
	if _, err := Load(path); err == nil {
		t.Fatal("expected load error")
	}
}

func writeWorkflow(t *testing.T, path, description string) {
	t.Helper()
	data := "version: 1\nname: shared\ndescription: " + description + "\nsteps:\n  - id: run\n    type: shell\n    with:\n      command: true\n"
	testfs.WriteFile(t, path, []byte(data), 0o644)
}
