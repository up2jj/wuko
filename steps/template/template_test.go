package template

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type boundRenderer struct {
	renderer *workflow.Renderer
	data     map[string]any
}

func (r *boundRenderer) Validate(value string) error { return r.renderer.Validate(value) }
func (r *boundRenderer) Render(value string) (string, error) {
	return r.renderer.Render(value, r.data)
}
func (r *boundRenderer) ValidateContent(value string) error {
	return r.renderer.ValidateUncached(value)
}
func (r *boundRenderer) RenderContent(value string) (string, error) {
	return r.renderer.RenderUncached(value, r.data)
}
func (r *boundRenderer) RenderWith(value string, extra map[string]any) (string, error) {
	return r.renderer.Render(value, overlay(r.data, extra))
}
func (r *boundRenderer) RenderContentWith(value string, extra map[string]any) (string, error) {
	return r.renderer.RenderUncached(value, overlay(r.data, extra))
}
func (r *boundRenderer) Snapshot() step.DataTemplateRenderer { return r }
func (r *boundRenderer) WithoutSecrets() step.DataTemplateRenderer {
	return r
}

func overlay(base, extra map[string]any) map[string]any {
	result := maps.Clone(base)
	maps.Copy(result, extra)
	return result
}

func newRenderer(t *testing.T, definitions map[string]workflow.TemplateDefinition, data map[string]any) *boundRenderer {
	t.Helper()
	renderer, err := workflow.NewRenderer(definitions)
	if err != nil {
		t.Fatal(err)
	}
	return &boundRenderer{renderer: renderer, data: data}
}

func TestNewValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "inline", raw: map[string]any{"source": "hello"}},
		{name: "file", raw: map[string]any{"file": "templates/hello.tmpl"}},
		{name: "destination", raw: map[string]any{"source": "hello", "destination": "hello.txt", "mode": "0640"}},
		{name: "missing source", raw: map[string]any{}, want: "exactly one"},
		{name: "both sources", raw: map[string]any{"source": "hello", "file": "hello.tmpl"}, want: "exactly one"},
		{name: "blank source", raw: map[string]any{"source": " \n"}, want: "source must not be empty"},
		{name: "blank file", raw: map[string]any{"file": ""}, want: "file must not be empty"},
		{name: "absolute file", raw: map[string]any{"file": "/tmp/hello.tmpl"}, want: "must be relative"},
		{name: "escaping file", raw: map[string]any{"file": "../hello.tmpl"}, want: "must not escape"},
		{name: "blank destination", raw: map[string]any{"source": "hello", "destination": ""}, want: "destination must not be empty"},
		{name: "file option without destination", raw: map[string]any{"source": "hello", "overwrite": true}, want: "require destination"},
		{name: "unquoted mode", raw: map[string]any{"source": "hello", "destination": "hello", "mode": 640}, want: "quoted octal"},
		{name: "invalid mode", raw: map[string]any{"source": "hello", "destination": "hello", "mode": "0999"}, want: "quoted octal"},
		{name: "blank expression", raw: map[string]any{"source": "hello", "data": map[string]any{"name": map[string]any{"expr": " "}}}, want: "non-empty string"},
		{name: "invalid expression", raw: map[string]any{"source": "hello", "data": map[string]any{"name": map[string]any{"expr": "("}}}, want: "compiling data"},
		{name: "unknown field", raw: map[string]any{"source": "hello", "output": "hello"}, want: "field output not found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := New(test.raw)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := runner.(step.ExecutorAware); !ok {
					t.Fatal("template runner is not executor aware")
				}
				_, needsFS := runner.(step.ExecutorFileSystem)
				if needsFS != (test.raw["destination"] != nil) {
					t.Fatalf("ExecutorFileSystem = %v", needsFS)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunRendersInlineTemplateToMemory(t *testing.T) {
	runner, err := New(map[string]any{
		"source": `{{ template "header" . }}{{ .data.name }}:{{ .data.replicas }}:{{ index .data.payload "expr" }}`,
		"data": map[string]any{
			"name":     "billing",
			"replicas": map[string]any{"expr": "vars.replicas"},
			"payload":  map[string]any{"literal": map[string]any{"expr": "literal"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := step.Request{
		Vars: map[string]any{"replicas": 3},
		TemplateRenderer: newRenderer(t, map[string]workflow.TemplateDefinition{
			"header": {Inline: `{{ .vars.prefix }}:`},
		}, map[string]any{"vars": map[string]any{"prefix": "service"}}),
	}
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Outputs["content"], "service:billing:3:literal"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if result.Outputs["size"] != int64(len("service:billing:3:literal")) || len(result.Outputs) != 2 {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestValidateAndRunFileTemplate(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "templates", "message.tmpl"), []byte(`hello {{ .data.name }}`), 0o644); err != nil {
		t.Fatal(err)
	}
	runner, err := New(map[string]any{"file": "templates/message.tmpl", "data": map[string]any{"name": "world"}})
	if err != nil {
		t.Fatal(err)
	}
	request := step.Request{WorkflowDir: root, TemplateRenderer: newRenderer(t, nil, map[string]any{})}
	if validator, ok := runner.(step.Validator); !ok {
		t.Fatal("template runner does not validate external content")
	} else if err := validator.Validate(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["content"] != "hello world" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestFileTemplateSourceSafety(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.tmpl")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "link.tmpl")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatal(err)
	}
	linkedDirectory := filepath.Join(root, "linked")
	if err := os.Symlink(filepath.Dir(outside), linkedDirectory); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(root, "large.tmpl")
	if err := os.WriteFile(large, bytes.Repeat([]byte("x"), maxTemplateBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "binary.tmpl")
	if err := os.WriteFile(binary, []byte{'a', 0xff, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	renderer := newRenderer(t, nil, map[string]any{})
	tests := []struct {
		name     string
		file     string
		borrowed bool
		want     string
	}{
		{name: "symlink", file: "link.tmpl", want: "symbolic link"},
		{name: "symlinked parent", file: "linked/outside.tmpl", want: "symbolic link"},
		{name: "oversized", file: "large.tmpl", want: "exceeds"},
		{name: "not utf-8", file: "binary.tmpl", want: "not valid UTF-8"},
		{name: "borrowed directory", file: "large.tmpl", borrowed: true, want: "requires a packaged action"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := New(map[string]any{"file": test.file})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{
				WorkflowDir: root, WorkflowDirBorrowed: test.borrowed, TemplateRenderer: renderer,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunWritesRenderedTemplateWithFileSemantics(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "config.txt")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, err := New(map[string]any{
		"source": "hello {{ .data.name }}", "data": map[string]any{"name": "world"},
		"destination": "config.txt", "overwrite": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{RunDir: root, TemplateRenderer: newRenderer(t, nil, map[string]any{})})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello world" || info.Mode().Perm() != 0o600 {
		t.Fatalf("content = %q, mode = %04o", content, info.Mode().Perm())
	}
	if _, exists := result.Outputs["content"]; exists || result.Outputs["created"] != false || result.Outputs["mode"] != "0600" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestFileModePreservesWriteFailures(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		destination string
		overwrite   bool
		want        string
	}{
		{name: "overwrite refusal", destination: "existing.txt", want: "set overwrite to true"},
		{name: "missing parent", destination: "missing/rendered.txt", want: "creating temporary file"},
		{name: "directory destination", destination: "directory", overwrite: true, want: "is a directory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := New(map[string]any{"source": "new", "destination": test.destination, "overwrite": test.overwrite})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{
				RunDir: root, TemplateRenderer: newRenderer(t, nil, map[string]any{}),
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want %q", err, test.want)
			}
		})
	}
	content, err := os.ReadFile(filepath.Join(root, "existing.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "old" {
		t.Fatalf("refused overwrite changed content to %q", content)
	}
}

func TestRunRejectsInvalidOrMissingTemplateValues(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "invalid syntax", source: "{{", want: "unclosed action"},
		{name: "missing data", source: "{{ .data.missing }}", want: `map has no entry for key "missing"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := New(map[string]any{"source": test.source})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{TemplateRenderer: newRenderer(t, nil, map[string]any{})})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want %q", err, test.want)
			}
		})
	}
}

type targetFileSystem struct {
	files map[string][]byte
	modes map[string]fs.FileMode
}

type commandOnlyExecutor struct{}

func (commandOnlyExecutor) Run(context.Context, process.Options) (process.Result, error) {
	return process.Result{}, nil
}

func (t *targetFileSystem) Run(context.Context, process.Options) (process.Result, error) {
	return process.Result{}, nil
}
func (t *targetFileSystem) Stat(_ context.Context, path string) (executor.FileInfo, error) {
	data, ok := t.files[path]
	if !ok {
		return executor.FileInfo{}, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
	}
	return executor.FileInfo{Size: int64(len(data)), Mode: t.modes[path], ModTime: time.Unix(0, 0)}, nil
}
func (t *targetFileSystem) ReadFile(_ context.Context, path string, _ int64) ([]byte, error) {
	data, ok := t.files[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return data, nil
}
func (t *targetFileSystem) WriteFile(_ context.Context, path string, data []byte, options executor.WriteOptions) error {
	if _, exists := t.files[path]; exists && !options.Replace {
		return fs.ErrExist
	}
	t.files[path] = bytes.Clone(data)
	t.modes[path] = options.Mode
	return nil
}
func (*targetFileSystem) MkdirAll(context.Context, string, fs.FileMode) error { return nil }

func TestRunWritesThroughExecutorFileSystem(t *testing.T) {
	root := t.TempDir()
	target := &targetFileSystem{files: map[string][]byte{}, modes: map[string]fs.FileMode{}}
	runner, err := New(map[string]any{"source": "target", "destination": "rendered.txt", "mode": "0640"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		RunDir: root, Executor: target, TemplateRenderer: newRenderer(t, nil, map[string]any{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "rendered.txt")
	if string(target.files[path]) != "target" || target.modes[path] != 0o640 {
		t.Fatalf("files = %#v, modes = %#v", target.files, target.modes)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host destination exists: %v", err)
	}
	if result.Outputs["path"] != path || result.Outputs["created"] != true {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestOnlyFileModeRequiresExecutorFileSystem(t *testing.T) {
	renderer := newRenderer(t, nil, map[string]any{})
	memory, err := New(map[string]any{"source": "memory"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Run(t.Context(), step.Request{Executor: commandOnlyExecutor{}, TemplateRenderer: renderer}); err != nil {
		t.Fatalf("memory mode failed with command-only executor: %v", err)
	}
	file, err := New(map[string]any{"source": "file", "destination": "rendered.txt"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.Run(t.Context(), step.Request{
		RunDir: t.TempDir(), Executor: commandOnlyExecutor{}, TemplateRenderer: renderer,
	})
	if err == nil || !strings.Contains(err.Error(), "does not expose a filesystem") {
		t.Fatalf("file mode error = %v", err)
	}
}

func TestRunHonorsCancellation(t *testing.T) {
	runner, err := New(map[string]any{"source": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = runner.Run(ctx, step.Request{TemplateRenderer: newRenderer(t, nil, map[string]any{})})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}
