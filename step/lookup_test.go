package step

import "testing"

func TestValidatePath(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"vars.items", "steps.list.value.rows", "vars.{{ .vars.name }}"} {
		if err := ValidatePath(path); err != nil {
			t.Fatalf("ValidatePath(%q) = %v", path, err)
		}
	}
	for _, path := range []string{"items", "vars.", "inputs.values", "vars..items", `filter(vars.envs, # != "prod")`, ""} {
		if err := ValidatePath(path); err == nil {
			t.Fatalf("ValidatePath(%q) accepted an invalid path", path)
		}
	}
}
