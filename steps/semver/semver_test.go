package semver

import (
	"context"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestParseReturnsCanonicalVersionPartsAndVariable(t *testing.T) {
	runner, err := New(map[string]any{
		"operation": "parse", "version": "v1.2.3-rc.1+build.7", "variable": "release",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "1.2.3-rc.1+build.7" || result.Variables["release"] != result.Outputs["value"] {
		t.Fatalf("result = %#v", result)
	}
	if result.Outputs["major"] != uint64(1) || result.Outputs["minor"] != uint64(2) || result.Outputs["patch"] != uint64(3) {
		t.Fatalf("parts = %#v", result.Outputs)
	}
	if result.Outputs["prerelease"] != "rc.1" || result.Outputs["metadata"] != "build.7" {
		t.Fatalf("parts = %#v", result.Outputs)
	}
}

func TestCompareUsesSemVerPrecedence(t *testing.T) {
	tests := []struct {
		name        string
		version     string
		other       string
		comparison  int
		less, equal bool
		greater     bool
	}{
		{"prerelease is lower", "1.0.0-rc.1", "1.0.0", -1, true, false, false},
		{"metadata is ignored", "1.0.0+one", "1.0.0+two", 0, false, true, false},
		{"higher minor is greater", "1.3.0", "1.2.9", 1, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := New(map[string]any{"operation": "compare", "version": tt.version, "other": tt.other})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), step.Request{})
			if err != nil {
				t.Fatal(err)
			}
			if result.Outputs["value"] != tt.comparison || result.Outputs["less"] != tt.less || result.Outputs["equal"] != tt.equal || result.Outputs["greater"] != tt.greater {
				t.Fatalf("outputs = %#v", result.Outputs)
			}
		})
	}
}

func TestConstrainSupportsRanges(t *testing.T) {
	for _, tt := range []struct {
		version    string
		constraint string
		matched    bool
	}{
		{"1.4.2", ">= 1.2.0, < 2.0.0", true},
		{"2.0.0", ">= 1.2.0, < 2.0.0", false},
		{"1.5.0", "~1.4 || ^1.5", true},
		{"1.5.0-beta.1", ">= 1.0.0", false},
		{"1.5.0-beta.1", ">= 1.5.0-beta.1, < 2.0.0", true},
	} {
		runner, err := New(map[string]any{"operation": "constrain", "version": tt.version, "constraint": tt.constraint})
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(t.Context(), step.Request{})
		if err != nil {
			t.Fatal(err)
		}
		if result.Outputs["value"] != tt.matched || result.Outputs["matched"] != tt.matched {
			t.Fatalf("version %q outputs = %#v", tt.version, result.Outputs)
		}
	}
}

func TestIncrementRejectsOverflow(t *testing.T) {
	for _, tt := range []struct {
		version string
		part    string
	}{
		{"18446744073709551615.0.0", "major"},
		{"1.18446744073709551615.0", "minor"},
		{"1.2.18446744073709551615", "patch"},
	} {
		runner, err := New(map[string]any{"operation": "increment", "version": tt.version, "part": tt.part})
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(t.Context(), step.Request{})
		if err == nil || !strings.Contains(err.Error(), "cannot be incremented") || len(result.Outputs) != 0 {
			t.Fatalf("%s result = %#v, error = %v", tt.part, result, err)
		}
	}
}

func TestIncrementResetsLowerPartsAndQualifiers(t *testing.T) {
	for _, tt := range []struct {
		version string
		part    string
		want    string
	}{
		{"1.2.3+build.7", "patch", "1.2.4"},
		{"1.2.3-rc.1+build.7", "patch", "1.2.3"},
		{"1.2.3-rc.1+build.7", "minor", "1.3.0"},
		{"1.2.3-rc.1+build.7", "major", "2.0.0"},
	} {
		runner, err := New(map[string]any{"operation": "increment", "version": tt.version, "part": tt.part})
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(t.Context(), step.Request{})
		if err != nil {
			t.Fatal(err)
		}
		if result.Outputs["value"] != tt.want || result.Outputs["version"] != tt.want || result.Outputs["previous"] != tt.version {
			t.Fatalf("%s increment = %#v", tt.part, result.Outputs)
		}
	}
}

func TestRejectsInvalidConfiguration(t *testing.T) {
	tests := []map[string]any{
		{},
		{"operation": "parse"},
		{"operation": "inspect", "version": "1.2.3"},
		{"operation": "parse", "version": "1.2"},
		{"operation": "parse", "version": "1.2.3", "other": "2.0.0"},
		{"operation": "compare", "version": "1.2.3"},
		{"operation": "compare", "version": "1.2.3", "other": "2"},
		{"operation": "constrain", "version": "1.2.3"},
		{"operation": "constrain", "version": "1.2.3", "constraint": ">> 2"},
		{"operation": "increment", "version": "1.2.3"},
		{"operation": "increment", "version": "1.2.3", "part": "prerelease"},
		{"operation": "parse", "version": "1.2.3", "unknown": true},
	}
	for _, raw := range tests {
		if _, err := New(raw); err == nil || strings.TrimSpace(err.Error()) == "" {
			t.Fatalf("New(%#v) error = %v", raw, err)
		}
	}
}

func TestTemplatedConfigurationIsAcceptedDuringValidation(t *testing.T) {
	if _, err := New(map[string]any{
		"operation": "{{ .vars.operation }}", "version": "{{ .vars.version }}",
		"other": "{{ .vars.other }}", "constraint": "{{ .vars.constraint }}", "part": "{{ .vars.part }}",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunHonorsCancellation(t *testing.T) {
	runner, err := New(map[string]any{"operation": "parse", "version": "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := runner.Run(ctx, step.Request{})
	if err != context.Canceled || len(result.Outputs) != 0 || len(result.Variables) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}
