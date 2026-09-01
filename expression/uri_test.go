package expression

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseURIReturnsDecodedComponents(t *testing.T) {
	t.Parallel()
	parts, err := ParseURI("https://alice:secret@example.com:8443/a%20b?q=hello+world&q=wuko&empty=#frag%20ment")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"scheme": "https", "opaque": "", "username": "alice", "password": "secret",
		"host": "example.com:8443", "path": "/a b", "fragment": "frag ment",
		"query": map[string]any{
			"q":     []any{"hello world", "wuko"},
			"empty": []any{""},
		},
	}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts = %#v, want %#v", parts, want)
	}
}

func TestParseURIPreservesOptionalUserinfoFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		value       string
		hasUsername bool
		hasPassword bool
		password    string
	}{
		{name: "none", value: "https://example.com"},
		{name: "username", value: "https://alice@example.com", hasUsername: true},
		{name: "empty password", value: "https://alice:@example.com", hasUsername: true, hasPassword: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts, err := ParseURI(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			_, hasUsername := parts["username"]
			password, hasPassword := parts["password"]
			if hasUsername != tt.hasUsername || hasPassword != tt.hasPassword || (hasPassword && password != tt.password) {
				t.Fatalf("userinfo = %#v", parts)
			}
		})
	}
}

func TestParseURISupportsRelativeAndOpaqueReferences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  map[string]any
	}{
		{
			name: "relative", value: "../releases/latest?channel=stable",
			want: map[string]any{"scheme": "", "opaque": "", "host": "", "path": "../releases/latest", "fragment": "", "query": map[string]any{"channel": []any{"stable"}}},
		},
		{
			name: "network path", value: "//cdn.example.com/assets/app.js",
			want: map[string]any{"scheme": "", "opaque": "", "host": "cdn.example.com", "path": "/assets/app.js", "fragment": "", "query": map[string]any{}},
		},
		{
			name: "opaque", value: "mailto:ops@example.com?subject=Ready",
			want: map[string]any{"scheme": "mailto", "opaque": "ops@example.com", "host": "", "path": "", "fragment": "", "query": map[string]any{"subject": []any{"Ready"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseURI(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parts = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBuildURIEncodesCanonicalQuery(t *testing.T) {
	t.Parallel()
	got, err := BuildURI(map[string]any{
		"scheme": "https", "username": "alice", "password": "secret", "host": "example.com",
		"path": "/a b", "fragment": "frag ment",
		"query": map[string]any{
			"b": "hello world", "a": []any{"one", "two"}, "empty": "", "omitted": []string{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://alice:secret@example.com/a%20b?a=one&a=two&b=hello+world&empty=#frag%20ment"
	if got != want {
		t.Fatalf("URI = %q, want %q", got, want)
	}
}

func TestBuildURISupportsRelativeNetworkAndOpaqueReferences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		parts map[string]any
		want  string
	}{
		{name: "relative", parts: map[string]any{"path": "/search", "query": map[string]string{"q": "hello world"}}, want: "/search?q=hello+world"},
		{name: "network path", parts: map[string]any{"host": "cdn.example.com", "path": "/app.js"}, want: "//cdn.example.com/app.js"},
		{name: "opaque", parts: map[string]any{"scheme": "mailto", "opaque": "ops@example.com", "query": map[string]any{"subject": "Deployment ready"}}, want: "mailto:ops@example.com?subject=Deployment+ready"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildURI(tt.parts)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("URI = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildURIRebuildsParsedCanonicalURI(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"https://alice:@example.com/a%20b?tag=v1&tag=v2#notes",
		"../releases?channel=stable",
		"mailto:ops@example.com?subject=Ready",
	} {
		parts, err := ParseURI(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := BuildURI(parts)
		if err != nil {
			t.Fatal(err)
		}
		if got != value {
			t.Fatalf("BuildURI(ParseURI(%q)) = %q", value, got)
		}
	}
}

func TestURIHelpersRejectInvalidInputs(t *testing.T) {
	t.Parallel()
	parseTests := []struct {
		value string
		want  string
	}{
		{value: "", want: "must not be blank"},
		{value: "https://example.com/%zz", want: "invalid URL escape"},
		{value: "https://example.com/?a=%zz", want: "invalid URL escape"},
		{value: "https://example.com/?a=1;b=2", want: "invalid semicolon separator"},
	}
	for _, tt := range parseTests {
		_, err := ParseURI(tt.value)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("ParseURI(%q) error = %v, want %q", tt.value, err, tt.want)
		}
	}

	buildTests := []struct {
		name  string
		parts map[string]any
		want  string
	}{
		{name: "nil", parts: nil, want: "must be an object"},
		{name: "blank", parts: map[string]any{}, want: "non-blank"},
		{name: "unknown", parts: map[string]any{"path": "/", "extra": true}, want: "unknown URI component"},
		{name: "component type", parts: map[string]any{"scheme": 1}, want: "must be a string"},
		{name: "invalid scheme", parts: map[string]any{"scheme": "1http", "path": "/"}, want: "invalid URI scheme"},
		{name: "invalid host", parts: map[string]any{"host": "user@example.com", "path": "/"}, want: "invalid URI host"},
		{name: "password without username", parts: map[string]any{"password": "secret", "host": "example.com"}, want: "requires"},
		{name: "opaque authority", parts: map[string]any{"scheme": "mailto", "opaque": "ops@example.com", "host": "example.com"}, want: "cannot be combined"},
		{name: "query type", parts: map[string]any{"path": "/", "query": "q=x"}, want: "expected map"},
		{name: "query value type", parts: map[string]any{"path": "/", "query": map[string]any{"q": 1}}, want: "string or list"},
		{name: "query item type", parts: map[string]any{"path": "/", "query": map[string]any{"q": []any{"ok", 1}}}, want: "item 1"},
	}
	for _, tt := range buildTests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildURI(tt.parts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestURIHelpersAcrossTemplateAndExpr(t *testing.T) {
	t.Parallel()
	templateURI, err := renderTemplate(`{{ dict "scheme" "https" "host" "api.example.com" "path" "/releases" "query" (dict "channel" "stable" "tag" (list "v1" "v2")) | buildURI }}`)
	if err != nil {
		t.Fatalf("template build: %v", err)
	}
	templateHost, err := renderTemplate(`{{ "https://api.example.com:8443/releases?id=42#notes" | parseURI | get "host" }}`)
	if err != nil {
		t.Fatalf("template parse: %v", err)
	}
	exprURI, err := Eval(`buildURI({"scheme": "https", "host": "api.example.com", "path": "/releases", "query": {"channel": "stable", "tag": ["v1", "v2"]}})`, map[string]any{})
	if err != nil {
		t.Fatalf("Expr build: %v", err)
	}
	exprTag, err := Eval(`parseURI("https://example.com/?tag=v1&tag=v2").query.tag[1]`, map[string]any{})
	if err != nil {
		t.Fatalf("Expr parse: %v", err)
	}
	wantURI := "https://api.example.com/releases?channel=stable&tag=v1&tag=v2"
	if templateURI != wantURI || exprURI != wantURI || templateHost != "api.example.com:8443" || exprTag != "v2" {
		t.Fatalf("template URI = %q, Expr URI = %#v, host = %q, tag = %#v", templateURI, exprURI, templateHost, exprTag)
	}
}
