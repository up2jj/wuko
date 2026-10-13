package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

// The README gallery is recorded from these fixtures, so they must stay valid as the schema evolves.
func TestGalleryFixturesValidate(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "docs", "demos", "gallery")
	paths, err := filepath.Glob(filepath.Join(root, ".wuko", "workflows", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no gallery fixtures found")
	}
	registry := step.NewRegistry()
	if err := registerBuiltinSteps(registry); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			if err := validateGalleryFixture(t, registry, root, path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The validation demo depends on this fixture failing with a suggestion.
func TestGalleryBrokenFixtureReportsUnknownVariable(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "docs", "demos", "gallery", "broken")
	registry := step.NewRegistry()
	if err := registerBuiltinSteps(registry); err != nil {
		t.Fatal(err)
	}
	err := validateGalleryFixture(t, registry, root, filepath.Join(root, ".wuko", "workflows", "deploy.yaml"))
	if err == nil || !strings.Contains(err.Error(), `"targt"`) {
		t.Fatalf("expected an unknown variable error for targt, got %v", err)
	}
}

func validateGalleryFixture(t *testing.T, registry *step.Registry, root, path string) error {
	t.Helper()
	runDir, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.NewLoader(nil).Load(t.Context(), path, workflow.LoadOptions{RunDir: runDir})
	if err != nil {
		return err
	}
	return engine.New(registry).Validate(t.Context(), definition, engine.Options{RunDir: runDir})
}
