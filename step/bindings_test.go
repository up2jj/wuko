package step

import (
	"strings"
	"testing"
)

func TestBindingTypesRejectImplicitCoercion(t *testing.T) {
	t.Parallel()
	type child struct {
		Text string `yaml:"text"`
	}
	type config struct {
		Text   string            `yaml:"text"`
		Args   []string          `yaml:"args"`
		Labels map[string]string `yaml:"labels"`
		Child  child             `yaml:"child"`
		Flag   bool              `yaml:"flag"`
		Count  int               `yaml:"count"`
		Port   uint16            `yaml:"port"`
		Small  int8              `yaml:"small"`
	}
	for _, tt := range []struct {
		name    string
		path    []string
		value   any
		invalid bool
	}{
		{"text", []string{"text"}, "hello", false},
		{"number to text", []string{"text"}, 12, true},
		{"boolean to text", []string{"text"}, true, true},
		{"null text", []string{"text"}, nil, true},
		{"list", []string{"args"}, []any{"one", "two"}, false},
		{"list item", []string{"args", "0"}, 42, true},
		{"nested list item", []string{"args"}, []any{"one", 42}, true},
		{"object", []string{"child"}, map[string]any{"text": "hello"}, false},
		{"object number", []string{"child"}, map[string]any{"text": 42}, true},
		{"map value", []string{"labels"}, map[string]any{"name": true}, true},
		{"bool", []string{"flag"}, true, false},
		{"int", []string{"count"}, 1, false},
		{"fractional int", []string{"count"}, 1.1, true},
		{"whole float int", []string{"count"}, 3.0, false},
		{"int to uint", []string{"port"}, 8080, false},
		{"negative uint", []string{"port"}, -1, true},
		{"whole float uint", []string{"port"}, 8080.0, false},
		{"overflowing float", []string{"small"}, 300.0, true},
		{"uint to int", []string{"count"}, uint(7), false},
		{"unknown", []string{"typo"}, "x", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := CheckBindingType(config{}, tt.path, tt.value)
			if (err != nil) != tt.invalid {
				t.Fatalf("CheckBindingType()=%v, invalid=%v", err, tt.invalid)
			}
		})
	}
}

func TestDeferredConfigKeepsUnknownSeparateFromMissing(t *testing.T) {
	t.Parallel()
	raw := map[string]any{"text": DeferredValue{}, "items": []any{"known", DeferredValue{}}, "flag": false}
	if ConfigKnown(raw, "text") || ConfigKnown(raw, "items.1") || ConfigKnown(raw, "") {
		t.Fatal("deferred values reported known")
	}
	if !ConfigKnown(raw, "missing", "flag", "items.0") {
		t.Fatal("ordinary or absent value reported unknown")
	}
	var config struct {
		Text  string   `yaml:"text"`
		Items []string `yaml:"items"`
		Flag  bool     `yaml:"flag"`
	}
	if err := DecodeConfig(raw, &config); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(config.Text, "{{") || config.Items[0] != "known" || config.Flag {
		t.Fatalf("validation configuration=%#v", config)
	}
	if ConfigKnown(raw, "text") {
		t.Fatal("decoding mutated configuration")
	}
}

func TestDecodeConfigAcceptsWholeFloatForInt(t *testing.T) {
	t.Parallel()
	var config struct {
		Count int `yaml:"count"`
	}
	if err := DecodeConfig(map[string]any{"count": 3.0}, &config); err != nil {
		t.Fatal(err)
	}
	if config.Count != 3 {
		t.Fatalf("count=%d, want 3", config.Count)
	}
}
