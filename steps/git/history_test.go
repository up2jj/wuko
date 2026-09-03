package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"gopkg.in/yaml.v3"
)

func TestNewRevisionValidatesConfiguration(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "blank", raw: map[string]any{"revision": "  "}, want: "must not be blank"},
		{name: "NUL", raw: map[string]any{"revision": "HEAD\x00"}, want: "must not contain NUL"},
		{name: "unknown", raw: map[string]any{"ref": "HEAD"}, want: "field ref"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRevision(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewRevision(%#v) error = %v, want %q", test.raw, err, test.want)
			}
		})
	}
	for _, raw := range []map[string]any{{}, {"revision": "v1.0.0"}, {"revision": "{{ .vars.ref }}"}} {
		if _, err := NewRevision(raw); err != nil {
			t.Fatalf("NewRevision(%#v) error = %v", raw, err)
		}
	}
}

func TestRegisterIncludesHistorySteps(t *testing.T) {
	registry := step.NewRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"git_revision", "git_log"} {
		if _, err := registry.Build(name, map[string]any{}); err != nil {
			t.Fatalf("building %s: %v", name, err)
		}
	}
}

func TestNewLogValidatesConfiguration(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "blank after", raw: map[string]any{"after": " "}, want: "after must not be blank"},
		{name: "blank through", raw: map[string]any{"through": " "}, want: "through must not be blank"},
		{name: "empty paths", raw: map[string]any{"paths": []any{}}, want: "at least one pathspec"},
		{name: "blank path", raw: map[string]any{"paths": []any{"src", " "}}, want: "paths[1]"},
		{name: "path NUL", raw: map[string]any{"paths": []any{"bad\x00path"}}, want: "must not contain NUL"},
		{name: "after NUL", raw: map[string]any{"after": "v1\x00"}, want: "must not contain NUL"},
		{name: "blank ancestry", raw: map[string]any{"ancestry": ""}, want: "ancestry must not be blank"},
		{name: "bad ancestry", raw: map[string]any{"ancestry": "linear"}, want: "all or first_parent"},
		{name: "templated ancestry", raw: map[string]any{"ancestry": "{{ .vars.mode }}"}, want: "must not be templated"},
		{name: "blank merges", raw: map[string]any{"merges": ""}, want: "merges must not be blank"},
		{name: "bad merges", raw: map[string]any{"merges": "hide"}, want: "include, exclude, or only"},
		{name: "templated merges", raw: map[string]any{"merges": "{{ .vars.mode }}"}, want: "must not be templated"},
		{name: "zero limit", raw: map[string]any{"limit": 0}, want: "between 1 and 1000"},
		{name: "negative limit", raw: map[string]any{"limit": -1}, want: "between 1 and 1000"},
		{name: "large limit", raw: map[string]any{"limit": 1001}, want: "between 1 and 1000"},
		{name: "unknown", raw: map[string]any{"order": "newest"}, want: "field order"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewLog(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewLog(%#v) error = %v, want %q", test.raw, err, test.want)
			}
		})
	}
	for _, raw := range []map[string]any{
		{},
		{"after": "{{ .vars.tag }}", "through": "{{ .vars.head }}", "paths": []any{"{{ .vars.path }}"}},
		{"ancestry": "first_parent", "merges": "only", "limit": 1000},
	} {
		if _, err := NewLog(raw); err != nil {
			t.Fatalf("NewLog(%#v) error = %v", raw, err)
		}
	}
}

