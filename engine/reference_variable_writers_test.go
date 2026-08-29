package engine

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/extract"
	importvars "github.com/up2jj/wuko/steps/import_vars"
	"github.com/up2jj/wuko/steps/jsonpath"
	keyvaluestep "github.com/up2jj/wuko/steps/key_value"
	luastep "github.com/up2jj/wuko/steps/lua"
	"github.com/up2jj/wuko/steps/semver"
	"github.com/up2jj/wuko/steps/set"
	timestep "github.com/up2jj/wuko/steps/time"
)

// The variable-writer tables are keyed by step type name, so a renamed or
// removed step type would silently reintroduce the false positives they exist
// to prevent. Pin every name to a real registration. The tui_* prompts are
// checked by name only: their packages import tui, which imports engine.
func TestVariableWriterTablesNameRegisteredSteps(t *testing.T) {
	registry := step.NewRegistry()
	for _, register := range []func(*step.Registry) error{
		set.Register, jsonpath.Register, semver.Register, keyvaluestep.Register, timestep.Register,
		extract.Register, luastep.Register, importvars.Register,
	} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	stub := func(map[string]any) (step.Runner, error) { return nil, nil }
	names := slices.Sorted(slices.Values(slices.Concat(
		slices.Collect(maps.Keys(varWriters)), slices.Collect(maps.Keys(dynamicVarWriters)), []string{"extract"},
	)))
	for _, name := range names {
		if strings.HasPrefix(name, "tui_") {
			continue
		}
		if err := registry.Register(name, stub); err == nil {
			t.Fatalf("step type %q is not registered by any variable-writing step", name)
		}
	}
	for _, name := range []string{"tui_choice", "tui_confirm", "tui_input", "tui_password", "tui_path", "tui_review"} {
		if _, listed := varWriters[name]; !listed {
			t.Fatalf("prompt step %q no longer declares the variable it assigns", name)
		}
	}
}
