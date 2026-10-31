package cmd

import (
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
	"testing"
)

func TestBuiltinsValidateDeferredRuntimeValues(t *testing.T) {
	t.Parallel()
	binding := func() map[string]any { return map[string]any{"expr": "vars.value"} }
	tests := []struct {
		name, kind string
		with       map[string]any
		value      any
	}{
		{"shell command", "shell", map[string]any{"command": binding()}, "echo"},
		{"shell script", "shell", map[string]any{"script": binding()}, "true"},
		{"shell any exit code", "shell", map[string]any{"command": "echo", "allowed_exit_codes": binding()}, "any"},
		{"tcp timeout", "tcp_probe", map[string]any{"hosts": []any{"localhost"}, "ports": []any{"80"}, "timeout": binding()}, "1s"},
		{"process shutdown signal", "process", map[string]any{"command": "echo", "shutdown": map[string]any{"signal": binding()}}, "SIGTERM"},
		{"shell exit codes", "shell", map[string]any{"command": "echo", "allowed_exit_codes": binding()}, []any{0, 1}},
		{"file contents", "file", map[string]any{"operation": "write", "path": "out", "content": binding()}, "body"},
		{"file mode", "file", map[string]any{"operation": "mkdir", "path": "out", "mode": binding()}, "0755"},
		{"file patterns", "file", map[string]any{"operation": "find", "path": ".", "patterns": binding()}, []any{"**"}},
		{"file paths", "file", map[string]any{"operation": "append", "paths": binding(), "content": "text"}, []any{"out"}},
		{"http url", "http", map[string]any{"url": binding()}, "https://example.test"},
		{"http headers", "http", map[string]any{"url": "https://example.test", "headers": binding()}, map[string]any{"Accept": "application/json"}},
		{"http files", "http", map[string]any{"url": "https://example.test", "files": binding()}, []any{map[string]any{"field": "upload", "path": "file.txt"}}},
		{"http status", "http", map[string]any{"url": "https://example.test", "success_statuses": []any{binding()}}, 200},
		{"cache paths", "cache", map[string]any{"operation": "save", "cache_dir": ".cache", "key_files": []any{"go.mod"}, "paths": binding()}, []any{"out"}},
		{"watch patterns", "watch", map[string]any{"patterns": binding()}, []any{"**"}},
		{"watch events", "watch", map[string]any{"patterns": []any{"**"}, "events": binding()}, []any{"modify"}},
		{"tcp hosts", "tcp_probe", map[string]any{"hosts": binding(), "ports": []any{"80"}}, []any{"localhost"}},
		{"tcp ports", "tcp_probe", map[string]any{"hosts": []any{"localhost"}, "ports": binding()}, []any{"80"}},
		{"changed files", "changed", map[string]any{"files": binding()}, []any{"**"}},
		{"template destination", "template", map[string]any{"source": "hello", "destination": binding()}, "out.txt"},
		{"decode path", "decode", map[string]any{"path": binding(), "format": "json"}, "out.json"},
		{"log path", "log_wait", map[string]any{"path": binding(), "pattern": "ready"}, "out.log"},
		{"temp pattern", "temp", map[string]any{"kind": "file", "pattern": binding()}, "sample-*"},
		{"multiple choices", "tui_choice", map[string]any{"variable": "selected", "message": "Choose", "from": []any{"a"}, "select_all": true, "multiple": binding()}, true},
	}
	registry := step.NewRegistry()
	if err := registerBuiltinSteps(registry); err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			definition := steptest.Definition(t, "bindings", workflow.Step{ID: "subject", Type: tt.kind, With: tt.with})
			definition.Vars = map[string]any{"value": tt.value}
			if err := engine.New(registry).Validate(t.Context(), definition, engine.Options{RunDir: t.TempDir(), LocalValueDir: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
