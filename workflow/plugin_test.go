package workflow

import (
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPluginSourceIncludesStartParameters(t *testing.T) {
	var source PluginSource
	err := yaml.Unmarshal([]byte("source: github:acme/plugin@v1\nwith:\n  token_name: demo\n"), &source)
	if err != nil {
		t.Fatal(err)
	}
	if source.With["token_name"] != "demo" {
		t.Fatalf("with = %#v", source.With)
	}
}

func TestPluginSourceRejectsInlineDigest(t *testing.T) {
	var source PluginSource
	if err := yaml.Unmarshal([]byte("source: https://example.com/plugin.json\nsha256: "+strings.Repeat("a", 64)+"\n"), &source); err == nil {
		t.Fatal("expected migration error")
	}
}

func TestPluginSourceCanonicalSourceLocalPaths(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		windows bool
		wantErr bool
	}{
		{name: "relative bundle", source: "plugins/acme"},
		{name: "scheme-like source", source: "ftp:acme", wantErr: true},
		{name: "windows absolute bundle", source: `C:\plugins\acme`, windows: true},
		{name: "windows colon after volume", source: `C:\plugins:acme`, windows: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.windows && runtime.GOOS != "windows" {
				t.Skip("volume names are only recognized on Windows")
			}
			_, err := (PluginSource{Source: test.source}).CanonicalSource()
			if (err != nil) != test.wantErr {
				t.Fatalf("CanonicalSource(%q) error = %v, wantErr %v", test.source, err, test.wantErr)
			}
		})
	}
}
