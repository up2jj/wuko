package step

import (
	"math"
	"testing"
)

func TestDecodeConfigKeepsWholeFloatsFloating(t *testing.T) {
	// yaml.v3 writes a whole float64 as "45", which decodes back as an int. A
	// configured 45.0 then reached the step as an integer, and rewriting a TOML
	// float with it silently changed the value's type.
	var config struct {
		Value  any            `yaml:"value"`
		Nested map[string]any `yaml:"nested"`
		List   []any          `yaml:"list"`
	}
	raw := map[string]any{
		"value":  float64(45),
		"nested": map[string]any{"ratio": float64(2)},
		"list":   []any{float64(1), float64(1.5)},
	}
	if err := DecodeConfig(raw, &config); err != nil {
		t.Fatalf("DecodeConfig() error = %v", err)
	}
	if value, ok := config.Value.(float64); !ok || value != 45 {
		t.Fatalf("value = %#v, want float64(45)", config.Value)
	}
	if value, ok := config.Nested["ratio"].(float64); !ok || value != 2 {
		t.Fatalf("nested ratio = %#v, want float64(2)", config.Nested["ratio"])
	}
	if value, ok := config.List[0].(float64); !ok || value != 1 {
		t.Fatalf("list[0] = %#v, want float64(1)", config.List[0])
	}
}

func TestDecodeConfigKeepsIntegersIntegral(t *testing.T) {
	var config struct {
		Whole any `yaml:"whole"`
		Count int `yaml:"count"`
	}
	if err := DecodeConfig(map[string]any{"whole": 45, "count": 3}, &config); err != nil {
		t.Fatalf("DecodeConfig() error = %v", err)
	}
	if value, ok := config.Whole.(int); !ok || value != 45 {
		t.Fatalf("whole = %#v, want int(45)", config.Whole)
	}
	if config.Count != 3 {
		t.Fatalf("count = %d, want 3", config.Count)
	}
}

func TestDecodeConfigEncodesNonFiniteFloats(t *testing.T) {
	var config struct {
		Nan      any `yaml:"nan"`
		Positive any `yaml:"positive"`
		Negative any `yaml:"negative"`
	}
	raw := map[string]any{
		"nan":      math.NaN(),
		"positive": math.Inf(1),
		"negative": math.Inf(-1),
	}
	if err := DecodeConfig(raw, &config); err != nil {
		t.Fatalf("DecodeConfig() error = %v", err)
	}
	value, ok := config.Nan.(float64)
	if !ok || !math.IsNaN(value) {
		t.Fatalf("nan = %#v, want NaN", config.Nan)
	}
	if value, ok := config.Positive.(float64); !ok || !math.IsInf(value, 1) {
		t.Fatalf("positive = %#v, want +Inf", config.Positive)
	}
	if value, ok := config.Negative.(float64); !ok || !math.IsInf(value, -1) {
		t.Fatalf("negative = %#v, want -Inf", config.Negative)
	}
}
