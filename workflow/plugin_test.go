package workflow

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPluginSourceIncludesStartParameters(t *testing.T) {
	var source PluginSource
	err := yaml.Unmarshal([]byte("source: github:acme/plugin@v1\nsha256: "+strings.Repeat("a", 64)+"\nwith:\n  token_name: demo\n"), &source)
	if err != nil {
		t.Fatal(err)
	}
	if source.With["token_name"] != "demo" {
		t.Fatalf("with = %#v", source.With)
	}
}

func TestPluginSourceRequiresDigest(t *testing.T) {
	var source PluginSource
	if err := yaml.Unmarshal([]byte("source: https://example.com/plugin.json\n"), &source); err == nil {
		t.Fatal("expected digest error")
	}
}
