package workflow

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/testfs"
)

func TestBrowsingDiscoverySkipsInvalidWorkflowsWithoutPromotingNamesakes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, data string
	}{
		{name: "syntax", data: "version: ["},
		{name: "header", data: "version: 2\nname: draft\nsteps: [{return: {outputs: {}}}]\n"},
		{name: "schema", data: "version: 1\nname: draft\nsteps: [{id: duplicate, type: shell}, {id: duplicate, type: shell}]\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			project, home := filepath.Join(root, "project"), filepath.Join(root, "home")
			local, global := filepath.Join(project, ".wuko", "workflows"), filepath.Join(home, ".wuko", "workflows")
			draft := filepath.Join(local, "draft.yaml")
			testfs.Write(t, draft, test.data)
			testfs.Write(t, filepath.Join(local, "nested", "check.yaml"), "version: 1\nname: check\nsteps: [{return: {outputs: {}}}]\n")
			testfs.Write(t, filepath.Join(global, "draft.yaml"), "version: 1\nname: draft\ntargets: {staging: {steps: [{return: {outputs: {}}}]}}\n")
			sources, warnings, err := DiscoverForBrowsing(project, home, "")
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, source := range sources {
				got = append(got, source.Name+"/"+source.Target)
				if source.Effective != (source.Name == "nested/check") {
					t.Fatalf("unexpected precedence: %#v", source)
				}
			}
			if diff := cmp.Diff([]string{"draft/staging", "nested/check/"}, got); diff != "" {
				t.Fatalf("sources (-want +got):\n%s", diff)
			}
			if len(warnings) != 1 || warnings[0].Source.Path != draft || warnings[0].Source.Name != "draft" || !warnings[0].Source.Effective || warnings[0].Err == nil {
				t.Fatalf("warnings = %#v", warnings)
			}
			if _, err := Discover(project, home, ""); err == nil {
				t.Fatal("strict discovery accepted invalid workflow")
			}
		})
	}
}

func TestBrowsingDiscoveryRecoversInvalidPackageIdentity(t *testing.T) {
	t.Parallel()
	for _, manifest := range []string{"version: [", "version: 2\nname: release\n"} {
		t.Run(manifest, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			home := filepath.Join(root, "home")
			packageDir := filepath.Join(root, ".wuko", "workflows", "marketplace", "installed-package")
			path := filepath.Join(packageDir, "wuko.yaml")
			testfs.Write(t, path, manifest)
			testfs.Write(t, filepath.Join(packageDir, WorkflowPackageMarkerName), `{"version":1,"name":"release"}`)
			testfs.Write(t, filepath.Join(home, ".wuko", "workflows", "release.yaml"), "version: 1\nname: release\nsteps: [{return: {outputs: {}}}]\n")
			sources, warnings, err := DiscoverForBrowsing(root, home, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(sources) != 1 || sources[0].Name != "release" || sources[0].Effective {
				t.Fatalf("sources = %#v", sources)
			}
			if len(warnings) != 1 || warnings[0].Source.Name != "release" || !warnings[0].Source.Effective {
				t.Fatalf("warnings = %#v", warnings)
			}
		})
	}
}

func TestBrowsingDiscoveryReportsFilesystemAndIdentityErrors(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"directory", "missing fragment", "duplicate name", "unknown package name"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			dir := filepath.Join(root, ".wuko", "workflows")
			switch scenario {
			case "directory":
				testfs.Write(t, dir, "not a directory")
			case "missing fragment":
				testfs.Write(t, filepath.Join(dir, "draft.yaml"), "version: 1\nname: draft\nsteps: [{require: missing.yaml}]\n")
			case "duplicate name":
				for _, ext := range []string{"yaml", "yml"} {
					testfs.Write(t, filepath.Join(dir, "draft."+ext), "version: [")
				}
			case "unknown package name":
				testfs.Write(t, filepath.Join(dir, "package", WorkflowPackageMarkerName), `{}`)
				testfs.Write(t, filepath.Join(dir, "package", "wuko.yaml"), "version: [")
			}
			if _, _, err := DiscoverForBrowsing(root, "", ""); err == nil {
				t.Fatal("expected discovery error")
			}
		})
	}
}

func TestBrowsingDiscoveryDeduplicatesWarnings(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".wuko", "workflows", "draft.yaml")
	testfs.Write(t, path, "version: [")
	testfs.MkdirAll(t, filepath.Join(filepath.Dir(path), ".wuko", "workflows"), 0o755)
	_, warnings, err := DiscoverForBrowsing(filepath.Dir(path), root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Err.Error(), "draft.yaml") {
		t.Fatalf("warnings = %#v", warnings)
	}
}