func TestRevisionAndLogAgainstRepository(t *testing.T) {
	dir := initGitRepository(t)
	initial := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	mainBranch := strings.TrimSpace(runGitTest(t, dir, "branch", "--show-current"))

	runGitTest(t, dir, "checkout", "-q", "-b", "feature/history")
	writeHistoryFile(t, dir, "feature.txt", "feature\n")
	runGitTest(t, dir, "add", "feature.txt")
	runGitTest(t, dir, "commit", "-q", "-m", "feat(history): add structured records", "-m", "Keep release data typed.")
	feature := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))

	runGitTest(t, dir, "checkout", "-q", mainBranch)
	runGitTest(t, dir, "merge", "-q", "--no-ff", "feature/history", "-m", "Merge feature history")
	merge := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	writeHistoryFile(t, dir, "direct.txt", "direct\n")
	runGitTest(t, dir, "add", "direct.txt")
	runGitTest(t, dir, "commit", "-q", "-m", "fix(log): keep newest first")
	head := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))

	revision := runRevision(t, dir, map[string]any{})
	if revision["found"] != true || revision["sha"] != head || revision["subject"] != "fix(log): keep newest first" {
		t.Fatalf("revision outputs = %#v", revision)
	}
	if revision["is_merge"] != false || revision["conventional"].(map[string]any)["type"] != "fix" {
		t.Fatalf("revision metadata = %#v", revision)
	}

	all := runLog(t, dir, map[string]any{"after": initial})
	if all["after"] != initial || all["through"] != head || all["count"] != 3 || all["has_more"] != false {
		t.Fatalf("all outputs = %#v", all)
	}
	commits := outputCommits(t, all)
	if got := commitSHAs(commits); !slices.Equal(got, []string{head, merge, feature}) {
		t.Fatalf("all commit order = %#v", got)
	}
	mergeRecord := commits[1].(map[string]any)
	if mergeRecord["is_merge"] != true || mergeRecord["conventional"].(map[string]any)["classification"] != "merge" || mergeRecord["conventional"].(map[string]any)["valid"] != false {
		t.Fatalf("merge record = %#v", mergeRecord)
	}

	firstParent := runLog(t, dir, map[string]any{"after": initial, "ancestry": "first_parent"})
	if got := commitSHAs(outputCommits(t, firstParent)); !slices.Equal(got, []string{head, merge}) {
		t.Fatalf("first-parent commits = %#v", got)
	}
	onlyMerges := runLog(t, dir, map[string]any{"after": initial, "ancestry": "first_parent", "merges": "only"})
	if got := commitSHAs(outputCommits(t, onlyMerges)); !slices.Equal(got, []string{merge}) {
		t.Fatalf("merge-only commits = %#v", got)
	}
	withoutMerges := runLog(t, dir, map[string]any{"after": initial, "merges": "exclude"})
	if got := commitSHAs(outputCommits(t, withoutMerges)); !slices.Equal(got, []string{head, feature}) {
		t.Fatalf("no-merge commits = %#v", got)
	}
	pathLimited := runLog(t, dir, map[string]any{"after": initial, "paths": []any{"direct.txt"}})
	if got := commitSHAs(outputCommits(t, pathLimited)); !slices.Equal(got, []string{head}) {
		t.Fatalf("path-limited commits = %#v", got)
	}
	limited := runLog(t, dir, map[string]any{"after": initial, "limit": 1})
	if limited["count"] != 1 || limited["has_more"] != true || commitSHAs(outputCommits(t, limited))[0] != head {
		t.Fatalf("limited outputs = %#v", limited)
	}
	empty := runLog(t, dir, map[string]any{"after": "HEAD", "through": "HEAD"})
	if empty["count"] != 0 || empty["has_more"] != false || len(outputCommits(t, empty)) != 0 {
		t.Fatalf("empty outputs = %#v", empty)
	}
}

func TestRevisionReadsAnnotatedTagAndMessageFields(t *testing.T) {
	dir := initGitRepository(t)
	writeHistoryFile(t, dir, ".mailmap", "Canonical Author <canonical@example.com> Wuko Test <wuko@example.test>\n")
	runGitTest(t, dir, "add", ".mailmap")
	runGitTest(t, dir, "commit", "-q", "-m", "feat(api)!: support history", "-m", "Unicode: zażółć\n\nMore detail.")
	runGitTest(t, dir, "tag", "-a", "v1.0.0", "-m", "release")

	outputs := runRevision(t, dir, map[string]any{"revision": "v1.0.0"})
	if outputs["found"] != true || outputs["subject"] != "feat(api)!: support history" {
		t.Fatalf("outputs = %#v", outputs)
	}
	if !strings.Contains(outputs["body"].(string), "Unicode: zażółć") || !strings.Contains(outputs["message"].(string), "More detail.") {
		t.Fatalf("message outputs = %#v", outputs)
	}
	author := outputs["author"].(map[string]any)
	if author["name"] != "Canonical Author" || author["email"] != "canonical@example.com" {
		t.Fatalf("mailmapped author = %#v", author)
	}
	conventional := outputs["conventional"].(map[string]any)
	if conventional["valid"] != true || conventional["type"] != "feat" || conventional["scope"] != "api" || conventional["breaking"] != true {
		t.Fatalf("conventional = %#v", conventional)
	}
}

