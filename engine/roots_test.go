package engine

import (
	"maps"
	"slices"
	"testing"

	"github.com/up2jj/wuko/step"
)

// The roots validation accepts are listed by hand, separately from the data the engine and
// steps actually build at run time. These tests keep the lists from drifting apart: a root
// added at run time but not here would be rejected by validation as unknown.
func TestValidationRootsCoverRuntimeTemplateData(t *testing.T) {
	t.Parallel()
	definition := testDefinition(t, "roots")
	data := templateData(definition, t.TempDir(), &State{})
	for _, name := range slices.Sorted(maps.Keys(data)) {
		if _, ok := baseTemplateRoots[name]; !ok {
			t.Errorf("template data root %q is missing from baseTemplateRoots", name)
		}
	}
}

func TestValidationRootsCoverStepExpressionEnvironment(t *testing.T) {
	t.Parallel()
	// observe is contributed by the background control of the same name, and secret is a
	// function rather than data; validation learns both elsewhere.
	contributedElsewhere := map[string]struct{}{"observe": {}, "secret": {}}
	environment := step.Request{}.ExpressionEnvironment(nil)
	for _, name := range slices.Sorted(maps.Keys(environment)) {
		_, known := baseExpressionRoots[name]
		_, elsewhere := contributedElsewhere[name]
		if !known && !elsewhere {
			t.Errorf("expression root %q is missing from baseExpressionRoots", name)
		}
	}
}
