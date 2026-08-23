package workflow

import "fmt"

func validateDependencyRuntimeOnly(definition *Definition) error {
	for name, value := range definition.Env {
		if dependencyReference(value) {
			return fmt.Errorf("workflow environment %q cannot reference dependencies; dependency outputs are available only at runtime", name)
		}
	}
	if err := validateActionSourcesWithoutDependencies(definition.Steps); err != nil {
		return err
	}
	return validateActionSourcesWithoutDependencies(definition.Finally)
}

func validateActionSourcesWithoutDependencies(steps []Step) error {
	for _, workflowStep := range steps {
		if dependencyReference(workflowStep.Uses.URL) || dependencyReference(workflowStep.Uses.Path) || dependencyReference(workflowStep.Uses.Command) {
			return fmt.Errorf("step %q uses cannot reference dependencies; action sources are resolved while loading", workflowStep.ID)
		}
		for _, arg := range workflowStep.Uses.Args {
			if dependencyReference(arg) {
				return fmt.Errorf("step %q uses argument cannot reference dependencies; action sources are resolved while loading", workflowStep.ID)
			}
		}
		for _, child := range workflowStep.ChildSequences() {
			if err := validateActionSourcesWithoutDependencies(child.Steps); err != nil {
				return err
			}
		}
	}
	return nil
}

func dependencyReference(value string) bool {
	return len(templateDependencyReferences(value)) > 0
}
