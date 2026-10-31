package workflow

import (
	"strings"
	"testing"
)

func TestStepBindingRulesClassifyPaths(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		stepType, path string
		owned, static  bool
	}{
		{"shell", "args.0", false, false},
		{"shell", "env.HOME", true, false},
		{"shell", "sdk", false, true},
		{"http", "json.expr", false, false},
		{"http", "until_expr", true, false},
		{"set", "variable", false, true},
		{"set", "variables.name", true, false},
		{"assert", "message", true, false},
		{"markdown_edit", "from.expr", true, false},
		{"markdown_edit", "from.file", false, false},
		{"tui_form", "fields", false, true},
		{"tui_form", "fields.0.id", false, true},
		{"tui_form", "fields.0.value", true, false},
		{"tui_form", "fields.0.label", false, false},
		{"dashboard", "groups.0.id", false, true},
		{"dashboard", "groups.0.widgets.1.id", false, true},
		{"dashboard", "groups.0.title", false, false},
		{"dashboard", "groups.0.widgets.1.refresh.steps", true, false},
		{"acme.plugin", "env", false, false},
	} {
		t.Run(tt.stepType+" "+tt.path, func(t *testing.T) {
			t.Parallel()
			path := strings.Split(tt.path, ".")
			if got := StepOwnsBindings(tt.stepType, path); got != tt.owned {
				t.Errorf("StepOwnsBindings()=%v, want %v", got, tt.owned)
			}
			if got := StaticStepBindingField(tt.stepType, path); got != tt.static {
				t.Errorf("StaticStepBindingField()=%v, want %v", got, tt.static)
			}
		})
	}
}
