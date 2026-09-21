package process

import (
	"errors"
	"testing"
)

func TestExitCodePolicyAllows(t *testing.T) {
	tests := []struct {
		name       string
		codes      []int
		configured bool
		allowAny   bool
		allowed    []int
		rejected   []int
	}{
		{name: "unconfigured", allowed: []int{0}, rejected: []int{-1, 1, 7, 255}},
		{name: "list", codes: []int{0, 7}, configured: true, allowed: []int{0, 7}, rejected: []int{-1, 1, 8, 255}},
		{
			name: "any", configured: true, allowAny: true,
			// -1 is what os/exec reports for a signal-terminated child, and no list can name it,
			// so any must not accept it either.
			allowed: []int{0, 1, 7, 254, 255}, rejected: []int{-1, -2, 256},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, err := NewExitCodePolicy(test.codes, test.configured, test.allowAny)
			if err != nil {
				t.Fatal(err)
			}
			for _, code := range test.allowed {
				if !policy.Allows(code) {
					t.Errorf("Allows(%d) = false, want true", code)
				}
			}
			for _, code := range test.rejected {
				if policy.Allows(code) {
					t.Errorf("Allows(%d) = true, want false", code)
				}
			}
		})
	}
}

func TestNewExitCodePolicyRejectsInvalidLists(t *testing.T) {
	if _, err := NewExitCodePolicy(nil, true, false); !errors.Is(err, ErrAllowedExitCodes) {
		t.Fatalf("empty list error = %v, want %v", err, ErrAllowedExitCodes)
	}
	for _, code := range []int{-1, 256} {
		if _, err := NewExitCodePolicy([]int{code}, true, false); err == nil {
			t.Fatalf("NewExitCodePolicy(%d) error = nil, want an out-of-range failure", code)
		}
	}
}

func TestNormalizeAllowedExitCodes(t *testing.T) {
	raw := map[string]any{"command": "probe", "allowed_exit_codes": "any"}
	decoded, allowAny, err := NormalizeAllowedExitCodes(raw)
	if err != nil || !allowAny {
		t.Fatalf("NormalizeAllowedExitCodes() = %t, %v", allowAny, err)
	}
	if _, present := decoded["allowed_exit_codes"]; present {
		t.Fatal("any was left in the decoded configuration")
	}
	// The caller's map is shared with the engine, so normalizing must not mutate it.
	if _, present := raw["allowed_exit_codes"]; !present {
		t.Fatal("NormalizeAllowedExitCodes mutated the caller's configuration")
	}
	if _, _, err := NormalizeAllowedExitCodes(map[string]any{"allowed_exit_codes": "ANY"}); !errors.Is(err, ErrAllowedExitCodes) {
		t.Fatalf("unsupported string error = %v, want %v", err, ErrAllowedExitCodes)
	}
}
