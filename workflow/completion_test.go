package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompletionDiscoveryPreservesPrecedenceAndPackages(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	project, home := filepath.Join(root, "project"), filepath.Join(root, "home")
	local, global := filepath.Join(project, ".wuko", "workflows"), filepath.Join(home, ".wuko", "workflows")
	for path, content := range map[string]string{
		filepath.Join(local, "draft.yaml"):                                        "version: [",
		filepath.Join(global, "draft.yaml"):                                       "targets: {wrong: {}}",
		filepath.Join(local, "private.yaml"):                                      "invokable: false",
		filepath.Join(global, "private.yaml"):                                     "invokable: true",
		filepath.Join(local, "nested", "check.yaml"):                              "description: Check",
		filepath.Join(local, "marketplace", "release", "wuko.yaml"):               "name: packaged\ntargets: {staging: {description: Stage}}",
		filepath.Join(local, "marketplace", "release", WorkflowPackageMarkerName): `{"version":1,"name":"packaged"}`,
		filepath.Join(local, "marketplace", "release", "fragment.yaml"):           "version: [",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := DiscoverForCompletion(project, home, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 {
		t.Fatalf("sources = %#v", sources)
	}
	if sources[0].Name != "nested/check" || sources[0].Description != "Check" || !sources[0].Invokable {
		t.Fatalf("nested workflow = %#v", sources[0])
	}
	if sources[1].Name != "packaged" || sources[1].Target != "staging" || sources[1].Description != "Stage" {
		t.Fatalf("package = %#v", sources[1])
	}
	if sources[2].Name != "private" || sources[2].Invokable || sources[2].Path != filepath.Join(local, "private.yaml") {
		t.Fatalf("dependency = %#v", sources[2])
	}
}

func TestCompletionDiscoveryReportsDirectoryErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".wuko"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverForCompletion(root, "", ""); err == nil {
		t.Fatal("expected invalid workflow directory to fail discovery")
	}
}
