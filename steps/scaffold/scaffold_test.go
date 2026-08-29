package scaffold

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type boundRenderer struct {
	renderer *workflow.Renderer
	data     map[string]any
}

func (renderer boundRenderer) Validate(value string) error { return renderer.renderer.Validate(value) }
func (renderer boundRenderer) Render(value string) (string, error) {
	return renderer.renderer.Render(value, renderer.data)
}

func (renderer boundRenderer) ValidateContent(value string) error {
	return renderer.renderer.ValidateUncached(value)
}

func (renderer boundRenderer) RenderContent(value string) (string, error) {
	return renderer.renderer.RenderUncached(value, renderer.data)
}

func TestNewValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "valid", raw: map[string]any{"from": "templates/service", "into": "services/api"}},
		{name: "templated", raw: map[string]any{"from": "templates/{{ .vars.kind }}", "into": "{{ .vars.into }}", "on_conflict": "{{ .vars.policy }}"}},
		{name: "missing from", raw: map[string]any{"into": "target"}, want: "from is required"},
		{name: "missing into", raw: map[string]any{"from": "source"}, want: "into is required"},
		{name: "absolute from", raw: map[string]any{"from": "/source", "into": "target"}, want: "relative path"},
		{name: "escaping from", raw: map[string]any{"from": "../source", "into": "target"}, want: "must not escape"},
		{name: "invalid conflict", raw: map[string]any{"from": "source", "into": "target", "on_conflict": "replace"}, want: "fail, skip, or overwrite"},
		{name: "escaping from with templated conflict", raw: map[string]any{"from": "../secrets", "into": "target", "on_conflict": "{{ .vars.policy }}"}, want: "must not escape"},
		{name: "unknown field", raw: map[string]any{"from": "source", "into": "target", "mode": "0755"}, want: "field mode not found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.raw)
			if test.want == "" && err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("New() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunRendersTemplateTree(t *testing.T) {
	workflowDir := t.TempDir()
	source := filepath.Join(workflowDir, "templates", "service")
	mustMkdir(t, filepath.Join(source, "{{ .vars.kind }}", "empty"), 0o750)
	mustWrite(t, filepath.Join(source, "{{ .vars.kind }}", "{{ .vars.name }}.go"), `{{ template "header" . }}package {{ .vars.name }}`+"\n", 0o755)
	mustWrite(t, filepath.Join(source, ".gitignore"), "{{ .vars.name }}.log\n", 0o640)

	renderer := newRenderer(t, map[string]workflow.TemplateDefinition{
		"header": {Inline: "// generated for {{ .vars.name }}\n"},
	}, map[string]any{"vars": map[string]any{"kind": "cmd", "name": "billing"}})
	runner := newRunner(t, map[string]any{"from": "templates/service", "into": "services/billing"})
	runDir := t.TempDir()
	result, err := runner.Run(t.Context(), step.Request{WorkflowDir: workflowDir, RunDir: runDir, TemplateRenderer: renderer})
	if err != nil {
		t.Fatal(err)
	}
	wantFile := filepath.Join(runDir, "services", "billing", "cmd", "billing.go")
	data, err := os.ReadFile(wantFile)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "// generated for billing\npackage billing\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	info, err := os.Stat(wantFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %04o, want 0755", info.Mode().Perm())
	}
	emptyInfo, err := os.Stat(filepath.Join(runDir, "services", "billing", "cmd", "empty"))
	if err != nil {
		t.Fatalf("empty directory: %v", err)
	}
	if emptyInfo.Mode().Perm() != 0o750 {
		t.Fatalf("empty directory mode = %04o, want 0750", emptyInfo.Mode().Perm())
	}
	files := result.Outputs["files"].([]any)
	if len(files) != 2 || !slices.IsSortedFunc(files, func(left, right any) int {
		return strings.Compare(left.(string), right.(string))
	}) {
		t.Fatalf("files = %#v", files)
	}
	if result.Outputs["created"] != 2 || result.Outputs["skipped"] != 0 || result.Outputs["overwritten"] != 0 {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestRunConflictPolicies(t *testing.T) {
	for _, policy := range []string{conflictFail, conflictSkip, conflictOverwrite} {
		t.Run(policy, func(t *testing.T) {
			workflowDir := t.TempDir()
			source := filepath.Join(workflowDir, "source")
			mustMkdir(t, source, 0o755)
			mustWrite(t, filepath.Join(source, "existing.txt"), "new", 0o644)
			mustWrite(t, filepath.Join(source, "created.txt"), "created", 0o644)
			runDir := t.TempDir()
			destination := filepath.Join(runDir, "target")
			mustMkdir(t, destination, 0o755)
			mustWrite(t, filepath.Join(destination, "existing.txt"), "old", 0o600)

			runner := newRunner(t, map[string]any{"from": "source", "into": "target", "on_conflict": policy})
			result, err := runner.Run(t.Context(), step.Request{
				WorkflowDir: workflowDir, RunDir: runDir, TemplateRenderer: newRenderer(t, nil, map[string]any{}),
			})
			if policy == conflictFail {
				if err == nil || !strings.Contains(err.Error(), "already exists") {
					t.Fatalf("Run() error = %v", err)
				}
				if _, err := os.Stat(filepath.Join(destination, "created.txt")); !os.IsNotExist(err) {
					t.Fatalf("fail policy wrote a new file: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(destination, "existing.txt"))
			if err != nil {
				t.Fatal(err)
			}
			want := "old"
			if policy == conflictOverwrite {
				want = "new"
			}
			if string(data) != want {
				t.Fatalf("existing content = %q, want %q", data, want)
			}
			if result.Outputs["created"] != 1 {
				t.Fatalf("outputs = %#v", result.Outputs)
			}
			if policy == conflictSkip && result.Outputs["skipped"] != 1 {
				t.Fatalf("outputs = %#v", result.Outputs)
			}
			if policy == conflictOverwrite && result.Outputs["overwritten"] != 1 {
				t.Fatalf("outputs = %#v", result.Outputs)
			}
		})
	}
}

func TestRunRejectsUnsafeTrees(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, string, string)
		vars    map[string]any
		want    string
	}{
		{
			name: "binary file", want: "not valid UTF-8",
			prepare: func(t *testing.T, source, _ string) {
				mustWriteBytes(t, filepath.Join(source, "binary"), []byte{0xff}, 0o644)
			},
		},
		{
			name: "rendered separator", vars: map[string]any{"name": "../escape"}, want: "invalid path component",
			prepare: func(t *testing.T, source, _ string) {
				mustWrite(t, filepath.Join(source, "{{ .vars.name }}"), "safe", 0o644)
			},
		},
		{
			name: "duplicate rendered path", vars: map[string]any{"first": "same", "second": "same"}, want: "duplicate path",
			prepare: func(t *testing.T, source, _ string) {
				mustWrite(t, filepath.Join(source, "{{ .vars.first }}"), "one", 0o644)
				mustWrite(t, filepath.Join(source, "{{ .vars.second }}"), "two", 0o644)
			},
		},
		{
			name: "case-only rendered path", vars: map[string]any{"first": "Same", "second": "same"}, want: "differ only in case",
			prepare: func(t *testing.T, source, _ string) {
				mustWrite(t, filepath.Join(source, "{{ .vars.first }}"), "one", 0o644)
				mustWrite(t, filepath.Join(source, "{{ .vars.second }}"), "two", 0o644)
			},
		},
		{
			name: "source symlink", want: "symbolic link",
			prepare: func(t *testing.T, source, root string) {
				mustWrite(t, filepath.Join(root, "outside"), "outside", 0o644)
				if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(source, "link")); err != nil {
					t.Skipf("creating symlink: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workflowDir := t.TempDir()
			source := filepath.Join(workflowDir, "source")
			mustMkdir(t, source, 0o755)
			test.prepare(t, source, workflowDir)
			runner := newRunner(t, map[string]any{"from": "source", "into": "target"})
			_, err := runner.Run(t.Context(), step.Request{
				WorkflowDir: workflowDir, RunDir: t.TempDir(),
				TemplateRenderer: newRenderer(t, nil, map[string]any{"vars": test.vars}),
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunRejectsSymlinkedDestinationDirectory(t *testing.T) {
	workflowDir := t.TempDir()
	source := filepath.Join(workflowDir, "source")
	mustMkdir(t, source, 0o755)
	mustWrite(t, filepath.Join(source, "file"), "content", 0o644)
	runDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(runDir, "target")); err != nil {
		t.Skipf("creating symlink: %v", err)
	}
	runner := newRunner(t, map[string]any{"from": "source", "into": "target", "on_conflict": "overwrite"})
	_, err := runner.Run(t.Context(), step.Request{
		WorkflowDir: workflowDir, RunDir: runDir, TemplateRenderer: newRenderer(t, nil, map[string]any{}),
	})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunOverwriteReplacesLeafSymlink(t *testing.T) {
	workflowDir := t.TempDir()
	source := filepath.Join(workflowDir, "source")
	mustMkdir(t, source, 0o755)
	mustWrite(t, filepath.Join(source, "file"), "generated", 0o644)
	runDir := t.TempDir()
	destination := filepath.Join(runDir, "target")
	mustMkdir(t, destination, 0o755)
	outside := filepath.Join(runDir, "outside")
	mustWrite(t, outside, "outside", 0o644)
	if err := os.Symlink(outside, filepath.Join(destination, "file")); err != nil {
		t.Skipf("creating symlink: %v", err)
	}
	runner := newRunner(t, map[string]any{"from": "source", "into": "target", "on_conflict": "overwrite"})
	result, err := runner.Run(t.Context(), step.Request{
		WorkflowDir: workflowDir, RunDir: runDir, TemplateRenderer: newRenderer(t, nil, map[string]any{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(destination, "file"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || result.Outputs["overwritten"] != 1 {
		t.Fatalf("mode = %v, outputs = %#v", info.Mode(), result.Outputs)
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "outside" {
		t.Fatalf("symlink target changed to %q", data)
	}
}

func TestValidateChecksLiteralTreeTemplates(t *testing.T) {
	workflowDir := t.TempDir()
	source := filepath.Join(workflowDir, "source")
	mustMkdir(t, source, 0o755)
	mustWrite(t, filepath.Join(source, "file"), `{{ template "missing" . }}`, 0o644)
	runner := newRunner(t, map[string]any{"from": "source", "into": "target"})
	err := runner.Validate(t.Context(), step.Request{
		WorkflowDir: workflowDir, TemplateRenderer: newRenderer(t, nil, map[string]any{}),
	})
	if err == nil || !strings.Contains(err.Error(), "undefined template") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRunRejectsBorrowedWorkflowDirectory(t *testing.T) {
	workflowDir := t.TempDir()
	source := filepath.Join(workflowDir, "source")
	mustMkdir(t, source, 0o755)
	mustWrite(t, filepath.Join(source, "leak.txt"), "caller secret", 0o644)
	runner := newRunner(t, map[string]any{"from": "source", "into": "target"})
	runDir := t.TempDir()
	request := step.Request{
		WorkflowDir: workflowDir, WorkflowDirBorrowed: true, RunDir: runDir,
		TemplateRenderer: newRenderer(t, nil, map[string]any{}),
	}
	if err := runner.Validate(t.Context(), request); err == nil || !strings.Contains(err.Error(), "requires a packaged action") {
		t.Fatalf("Validate() error = %v", err)
	}
	_, err := runner.Run(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "requires a packaged action") {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "target")); !os.IsNotExist(err) {
		t.Fatalf("destination was created: %v", err)
	}
}

func TestRunHonorsCanceledContext(t *testing.T) {
	workflowDir := t.TempDir()
	mustMkdir(t, filepath.Join(workflowDir, "source"), 0o755)
	runner := newRunner(t, map[string]any{"from": "source", "into": "target"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := runner.Run(ctx, step.Request{
		WorkflowDir: workflowDir, RunDir: t.TempDir(), TemplateRenderer: newRenderer(t, nil, map[string]any{}),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func newRunner(t *testing.T, raw map[string]any) *Runner {
	t.Helper()
	runner, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return runner.(*Runner)
}

func newRenderer(t *testing.T, definitions map[string]workflow.TemplateDefinition, data map[string]any) boundRenderer {
	t.Helper()
	renderer, err := workflow.NewRenderer(definitions)
	if err != nil {
		t.Fatal(err)
	}
	return boundRenderer{renderer: renderer, data: data}
}

func mustMkdir(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	mustWriteBytes(t, path, []byte(content), mode)
}

func mustWriteBytes(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
