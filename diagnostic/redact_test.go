package diagnostic

import (
	"strings"
	"testing"
)

func TestRedactedJSONRemovesSensitiveAndEnvironmentValues(t *testing.T) {
	type environment map[string]string
	value := map[string]any{
		"command": "deploy", "args": []any{"--target", "prod"}, "api_key": "secret-api-key",
		"env":    environment{"TOKEN": "secret-token", "VISIBLE": "also-secret"},
		"nested": map[string]any{"authorization": "Bearer secret", "url": "https://example.test/build?token=secret#part", "script": "curl 'https://example.test/run?token=embedded'"},
	}
	got := RedactedJSON(value)
	for _, secret := range []string{"secret-api-key", "secret-token", "also-secret", "Bearer secret", "?token=secret", "?token=embedded"} {
		if strings.Contains(got, secret) {
			t.Fatalf("RedactedJSON() exposed %q in %s", secret, got)
		}
	}
	for _, want := range []string{`"command":"deploy"`, `"args":["--target","prod"]`, `"api_key":"<redacted>"`, `"VISIBLE":"<redacted>"`, `"url":"https://example.test/build#part"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("RedactedJSON() = %s, want %s", got, want)
		}
	}
}

func TestRedactedJSONTruncatesAtUTF8Boundary(t *testing.T) {
	got := RedactedJSON(map[string]any{"source": strings.Repeat("ą", ConfigLimit)})
	if len(got) > ConfigLimit || !strings.HasSuffix(got, truncatedMark) {
		t.Fatalf("RedactedJSON() length = %d, value suffix = %q", len(got), got[max(0, len(got)-20):])
	}
}
