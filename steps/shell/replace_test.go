package shell

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/process/processtest"
	"github.com/up2jj/wuko/step"
)

func TestNewValidatesReplaceConfiguration(t *testing.T) {
	t.Parallel()
	argv := map[string]any{"expr": "steps.connection.argv"}
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "templated", raw: map[string]any{"argv": argv, "tty": true, "replace": "{{ vars.replace }}"}, want: "cannot unmarshal"},
		{name: "without tty", raw: map[string]any{"argv": argv, "replace": true}, want: "replace requires tty"},
		{name: "script", raw: map[string]any{"script": "psql", "tty": true, "replace": true}, want: "replace cannot be combined with script"},
		{name: "shell", raw: map[string]any{"command": "psql", "shell": "/bin/bash", "tty": true, "replace": true}, want: "replace cannot be combined with shell"},
		{name: "sdk", raw: map[string]any{"command": "node", "sdk": "node", "tty": true, "replace": true}, want: "replace cannot be combined with sdk"},
		{name: "interactions", raw: map[string]any{"argv": argv, "tty": true, "replace": true, "interactions": []any{map[string]any{"expect": "a", "send": "b"}}}, want: "replace cannot be combined with interactions"},
		{name: "terminal", raw: map[string]any{"argv": argv, "tty": true, "replace": true, "terminal": map[string]any{"title": "db"}}, want: "replace cannot be combined with terminal"},
		{name: "exit codes", raw: map[string]any{"argv": argv, "tty": true, "replace": true, "allowed_exit_codes": []any{1}}, want: "replace cannot be combined with allowed_exit_codes"},
		{name: "user", raw: map[string]any{"argv": argv, "tty": true, "replace": true, "user": "postgres"}, want: "replace cannot be combined with user"},
		{name: "elevated", raw: map[string]any{"argv": argv, "tty": true, "replace": true, "elevated": true}, want: "replace cannot be combined with elevated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want %q", err, test.want)
			}
		})
	}

	for _, raw := range []map[string]any{
		{"argv": argv, "tty": true, "replace": true},
		{"command": "psql", "args": []any{"-h", "localhost"}, "tty": true, "replace": true},
		{"script": "echo hello", "replace": false},
	} {
		if _, err := New(raw); err != nil {
			t.Fatalf("New(%#v) error = %v", raw, err)
		}
	}
}

func TestShellReplaceRequestsResolvedReplacement(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	bin := filepath.Join(runDir, "bin")
	testfs.MkdirAll(t, bin, 0o755)
	testfs.WriteFile(t, filepath.Join(bin, "client"), []byte("#!/bin/sh\n"), 0o755)
	testfs.MkdirAll(t, filepath.Join(runDir, "work"), 0o755)
	runner := steptest.Build(t, New, map[string]any{
		"argv": map[string]any{"expr": "steps.connection.argv"}, "tty": true, "replace": true,
		"working_directory": "work", "env": map[string]any{"MODE": "replace"},
	})
	var got []process.Replacement
	executor := &processtest.Executor{}
	_, err := runner.Run(t.Context(), step.Request{
		RunDir: runDir, Env: map[string]string{"PATH": bin}, Interactive: true, Stdin: strings.NewReader(""),
		Steps:    map[string]any{"connection": map[string]any{"argv": []any{"client", "--db", "dev; rm -rf ignored"}}},
		Executor: executor,
		ReplaceProcess: func(replacement process.Replacement) error {
			got = append(got, replacement)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []process.Replacement{{
		Path: filepath.Join(bin, "client"), Argv: []string{"client", "--db", "dev; rm -rf ignored"},
		Env: []string{"MODE=replace", "PATH=" + bin, "WUKO_STEP_ATTEMPT=1", "WUKO_STEP_MAX_ATTEMPTS=1", "WUKO_STEP_OPERATION_ID="},
		Dir: filepath.Join(runDir, "work"),
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("replacement mismatch (-want +got):\n%s", diff)
	}
	if calls := executor.Calls(); len(calls) != 0 {
		t.Fatalf("executor ran %d command(s), want none", len(calls))
	}
}

func TestShellReplaceFailsBeforeRequestingReplacement(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	testfs.WriteFile(t, filepath.Join(runDir, "plain"), []byte("data"), 0o644)
	requested := func(process.Replacement) error {
		t.Error("ReplaceProcess called")
		return nil
	}
	tests := []struct {
		name    string
		raw     map[string]any
		request step.Request
		want    string
	}{
		{
			name:    "unavailable",
			raw:     map[string]any{"command": "sh", "tty": true, "replace": true},
			request: step.Request{RunDir: runDir, Interactive: true, Stdin: strings.NewReader("")},
			want:    "replace is available only to the final step",
		},
		{
			name:    "inside executor",
			raw:     map[string]any{"command": "sh", "tty": true, "replace": true},
			request: step.Request{RunDir: runDir, Interactive: true, Stdin: strings.NewReader(""), InsideExecutor: true, ReplaceProcess: requested},
			want:    "replace is not supported inside executor blocks",
		},
		{
			name:    "non-interactive",
			raw:     map[string]any{"command": "sh", "tty": true, "replace": true},
			request: step.Request{RunDir: runDir, ReplaceProcess: requested},
			want:    "tty requires an interactive terminal",
		},
		{
			name:    "missing command",
			raw:     map[string]any{"command": "wuko-missing-client", "tty": true, "replace": true},
			request: step.Request{RunDir: runDir, Env: map[string]string{"PATH": runDir}, Interactive: true, Stdin: strings.NewReader(""), ReplaceProcess: requested},
			want:    `resolving replacement command "wuko-missing-client"`,
		},
		{
			name:    "not executable",
			raw:     map[string]any{"command": "./plain", "tty": true, "replace": true},
			request: step.Request{RunDir: runDir, Interactive: true, Stdin: strings.NewReader(""), ReplaceProcess: requested},
			want:    "is not executable",
		},
		{
			name:    "missing working directory",
			raw:     map[string]any{"command": "sh", "tty": true, "replace": true, "working_directory": "absent"},
			request: step.Request{RunDir: runDir, Interactive: true, Stdin: strings.NewReader(""), ReplaceProcess: requested},
			want:    "replacement working directory",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := steptest.Build(t, New, test.raw)
			_, err := runner.Run(t.Context(), test.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestShellReplaceDeclaresNoOutputs(t *testing.T) {
	t.Parallel()
	registry := step.NewRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}
	schema, err := registry.OutputSchemaForConfig(t.Context(), "shell", map[string]any{"command": "psql", "tty": true, "replace": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Fields) != 0 {
		t.Fatalf("replace outputs = %#v, want none", schema.Fields)
	}
}
