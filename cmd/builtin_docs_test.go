package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestEveryBuiltinStepIsDocumented(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob(filepath.Join("..", "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	var docs strings.Builder
	for _, path := range append(paths, filepath.Join("..", "README.md")) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		docs.Write(data)
	}
	registry := step.NewRegistry()
	if err := registerBuiltinSteps(registry); err != nil {
		t.Fatal(err)
	}
	text := docs.String()
	for name := range registry.Definitions() {
		t.Run(name, func(t *testing.T) {
			// The plain search settles most names; the regex over every document is slow under -race.
			if strings.Contains(text, "`"+name+"`") {
				return
			}
			mentioned := regexp.MustCompile("type: *" + regexp.QuoteMeta(name) + `\b`)
			if !mentioned.MatchString(text) {
				t.Fatalf("step type %q is not documented in docs/ or README.md", name)
			}
		})
	}
}
