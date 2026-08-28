package changed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	storepkg "github.com/up2jj/wuko/keyvalue"
	"github.com/up2jj/wuko/step"
)

// The snapshot store shares the values directory with workflow-managed stores, so a
// workflow must not be able to open it by name and rewrite the detector's history.
func TestSnapshotStoreNameIsReservedFromWorkflows(t *testing.T) {
	dir := t.TempDir()
	if _, err := storepkg.OpenWorkflowScoped(dir, dir, storepkg.Local, storeName); err == nil {
		t.Fatalf("a workflow can open the changed snapshot store %q", storeName)
	}
}

func TestChangedTracksFilesDirectoriesGlobsAndValues(t *testing.T) {
	runDir := t.TempDir()
	localDir := filepath.Join(runDir, ".wuko", "values")
	writeTestFile(t, runDir, "go.mod", "module example\n")
	writeTestFile(t, runDir, "src/main.go", "package main\n")
	writeTestFile(t, runDir, "assets/logo.txt", "logo\n")
	writeTestFile(t, runDir, "assets/.ignored", "hidden\n")
	runner := newTestRunner(t, map[string]any{
		"files":  []any{"go.mod", "src/**/*.go", "assets", "src/**/*.go"},
		"values": map[string]any{"target": "linux", "options": map[string]any{"race": true, "count": 2}},
	})
	request := testRequest(runDir, localDir)
	if !runChanged(t, runner, request) {
		t.Fatal("first run did not report a change")
	}
	if runChanged(t, runner, request) {
		t.Fatal("identical run reported a change")
	}

	mainPath := filepath.Join(runDir, "src", "main.go")
	if err := os.Chmod(mainPath, 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(time.Hour)
	if err := os.Chtimes(mainPath, when, when); err != nil {
		t.Fatal(err)
	}
	if runChanged(t, runner, request) {
		t.Fatal("metadata-only update reported a change")
	}

	writeTestFile(t, runDir, "src/main.go", "package changed\n")
	if !runChanged(t, runner, request) || runChanged(t, runner, request) {
		t.Fatal("content update was not reported exactly once")
	}
	writeTestFile(t, runDir, "src/extra.go", "package changed\n")
	if !runChanged(t, runner, request) {
		t.Fatal("glob addition was not reported")
	}
	if err := os.Remove(filepath.Join(runDir, "src", "extra.go")); err != nil {
		t.Fatal(err)
	}
	if !runChanged(t, runner, request) {
		t.Fatal("glob deletion was not reported")
	}

	writeTestFile(t, runDir, "assets/.ignored", "changed hidden content\n")
	if runChanged(t, runner, request) {
		t.Fatal("implicitly selected hidden file affected the fingerprint")
	}
	changedValues := newTestRunner(t, map[string]any{
		"files": []any{"assets", "src/**/*.go", "go.mod"},
		"values": map[string]any{
			"options": map[string]any{"count": 2, "race": true}, "target": "darwin",
		},
	})
	if !runChanged(t, changedValues, request) || runChanged(t, changedValues, request) {
		t.Fatal("value update was not reported exactly once")
	}
}

func TestChangedTracksMissingPathsAndExcludesOwnStore(t *testing.T) {
	runDir := t.TempDir()
	localDir := filepath.Join(runDir, ".wuko", "values")
	runner := newTestRunner(t, map[string]any{
		"files": []any{"missing.txt", ".wuko/values/**"},
	})
	request := testRequest(runDir, localDir)
	if !runChanged(t, runner, request) || runChanged(t, runner, request) {
		t.Fatal("snapshot file caused self-triggering")
	}
	writeTestFile(t, runDir, "missing.txt", "present")
	if !runChanged(t, runner, request) {
		t.Fatal("creation of a missing literal path was not reported")
	}
	if err := os.Remove(filepath.Join(runDir, "missing.txt")); err != nil {
		t.Fatal(err)
	}
	if !runChanged(t, runner, request) {
		t.Fatal("deletion of a literal path was not reported")
	}
}

func TestChangedDoesNotFollowSymlinksInLiteralPrefixes(t *testing.T) {
	runDir := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, outside, "secret.txt", "first\n")
	if err := os.Symlink(outside, filepath.Join(runDir, "linked")); err != nil {
		t.Skipf("creating directory symlink: %v", err)
	}
	runner := newTestRunner(t, map[string]any{
		"files": []any{"linked/secret.txt", "linked/**/*.txt"},
	})
	request := testRequest(runDir, filepath.Join(runDir, ".wuko", "values"))
	if !runChanged(t, runner, request) || runChanged(t, runner, request) {
		t.Fatal("symlinked inputs did not stabilize as excluded")
	}
	writeTestFile(t, outside, "secret.txt", "second\n")
	if runChanged(t, runner, request) {
		t.Fatal("content reached through a symlink changed the fingerprint")
	}
}

