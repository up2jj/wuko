package workflow

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
)

func TestValidateStandaloneRejectsDirectoryDependencies(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		definition Definition
		want       string
	}{
		{name: "self-contained", source: "version: 1\n", definition: Definition{Steps: []Step{{ID: "hello", Type: "shell", With: map[string]any{"script": "echo hello"}}}}},
		{name: "required files", source: "version: 1\nrequire:\n  - helper.sh\n", want: "required step files"},
		{name: "file template", source: "version: 1\n", definition: Definition{Templates: map[string]TemplateDefinition{"banner": {File: "banner.tmpl"}}}, want: `file-backed template "banner"`},
		{name: "nested local action", source: "version: 1\n", definition: Definition{Steps: []Step{{ID: "group", Concurrent: &ConcurrentGroup{Steps: []Step{{ID: "build", Uses: ActionSource{Path: "./actions/build"}}}}}}}, want: `local action "./actions/build"`},
		{name: "relative lifecycle script", source: "version: 1\n", definition: Definition{Install: []Step{{ID: "setup", Type: "shell", With: map[string]any{"script": "./setup.sh"}}}}, want: `relative lifecycle script "./setup.sh"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.yaml")
			testfs.WriteFile(t, path, []byte(test.source), 0o644)
			err := ValidateStandalone(path, &test.definition)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
