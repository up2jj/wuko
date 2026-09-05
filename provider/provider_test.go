package provider

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeProvider struct {
	name   string
	schema Schema
	value  map[string]any
	active bool
	err    error
	load   func(map[string]string)
}

func (provider fakeProvider) Name() string   { return provider.name }
func (provider fakeProvider) Schema() Schema { return provider.schema }
func (provider fakeProvider) Load(_ context.Context, environment map[string]string) (map[string]any, bool, error) {
	if provider.load != nil {
		provider.load(environment)
	}
	return provider.value, provider.active, provider.err
}

func TestRegistryRegisterValidation(t *testing.T) {
	for _, name := range []string{"", "1cloud", "cloud-name", "cloud.name"} {
		t.Run(name, func(t *testing.T) {
			var registry Registry
			if err := registry.Register(fakeProvider{name: name}); err == nil {
				t.Fatalf("Register(%q) succeeded", name)
			}
		})
	}
	var registry Registry
	if err := registry.Register(nil); err == nil {
		t.Fatal("Register(nil) succeeded")
	}
	if err := registry.Register(fakeProvider{name: "cloud"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeProvider{name: "cloud"}); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestRegistryLoadRetainsSchemasAndActiveValuesInOrder(t *testing.T) {
	order := make([]string, 0, 3)
	var registry Registry
	for _, item := range []fakeProvider{
		{name: "first", schema: Object(map[string]Schema{"value": Scalar()}), value: map[string]any{"value": int64(1)}, active: true, load: func(map[string]string) { order = append(order, "first") }},
		{name: "inactive", schema: OpenObject(), load: func(map[string]string) { order = append(order, "inactive") }},
		{name: "second", schema: Object(map[string]Schema{"nested": OpenObject()}), value: map[string]any{"nested": map[string]any{"ok": true}}, active: true, load: func(map[string]string) { order = append(order, "second") }},
	} {
		if err := registry.Register(item); err != nil {
			t.Fatal(err)
		}
	}
	set, err := registry.Load(context.Background(), map[string]string{"TOKEN": "original"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"first", "inactive", "second"}) {
		t.Fatalf("load order = %v", order)
	}
	if got := set.Names(); !reflect.DeepEqual(got, []string{"first", "inactive", "second"}) {
		t.Fatalf("schema names = %v", got)
	}
	if _, exists := set.Values["inactive"]; exists {
		t.Fatal("inactive provider has a value")
	}
	if len(set.Values) != 2 {
		t.Fatalf("active values = %#v", set.Values)
	}
}

func TestRegistryLoadIsolatesInputsAndOutputs(t *testing.T) {
	environment := map[string]string{"TOKEN": "original"}
	value := map[string]any{
		"nested": map[string]any{"value": "original"},
		"items":  []any{map[string]any{"value": "original"}},
		"typed":  map[string][]string{"regions": {"eu", "us"}},
	}
	var registry Registry
	if err := registry.Register(fakeProvider{
		name: "cloud", schema: OpenObject(), value: value, active: true,
		load: func(received map[string]string) { received["TOKEN"] = "changed" },
	}); err != nil {
		t.Fatal(err)
	}
	set, err := registry.Load(context.Background(), environment)
	if err != nil {
		t.Fatal(err)
	}
	if environment["TOKEN"] != "original" {
		t.Fatalf("environment mutated: %#v", environment)
	}
	value["nested"].(map[string]any)["value"] = "provider changed"
	clone := set.Clone()
	set.Values["cloud"]["nested"].(map[string]any)["value"] = "set changed"
	set.Values["cloud"]["items"].([]any)[0].(map[string]any)["value"] = "set changed"
	set.Values["cloud"]["typed"].(map[string][]string)["regions"][0] = "set changed"
	if got := clone.Values["cloud"]["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("cloned nested value = %v", got)
	}
	if got := clone.Values["cloud"]["items"].([]any)[0].(map[string]any)["value"]; got != "original" {
		t.Fatalf("cloned list value = %v", got)
	}
	if got := clone.Values["cloud"]["typed"].(map[string][]string)["regions"][0]; got != "eu" {
		t.Fatalf("cloned typed value = %v", got)
	}
	set.Schemas["cloud"] = Scalar()
	if !clone.Schemas["cloud"].Open {
		t.Fatal("cloned schema was mutated")
	}
}

func TestRegistryLoadFailure(t *testing.T) {
	sentinel := errors.New("unavailable")
	var registry Registry
	if err := registry.Register(fakeProvider{name: "cloud", err: sentinel}); err != nil {
		t.Fatal(err)
	}
	_, err := registry.Load(context.Background(), nil)
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), `loading provider "cloud"`) {
		t.Fatalf("Load error = %v", err)
	}
}

func TestSetValidateNames(t *testing.T) {
	tests := []struct {
		name string
		set  Set
		want string
	}{
		{name: "invalid", set: Set{Schemas: map[string]Schema{"not-valid": {}}}, want: "valid identifier"},
		{name: "collision", set: Set{Schemas: map[string]Schema{"vars": {}}}, want: "runtime root"},
		{name: "unregistered value", set: Set{Schemas: map[string]Schema{}, Values: map[string]map[string]any{"cloud": {}}}, want: "has no schema"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.set.ValidateNames(map[string]struct{}{"vars": {}}); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateNames error = %v, want %q", err, tt.want)
			}
		})
	}
}