func TestChangedSnapshotIdentityAndExplicitKey(t *testing.T) {
	runDir := t.TempDir()
	localDir := filepath.Join(runDir, ".wuko", "values")
	runner := newTestRunner(t, map[string]any{"values": map[string]any{"value": true}})
	first := testRequest(runDir, localDir)
	if !runChanged(t, runner, first) || runChanged(t, runner, first) {
		t.Fatal("default identity did not stabilize")
	}
	second := first
	second.WorkflowSource = "/project/other.yaml"
	if !runChanged(t, runner, second) {
		t.Fatal("workflow source did not namespace the snapshot")
	}
	explicit := newTestRunner(t, map[string]any{"key": "iteration-linux", "values": map[string]any{"value": true}})
	if !runChanged(t, explicit, first) || runChanged(t, explicit, first) {
		t.Fatal("explicit key did not create a stable snapshot")
	}
}

func TestChangedPreservesValueTypesAndIgnoresMapOrder(t *testing.T) {
	runDir := t.TempDir()
	request := testRequest(runDir, filepath.Join(runDir, ".wuko", "values"))
	first := newTestRunner(t, map[string]any{"values": map[string]any{
		"enabled": true, "count": 2, "items": []any{"a", map[string]any{"left": 1, "right": false}},
	}})
	if !runChanged(t, first, request) {
		t.Fatal("first typed value snapshot was unchanged")
	}
	reordered := newTestRunner(t, map[string]any{"values": map[string]any{
		"items": []any{"a", map[string]any{"right": false, "left": 1}}, "count": 2, "enabled": true,
	}})
	if runChanged(t, reordered, request) {
		t.Fatal("map insertion order affected the fingerprint")
	}
	typeChanged := newTestRunner(t, map[string]any{"values": map[string]any{
		"enabled": true, "count": "2", "items": []any{"a", map[string]any{"left": 1, "right": false}},
	}})
	if !runChanged(t, typeChanged, request) {
		t.Fatal("number-to-string type change was not reported")
	}
}

func TestChangedRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "no inputs", raw: map[string]any{}, want: "at least one"},
		{name: "empty key", raw: map[string]any{"key": "", "values": map[string]any{"x": true}}, want: "key must not be empty"},
		{name: "empty root", raw: map[string]any{"root": "", "values": map[string]any{"x": true}}, want: "root must not be empty"},
		{name: "empty file", raw: map[string]any{"files": []any{""}}, want: "must not be empty"},
		{name: "absolute file", raw: map[string]any{"files": []any{"/tmp/file"}}, want: "relative to root"},
		{name: "parent file", raw: map[string]any{"files": []any{"../file"}}, want: "parent directory"},
		{name: "invalid glob", raw: map[string]any{"files": []any{"[bad"}}, want: "invalid pattern"},
		{name: "unknown field", raw: map[string]any{"values": map[string]any{"x": true}, "unknown": true}, want: "field unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestChangedValidationRuntimeErrorsAndCancellation(t *testing.T) {
	runner := newTestRunner(t, map[string]any{"values": map[string]any{"value": true}})
	if err := runner.(step.Validator).Validate(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "local workflow storage") {
		t.Fatalf("validation error = %v", err)
	}

	unresolved := newTestRunner(t, map[string]any{"key": "{{ .vars.key }}", "values": map[string]any{"value": true}})
	_, err := unresolved.Run(t.Context(), testRequest(t.TempDir(), t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "unresolved template") {
		t.Fatalf("unresolved key error = %v", err)
	}

	missingRoot := newTestRunner(t, map[string]any{"root": "missing", "files": []any{"**"}})
	_, err = missingRoot.Run(t.Context(), testRequest(t.TempDir(), t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "inspecting changed root") {
		t.Fatalf("missing root error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = runner.Run(ctx, testRequest(t.TempDir(), t.TempDir()))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func newTestRunner(t *testing.T, raw map[string]any) step.Runner {
	t.Helper()
	runner, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func runChanged(t *testing.T, runner step.Runner, request step.Request) bool {
	t.Helper()
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	changed, ok := result.Outputs["changed"].(bool)
	if !ok {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	return changed
}

func testRequest(runDir, localDir string) step.Request {
	return step.Request{
		StepID: "detect", WorkflowName: "build", WorkflowSource: "/project/workflow.yaml",
		RunDir: runDir, LocalValueDir: localDir,
	}
}

func writeTestFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
