package marketplace

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/workflow"
)

// discoverWriteFiles creates each relative file under root with placeholder content.
func discoverWriteFiles(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, name := range files {
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("version: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiscoverPackagesFindsNestedPackagesInOrder(t *testing.T) {
	root := t.TempDir()
	discoverWriteFiles(t, root,
		"zeta/wuko.yaml",
		"alpha/deploy/wuko.yml",
		"alpha/deploy/nested/wuko.yaml", // inside a package, so not a package of its own
		"alpha/deploy/scripts/run.sh",
		"alpha/build/wuko.yaml",
	)
	if err := os.MkdirAll(filepath.Join(root, "empty", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	packages, err := DiscoverPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []SourcePackage{
		{Directory: filepath.Join(root, "alpha", "build"), ManifestPath: filepath.Join(root, "alpha", "build", "wuko.yaml")},
		{Directory: filepath.Join(root, "alpha", "deploy"), ManifestPath: filepath.Join(root, "alpha", "deploy", "wuko.yml")},
		{Directory: filepath.Join(root, "zeta"), ManifestPath: filepath.Join(root, "zeta", "wuko.yaml")},
	}
	if !slices.Equal(packages, want) {
		t.Fatalf("packages = %#v, want %#v", packages, want)
	}
}

func TestDiscoverPackagesTreatsRootManifestAsSinglePackage(t *testing.T) {
	root := t.TempDir()
	discoverWriteFiles(t, root, "wuko.yaml", "child/wuko.yaml")
	packages, err := DiscoverPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 1 || packages[0].Directory != root || packages[0].ManifestPath != filepath.Join(root, "wuko.yaml") {
		t.Fatalf("packages = %#v", packages)
	}
}

func TestDiscoverPackagesReturnsNothingForEmptyRoot(t *testing.T) {
	packages, err := DiscoverPackages(t.TempDir())
	if err != nil || len(packages) != 0 {
		t.Fatalf("packages = %#v, err = %v", packages, err)
	}
}

func TestDiscoverPackagesRejectsInvalidLayouts(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  string
	}{
		{
			name:  "stray file outside a package",
			setup: func(t *testing.T, root string) { discoverWriteFiles(t, root, "group/notes.txt") },
			want:  "has no root wuko.yaml or wuko.yml",
		},
		{
			name:  "both manifest spellings",
			setup: func(t *testing.T, root string) { discoverWriteFiles(t, root, "pkg/wuko.yaml", "pkg/wuko.yml") },
			want:  "both wuko.yaml and wuko.yml",
		},
		{
			name: "manifest is a directory",
			setup: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "pkg", "wuko.yaml"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "is not a regular file",
		},
		{
			name: "symlinked directory",
			setup: func(t *testing.T, root string) {
				target := t.TempDir()
				discoverWriteFiles(t, target, "wuko.yaml")
				if err := os.Symlink(target, filepath.Join(root, "linked")); err != nil {
					t.Fatal(err)
				}
			},
			want: "contains symlink",
		},
		{
			name:  "missing root",
			setup: func(t *testing.T, root string) {},
			want:  "reading marketplace workflow directory",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.name == "missing root" {
				root = filepath.Join(root, "absent")
			}
			test.setup(t, root)
			packages, err := DiscoverPackages(root)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "discovering marketplace packages") {
				t.Fatalf("DiscoverPackages() = %#v, %v; want error containing %q", packages, err, test.want)
			}
		})
	}
}

func TestPluginSourceRootIsUnderWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	root := PluginSourceRoot(cwd)
	if want := filepath.Join(cwd, ".wuko", "plugin-sources"); root != want {
		t.Fatalf("PluginSourceRoot() = %q, want %q", root, want)
	}
	if root != filepath.Join(cwd, filepath.FromSlash(workflow.MarketplacePluginSourceDir)) {
		t.Fatalf("PluginSourceRoot() = %q does not follow workflow.MarketplacePluginSourceDir", root)
	}
	// Imported releases have no executable, so they must stay out of the local plugin
	// installation root that plugin discovery scans.
	installRoot := filepath.Join(cwd, ".wuko", "plugins")
	if inside, err := filepath.Rel(installRoot, root); err == nil && inside != ".." && !strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		t.Fatalf("PluginSourceRoot() = %q lies inside the plugin installation root %q", root, installRoot)
	}
	relative, err := filepath.Rel(cwd, root)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("PluginSourceRoot() = %q escapes %q", root, cwd)
	}
	if PluginSourceRoot("relative/project") != filepath.Join("relative", "project", ".wuko", "plugin-sources") {
		t.Fatalf("PluginSourceRoot() for relative cwd = %q", PluginSourceRoot("relative/project"))
	}
}
