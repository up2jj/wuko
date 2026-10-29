package text

import "github.com/up2jj/wuko/step"

// Register adds the text file steps.
func Register(registry *step.Registry) error {
	return registry.RegisterDefinition("text_fix", step.Registration{
		Builder:    NewFix,
		Outputs:    step.ClosedOutputs("changed", "count", "checked"),
		References: fileListReferences,
	})
}
