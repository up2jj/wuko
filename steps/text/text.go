package text

import "github.com/up2jj/wuko/step"

// Register adds the text file steps.
func Register(registry *step.Registry) error {
	if err := registry.RegisterDefinition("text_fix", step.Registration{
		Builder:    NewFix,
		Config:     fixConfig{},
		Outputs:    step.ClosedOutputs("changed", "count", "checked"),
		References: fileListReferences,
	}); err != nil {
		return err
	}
	return registry.RegisterDefinition("text_check", step.Registration{
		Builder:    NewCheck,
		Config:     checkConfig{},
		Outputs:    step.ClosedOutputs("checked", "count", "failures"),
		References: fileListReferences,
	})
}