func TestHistoryHandlesUnbornRepository(t *testing.T) {
	dir := t.TempDir()
	runGitTest(t, dir, "init", "--quiet")
	outputs := runRevision(t, dir, map[string]any{})
	if outputs["found"] != false || outputs["sha"] != "" || len(outputs["parents"].([]any)) != 0 {
		t.Fatalf("revision outputs = %#v", outputs)
	}
	log := runLog(t, dir, map[string]any{})
	if log["through"] != "" || log["count"] != 0 || len(outputCommits(t, log)) != 0 {
		t.Fatalf("log outputs = %#v", log)
	}
	runner, err := NewRevision(map[string]any{"revision": "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{RunDir: dir}); err == nil || !strings.Contains(err.Error(), "resolving Git revision") {
		t.Fatalf("explicit HEAD error = %v", err)
	}
}

func TestHistoryRejectsUnresolvedTemplatesAndDivergentBoundary(t *testing.T) {
	revision, err := NewRevision(map[string]any{"revision": "{{ .vars.ref }}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := revision.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "unresolved template") {
		t.Fatalf("revision error = %v", err)
	}
	log, err := NewLog(map[string]any{"paths": []any{"{{ .vars.path }}"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "unresolved template") {
		t.Fatalf("log error = %v", err)
	}

	executor := &scriptedGitExecutor{results: []scriptedGitResult{
		{result: process.Result{Stdout: "through-id\n"}},
		{result: process.Result{Stdout: "after-id\n"}},
		{err: &process.ExitError{Command: "git", Code: 1}},
	}}
	log, err = NewLog(map[string]any{"after": "release", "through": "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Run(t.Context(), step.Request{Executor: executor}); err == nil || !strings.Contains(err.Error(), `"release" is not an ancestor of "main"`) {
		t.Fatalf("boundary error = %v", err)
	}
}

func TestHistoryUsesCaptureOnlyExecutorAndRejectsTruncation(t *testing.T) {
	executor := &scriptedGitExecutor{results: []scriptedGitResult{
		{result: process.Result{Stdout: "commit-id\n"}},
		{result: process.Result{StdoutTruncated: true}},
	}}
	runner, err := NewRevision(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{Executor: executor, Env: map[string]string{"MODE": "test"}})
	if err == nil || !strings.Contains(err.Error(), "output exceeded 16 MiB") {
		t.Fatalf("truncation error = %v", err)
	}
	for _, call := range executor.calls {
		if call.StdoutPolicy != process.OutputCapture || call.StderrPolicy != process.OutputCapture || call.CaptureLimit != historyCaptureLimit {
			t.Fatalf("process options = %#v", call)
		}
		if call.Env["MODE"] != "test" || call.Command != "git" {
			t.Fatalf("process options = %#v", call)
		}
	}
	if _, ok := runner.(step.ExecutorAware); !ok {
		t.Fatal("git_revision is not executor-aware")
	}
	log, err := NewLog(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := log.(step.ExecutorAware); !ok {
		t.Fatal("git_log is not executor-aware")
	}
}

func TestHistoryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, builder := range []step.Builder{NewRevision, NewLog} {
		runner, err := builder(map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Run(ctx, step.Request{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	}
}

func TestHistoryDocumentationExamples(t *testing.T) {
	data, err := os.ReadFile("../../docs/steps-system.md")
	if err != nil {
		t.Fatal(err)
	}
	section := historyDocumentationSection(data)
	if section == nil {
		t.Fatal("git_revision and git_log documentation section not found")
	}
	blocks := regexpYAMLBlocks(section)
	if len(blocks) < 8 {
		t.Fatalf("found %d YAML examples, want at least 8", len(blocks))
	}
	type documentedStep struct {
		Type string         `yaml:"type"`
		With map[string]any `yaml:"with"`
	}
	built := 0
	for blockIndex, block := range blocks {
		var steps []documentedStep
		if err := yaml.Unmarshal(block, &steps); err != nil {
			var document struct {
				Steps []documentedStep `yaml:"steps"`
			}
			if documentErr := yaml.Unmarshal(block, &document); documentErr != nil {
				t.Fatalf("example %d: %v", blockIndex+1, err)
			}
			steps = document.Steps
		}
		for stepIndex, documented := range steps {
			var buildErr error
			switch documented.Type {
			case "git_revision":
				_, buildErr = NewRevision(documented.With)
				built++
			case "git_log":
				_, buildErr = NewLog(documented.With)
				built++
			case "", "assert", "shell", "tui_table":
				continue
			default:
				buildErr = errors.New("unexpected documented step type")
			}
			if buildErr != nil {
				t.Fatalf("example %d step %d (%s): %v", blockIndex+1, stepIndex+1, documented.Type, buildErr)
			}
		}
	}
	if built < 8 {
		t.Fatalf("built %d documented history steps, want at least 8", built)
	}
}

func historyDocumentationSection(data []byte) []byte {
	start := []byte("## `git_revision`")
	end := []byte("## `git_conventional_commit`")
	startIndex := strings.Index(string(data), string(start))
	endIndex := strings.Index(string(data), string(end))
	if startIndex < 0 || endIndex <= startIndex {
		return nil
	}
	return data[startIndex:endIndex]
}

func regexpYAMLBlocks(section []byte) [][]byte {
	parts := strings.Split(string(section), "```yaml\n")
	blocks := make([][]byte, 0, len(parts)-1)
	for _, part := range parts[1:] {
		block, _, found := strings.Cut(part, "```")
		if found {
			blocks = append(blocks, []byte(block))
		}
	}
	return blocks
}

func runRevision(t *testing.T, dir string, raw map[string]any) map[string]any {
	t.Helper()
	runner, err := NewRevision(raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return result.Outputs
}

func runLog(t *testing.T, dir string, raw map[string]any) map[string]any {
	t.Helper()
	runner, err := NewLog(raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return result.Outputs
}

func outputCommits(t *testing.T, outputs map[string]any) []any {
	t.Helper()
	commits, ok := outputs["commits"].([]any)
	if !ok {
		t.Fatalf("commits = %#v", outputs["commits"])
	}
	return commits
}

func commitSHAs(commits []any) []string {
	values := make([]string, len(commits))
	for index, commit := range commits {
		values[index] = commit.(map[string]any)["sha"].(string)
	}
	return values
}

func writeHistoryFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
