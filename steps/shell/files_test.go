package shell

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

func TestChunkPathsStaysWithinTheArgumentBudget(t *testing.T) {
	t.Parallel()
	paths := make([]string, 3000)
	for index := range paths {
		paths[index] = strings.Repeat("p", 90) + strings.Repeat("x", index%7)
	}
	paths = append(paths, strings.Repeat("y", filesArgumentBudget))
	chunks := chunkPaths("gofmt", []string{"-l", "--"}, paths)
	var flattened []string
	for index, chunk := range chunks {
		size := len("gofmt") + 1 + len("-l") + 1 + len("--") + 1
		for _, path := range chunk {
			size += len(path) + 1
		}
		if size > filesArgumentBudget && len(chunk) > 1 {
			t.Fatalf("chunk %d uses %d bytes over a %d budget", index, size, filesArgumentBudget)
		}
		flattened = append(flattened, chunk...)
	}
	if len(chunks) < 3 || len(chunks[len(chunks)-1]) != 1 {
		t.Fatalf("got %d chunks; want several and the oversized path alone", len(chunks))
	}
	if diff := cmp.Diff(paths, flattened); diff != "" {
		t.Fatalf("paths were reordered or lost (-want +got):\n%s", diff)
	}
}

func TestShellFilesSplitsLongListsAcrossRuns(t *testing.T) {
	t.Parallel()
	paths := make([]any, 3000)
	want := make([]string, len(paths))
	for index := range paths {
		want[index] = "dir/" + strings.Repeat("f", 80) + string(rune('a'+index%26))
		paths[index] = want[index]
	}
	runner := steptest.Build(t, New, map[string]any{
		"script": `printf '%s\n' "$@"`, "files": map[string]any{"expr": "vars.paths"}, "stdout": "capture",
	})
	result, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir(), Vars: map[string]any{"paths": paths}, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSuffix(result.Outputs["stdout"].(string), "\n"), "\n"); !cmp.Equal(want, got) {
		t.Fatalf("files received %d paths, want all %d in order", len(got), len(want))
	}
	if runs := result.Outputs["runs"].(int); runs < 2 {
		t.Fatalf("runs = %d, want the list split", runs)
	}
}

func TestShellFilesRunsEveryChunkAndReportsTheFirstFailure(t *testing.T) {
	t.Parallel()
	paths := []any{"bad"}
	for range 2000 {
		paths = append(paths, strings.Repeat("g", 100))
	}
	runner := steptest.Build(t, New, map[string]any{
		"script": `for path; do [ "$path" = bad ] && exit 3; done; echo done`, "files": paths, "stdout": "capture",
	})
	result, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	var exitErr *process.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 3 {
		t.Fatalf("error = %v, want exit 3 from the first chunk", err)
	}
	if result.Outputs["exit_code"] != 3 || !strings.Contains(result.Outputs["stdout"].(string), "done") {
		t.Fatalf("outputs = %v, want later chunks to run", result.Outputs)
	}
}

func TestShellFilesSkipsAnEmptyList(t *testing.T) {
	t.Parallel()
	runner := steptest.Build(t, New, map[string]any{"command": "false", "files": map[string]any{"expr": "vars.paths"}})
	result, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir(), Vars: map[string]any{"paths": []any{}}})
	if err != nil || result.Outputs["runs"] != 0 {
		t.Fatalf("result = %v, %v; want no run", result.Outputs, err)
	}
}

func TestShellFilesRejectsInteractiveModes(t *testing.T) {
	t.Parallel()
	for _, raw := range []map[string]any{
		{"command": "vi", "tty": true, "files": []any{"a"}},
		{"script": "true", "sdk": "bash", "files": []any{"a"}},
		{"command": "true", "files": []any{" "}},
	} {
		if _, err := New(raw); err == nil {
			t.Errorf("New(%v) accepted an invalid files configuration", raw)
		}
	}
}
