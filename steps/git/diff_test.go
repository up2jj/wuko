package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/expr-lang/expr"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/provider"
	"github.com/up2jj/wuko/step"
	"gopkg.in/yaml.v3"
)

func TestNewDiffValidatesConfiguration(t *testing.T) {
	for _, test := range []struct {
		name    string
		builder step.Builder
		raw     map[string]any
		want    string
	}{
		{"unknown diff source", NewDiff, map[string]any{"source": "push"}, "commit, staged, or unstaged"},
		{"templated source", NewDiff, map[string]any{"source": "{{ .vars.source }}"}, "must not be templated"},
		{"staged from", NewDiff, map[string]any{"source": "staged", "from": "HEAD"}, "only supported"},
		{"unstaged through", NewDiff, map[string]any{"source": "unstaged", "through": "HEAD"}, "only supported"},
		{"blank from", NewDiff, map[string]any{"from": " "}, "from must not be blank"},
		{"empty paths", NewDiff, map[string]any{"paths": []any{}}, "at least one pathspec"},
		{"blank path", NewDiff, map[string]any{"paths": []any{"src", " "}}, "paths[1]"},
		{"copies without renames", NewDiff, map[string]any{"copies": true, "renames": false}, "copies requires renames"},
		{"unknown diff field", NewDiff, map[string]any{"format": "patch"}, "field format"},
		{"missing check source", NewDiffCheck, map[string]any{}, "source is required"},
		{"bad check source", NewDiffCheck, map[string]any{"source": "index"}, "commit, staged, unstaged, or pushed"},
		{"updates on staged", NewDiffCheck, map[string]any{"source": "staged", "updates": map[string]any{"expr": "vars.updates"}}, "only supported"},
		{"blank updates", NewDiffCheck, map[string]any{"source": "pushed", "updates": map[string]any{"expr": " "}}, "non-empty"},
		{"invalid updates expression", NewDiffCheck, map[string]any{"source": "pushed", "updates": map[string]any{"expr": "("}}, "compiling updates expr"},
		{"unknown check field", NewDiffCheck, map[string]any{"source": "staged", "renames": true}, "field renames"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.builder(test.raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New(%#v) error = %v, want %q", test.raw, err, test.want)
			}
		})
	}
	for _, raw := range []map[string]any{
		{}, {"source": "staged"}, {"source": "unstaged", "paths": []any{"src"}},
		{"from": "{{ .vars.base }}", "through": "{{ .vars.head }}", "copies": true},
	} {
		if _, err := NewDiff(raw); err != nil {
			t.Fatalf("NewDiff(%#v): %v", raw, err)
		}
	}
	for _, raw := range []map[string]any{
		{"source": "commit"}, {"source": "staged"}, {"source": "unstaged"}, {"source": "pushed"},
		{"source": "pushed", "updates": map[string]any{"expr": "git.hook.payload.updates"}},
	} {
		if _, err := NewDiffCheck(raw); err != nil {
			t.Fatalf("NewDiffCheck(%#v): %v", raw, err)
		}
	}
}

func TestGitDiffReadsStructuredCommitStagedAndUnstagedChanges(t *testing.T) {
	dir := initGitRepository(t)
	base := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	writeHistoryFile(t, dir, "old name.txt", "one\ntwo\n")
	runGitTest(t, dir, "add", "old name.txt")
	runGitTest(t, dir, "commit", "--quiet", "-m", "add old name")
	previous := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	if err := os.Rename(filepath.Join(dir, "old name.txt"), filepath.Join(dir, "new name.txt")); err != nil {
		t.Fatal(err)
	}
	writeHistoryFile(t, dir, "new name.txt", "one\ntwo\nthree\n")
	if err := os.WriteFile(filepath.Join(dir, "binary.dat"), []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "-A")
	runGitTest(t, dir, "commit", "--quiet", "-m", "rename and add binary")
	head := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))

	outputs := runDiff(t, dir, map[string]any{"from": previous, "through": head})
	if outputs["source"] != "commit" || outputs["from"] != previous || outputs["through"] != head || outputs["changed"] != true {
		t.Fatalf("outputs = %#v", outputs)
	}
	files := outputs["files"].([]any)
	if len(files) != 2 || outputs["count"] != 2 || outputs["binary_files"] != 1 {
		t.Fatalf("outputs = %#v", outputs)
	}
	if outputs["additions"] != 1 || outputs["deletions"] != 0 {
		t.Fatalf("line totals = %#v", outputs)
	}
	renamed := findDiffFile(t, files, "new name.txt")
	if renamed["status"] != "renamed" || renamed["previous_path"] != "old name.txt" || renamed["path"] != "new name.txt" || renamed["additions"] != 1 {
		t.Fatalf("renamed = %#v", renamed)
	}
	binary := findDiffFile(t, files, "binary.dat")
	if binary["path"] != "binary.dat" || binary["binary"] != true || binary["additions"] != 0 {
		t.Fatalf("binary = %#v", binary)
	}

	writeHistoryFile(t, dir, "staged.txt", "staged\n")
	runGitTest(t, dir, "add", "staged.txt")
	writeHistoryFile(t, dir, "README.md", "unstaged\n")
	writeHistoryFile(t, dir, "untracked.txt", "ignored\n")
	staged := runDiff(t, dir, map[string]any{"source": "staged"})
	if got := diffPaths(staged); !slices.Equal(got, []string{"staged.txt"}) {
		t.Fatalf("staged paths = %#v", got)
	}
	unstaged := runDiff(t, dir, map[string]any{"source": "unstaged"})
	if got := diffPaths(unstaged); !slices.Equal(got, []string{"README.md"}) {
		t.Fatalf("unstaged paths = %#v", got)
	}
	limited := runDiff(t, dir, map[string]any{"source": "staged", "paths": []any{"README.md"}})
	if limited["changed"] != false || limited["count"] != 0 {
		t.Fatalf("path-limited outputs = %#v", limited)
	}
	if base == head {
		t.Fatal("test history did not advance")
	}
}

func TestGitDiffUsesFirstParentAndHandlesRootAndDivergentTrees(t *testing.T) {
	dir := initGitRepository(t)
	root := strings.TrimSpace(runGitTest(t, dir, "rev-list", "--max-parents=0", "HEAD"))
	rootDiff := runDiff(t, dir, map[string]any{"through": root})
	if rootDiff["from"] != "" || rootDiff["through"] != root || rootDiff["count"] != 1 {
		t.Fatalf("root outputs = %#v", rootDiff)
	}

	main := strings.TrimSpace(runGitTest(t, dir, "branch", "--show-current"))
	runGitTest(t, dir, "checkout", "-q", "-b", "left")
	writeHistoryFile(t, dir, "left.txt", "left\n")
	runGitTest(t, dir, "add", "left.txt")
	runGitTest(t, dir, "commit", "-qm", "left")
	left := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	runGitTest(t, dir, "checkout", "-q", main)
	runGitTest(t, dir, "checkout", "-q", "-b", "right")
	writeHistoryFile(t, dir, "right.txt", "right\n")
	runGitTest(t, dir, "add", "right.txt")
	runGitTest(t, dir, "commit", "-qm", "right")
	right := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	divergent := runDiff(t, dir, map[string]any{"from": left, "through": right})
	if got := diffPaths(divergent); !slices.Equal(got, []string{"left.txt", "right.txt"}) {
		t.Fatalf("divergent paths = %#v", got)
	}
	runGitTest(t, dir, "merge", "-q", "--no-ff", "left", "-m", "merge left")
	merge := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	mergeDiff := runDiff(t, dir, map[string]any{"through": merge})
	if mergeDiff["from"] != right || !slices.Equal(diffPaths(mergeDiff), []string{"left.txt"}) {
		t.Fatalf("merge first-parent outputs = %#v", mergeDiff)
	}
}

func TestGitDiffDetectsCopiesModesAndUnusualPaths(t *testing.T) {
	dir := initGitRepository(t)
	writeHistoryFile(t, dir, "source.txt", "copy me\n")
	runGitTest(t, dir, "add", "source.txt")
	runGitTest(t, dir, "commit", "-qm", "add source")
	writeHistoryFile(t, dir, "copied\nfile.txt", "copy me\n")
	if err := os.Chmod(filepath.Join(dir, "README.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "-A")
	outputs := runDiff(t, dir, map[string]any{"source": "staged", "copies": true})
	copyRecord := findDiffFile(t, outputs["files"].([]any), "copied\nfile.txt")
	if copyRecord["status"] != "copied" || copyRecord["previous_path"] != "source.txt" || copyRecord["score"] != 100 {
		t.Fatalf("copy record = %#v", copyRecord)
	}
	modeRecord := findDiffFile(t, outputs["files"].([]any), "README.md")
	if modeRecord["old_mode"] != "100644" || modeRecord["new_mode"] != "100755" {
		t.Fatalf("mode record = %#v", modeRecord)
	}
}

func TestGitDiffHandlesUnbornStagedRepository(t *testing.T) {
	dir := t.TempDir()
	runGitTest(t, dir, "init", "--quiet")
	writeHistoryFile(t, dir, "first.txt", "first\n")
	runGitTest(t, dir, "add", "first.txt")
	outputs := runDiff(t, dir, map[string]any{"source": "staged"})
	if outputs["count"] != 1 || diffPaths(outputs)[0] != "first.txt" {
		t.Fatalf("outputs = %#v", outputs)
	}
}

func TestGitDiffReportsUnmergedPathOnce(t *testing.T) {
	dir := initGitRepository(t)
	main := strings.TrimSpace(runGitTest(t, dir, "branch", "--show-current"))
	runGitTest(t, dir, "checkout", "-qb", "conflict")
	writeHistoryFile(t, dir, "README.md", "branch\n")
	runGitTest(t, dir, "commit", "-qam", "branch change")
	runGitTest(t, dir, "checkout", "-q", main)
	writeHistoryFile(t, dir, "README.md", "main\n")
	runGitTest(t, dir, "commit", "-qam", "main change")
	command := exec.Command("git", "merge", "conflict")
	command.Dir = dir
	if err := command.Run(); err == nil {
		t.Fatal("merge unexpectedly succeeded")
	}
	outputs := runDiff(t, dir, map[string]any{"source": "unstaged"})
	files := outputs["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("files = %#v, want one coalesced conflict", files)
	}
	file := files[0].(map[string]any)
	if file["status"] != "unmerged" || file["path"] != "README.md" {
		t.Fatalf("conflict file = %#v", file)
	}
}

func TestGitDiffDisablesRenameDetectionExplicitly(t *testing.T) {
	dir := initGitRepository(t)
	writeHistoryFile(t, dir, "old.txt", "one\ntwo\nthree\nfour\nfive\n")
	runGitTest(t, dir, "add", "old.txt")
	runGitTest(t, dir, "commit", "-qm", "add old")
	runGitTest(t, dir, "mv", "old.txt", "new.txt")
	for _, source := range []string{"staged", "commit"} {
		if source == "commit" {
			runGitTest(t, dir, "commit", "-qm", "move old")
		}
		// Porcelain git diff honours diff.renames, which defaults to true, so renames: false must
		// reach Git as an explicit flag rather than as an omitted -M.
		outputs := runDiff(t, dir, map[string]any{"source": source, "renames": false})
		if got := diffPaths(outputs); !slices.Equal(got, []string{"new.txt", "old.txt"}) {
			t.Fatalf("%s renames=false paths = %#v, want a separate add and delete", source, got)
		}
		renamed := runDiff(t, dir, map[string]any{"source": source})
		if got := diffPaths(renamed); !slices.Equal(got, []string{"new.txt"}) {
			t.Fatalf("%s default paths = %#v, want one rename", source, got)
		}
	}
}

func TestGitDiffCheckIgnoresRenamedContentAndMergeCommits(t *testing.T) {
	dir := initGitRepository(t)
	base := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	writeHistoryFile(t, dir, "legacy.txt", "legacy \n")
	runGitTest(t, dir, "add", "legacy.txt")
	runGitTest(t, dir, "commit", "-qm", "legacy whitespace")
	runGitTest(t, dir, "checkout", "-qb", "legacy")
	legacy := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	runGitTest(t, dir, "update-ref", "refs/remotes/origin/legacy", legacy)

	runGitTest(t, dir, "mv", "legacy.txt", "moved.txt")
	staged, err := NewDiffCheck(map[string]any{"source": "staged"})
	if err != nil {
		t.Fatal(err)
	}
	// A pure move must not report the moved file's pre-existing whitespace as newly introduced.
	if _, err := staged.Run(t.Context(), step.Request{RunDir: dir}); err != nil {
		t.Fatalf("staged check rejected a pure rename: %v", err)
	}
	runGitTest(t, dir, "reset", "--hard", "--quiet", legacy)

	runGitTest(t, dir, "checkout", "-q", base)
	runGitTest(t, dir, "checkout", "-qb", "release")
	runGitTest(t, dir, "merge", "-q", "--no-ff", "legacy", "-m", "merge legacy")
	head := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	pushed, err := NewDiffCheck(map[string]any{"source": "pushed"})
	if err != nil {
		t.Fatal(err)
	}
	// Only the merge commit is new: the merged branch is already on the remote. Comparing the
	// merge with its first parent would blame it for the merged branch's whitespace.
	request := step.Request{RunDir: dir, Providers: gitHookProviders([]any{map[string]any{
		"local_ref": "refs/heads/release", "local_oid": head,
		"remote_ref": "refs/heads/release", "remote_oid": strings.Repeat("0", 40),
	}})}
	if _, err := pushed.Run(t.Context(), request); err != nil {
		t.Fatalf("pushed check rejected a merge of already-pushed history: %v", err)
	}
}

func TestGitDiffCheckDetectsWhitespace(t *testing.T) {
	dir := initGitRepository(t)
	writeHistoryFile(t, dir, "bad.txt", "trailing \n")
	runGitTest(t, dir, "add", "bad.txt")
	runner, err := NewDiffCheck(map[string]any{"source": "staged"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{RunDir: dir}); err == nil || !strings.Contains(err.Error(), "bad.txt:1") {
		t.Fatalf("check error = %v", err)
	}
	writeHistoryFile(t, dir, "bad.txt", "clean\n")
	runGitTest(t, dir, "add", "bad.txt")
	if _, err := runner.Run(t.Context(), step.Request{RunDir: dir}); err != nil {
		t.Fatal(err)
	}
}

func TestGitDiffCheckPushedUsesHookContextAndChecksEveryCommit(t *testing.T) {
	dir := initGitRepository(t)
	base := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	writeHistoryFile(t, dir, "temporary.txt", "bad \n")
	runGitTest(t, dir, "add", "temporary.txt")
	runGitTest(t, dir, "commit", "-qm", "introduce whitespace")
	writeHistoryFile(t, dir, "temporary.txt", "clean\n")
	runGitTest(t, dir, "add", "temporary.txt")
	runGitTest(t, dir, "commit", "-qm", "fix whitespace")
	head := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	runner, err := NewDiffCheck(map[string]any{"source": "pushed"})
	if err != nil {
		t.Fatal(err)
	}
	request := step.Request{RunDir: dir, Providers: gitHookProviders([]any{map[string]any{
		"local_ref": "refs/heads/main", "local_oid": head,
		"remote_ref": "refs/heads/main", "remote_oid": base,
	}})}
	if _, err := runner.Run(t.Context(), request); err == nil || !strings.Contains(err.Error(), "temporary.txt:1") {
		t.Fatalf("pushed check error = %v", err)
	}

	request.Providers = gitHookProviders([]any{map[string]any{
		"local_ref": "(delete)", "local_oid": strings.Repeat("0", 64),
		"remote_ref": "refs/heads/old", "remote_oid": base,
	}})
	if _, err := runner.Run(t.Context(), request); err != nil {
		t.Fatalf("deleted ref check: %v", err)
	}
}

func TestGitDiffCheckPushedAcceptsExplicitUpdatesExpression(t *testing.T) {
	runner, err := NewDiffCheck(map[string]any{
		"source": "pushed", "updates": map[string]any{"expr": "vars.updates"},
	})
	if err != nil {
		t.Fatal(err)
	}
	value := []any{map[string]any{
		"local_ref": "(delete)", "local_oid": strings.Repeat("0", 40),
		"remote_ref": "refs/heads/old", "remote_oid": strings.Repeat("a", 40),
	}}
	if _, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"updates": value}}); err != nil {
		t.Fatal(err)
	}
	withoutContext, err := NewDiffCheck(map[string]any{"source": "pushed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withoutContext.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "active Git hook context") {
		t.Fatalf("missing context error = %v", err)
	}
}

type recordingGitExecutor struct {
	calls []process.Options
}

func (executor *recordingGitExecutor) Run(ctx context.Context, options process.Options) (process.Result, error) {
	options.Args = slices.Clone(options.Args)
	executor.calls = append(executor.calls, options)
	return process.LocalExecutor{}.Run(ctx, options)
}

func TestGitDiffCheckPushedRunsOneCommandPerCommit(t *testing.T) {
	dir := initGitRepository(t)
	base := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	for _, content := range []string{"one\n", "two\n", "three\n"} {
		writeHistoryFile(t, dir, "clean.txt", content)
		runGitTest(t, dir, "add", "clean.txt")
		runGitTest(t, dir, "commit", "-qm", "clean commit")
	}
	head := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	runner, err := NewDiffCheck(map[string]any{"source": "pushed"})
	if err != nil {
		t.Fatal(err)
	}
	executor := &recordingGitExecutor{}
	request := step.Request{RunDir: dir, Executor: executor, Providers: gitHookProviders([]any{map[string]any{
		"local_ref": "refs/heads/main", "local_oid": head,
		"remote_ref": "refs/heads/main", "remote_oid": base,
	}})}
	if _, err := runner.Run(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	// Commit ids come from rev-list already resolved, so a pre-push hook over a long branch must
	// spawn one enumeration plus one diff-tree per commit, not re-resolve or re-parent each one.
	if len(executor.calls) != 4 {
		commands := make([][]string, len(executor.calls))
		for index, call := range executor.calls {
			commands[index] = call.Args
		}
		t.Fatalf("git invocations = %d, want 4: %v", len(commands), commands)
	}
	if executor.calls[0].Args[0] != "rev-list" {
		t.Fatalf("enumeration command = %v", executor.calls[0].Args)
	}
	for _, call := range executor.calls[1:] {
		if call.Args[0] != "diff-tree" || !slices.Contains(call.Args, "--check") || !slices.Contains(call.Args, "--root") {
			t.Fatalf("check command = %v", call.Args)
		}
	}
}

func TestGitDiffCheckPushedHandlesNewBranch(t *testing.T) {
	dir := initGitRepository(t)
	base := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	runGitTest(t, dir, "update-ref", "refs/remotes/origin/main", base)
	writeHistoryFile(t, dir, "clean.txt", "clean\n")
	runGitTest(t, dir, "add", "clean.txt")
	runGitTest(t, dir, "commit", "-qm", "clean commit")
	head := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	runner, err := NewDiffCheck(map[string]any{"source": "pushed"})
	if err != nil {
		t.Fatal(err)
	}
	request := step.Request{RunDir: dir, Providers: gitHookProviders([]any{map[string]any{
		"local_ref": "refs/heads/topic", "local_oid": head,
		"remote_ref": "refs/heads/topic", "remote_oid": strings.Repeat("0", 40),
	}})}
	if _, err := runner.Run(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestGitDiffRejectsMalformedAndTruncatedOutput(t *testing.T) {
	for _, test := range []struct {
		name    string
		results []scriptedGitResult
		want    string
	}{
		{"malformed raw", []scriptedGitResult{{result: process.Result{Stdout: "id\n"}}, {result: process.Result{Stdout: "id parent\n"}}, {result: process.Result{Stdout: "bad\x00"}}}, "malformed header"},
		{"truncated raw", []scriptedGitResult{{result: process.Result{Stdout: "id\n"}}, {result: process.Result{Stdout: "id parent\n"}}, {result: process.Result{StdoutTruncated: true}, err: &process.ExitError{Command: "git", Code: 1}}}, "output exceeded 16 MiB"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, err := NewDiff(map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{Executor: &scriptedGitExecutor{results: test.results}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestGitDiffCancellationAndRegistration(t *testing.T) {
	registry := step.NewRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"git_diff", "git_diff_check"} {
		raw := map[string]any{}
		if name == "git_diff_check" {
			raw["source"] = "staged"
		}
		if _, err := registry.Build(name, raw); err != nil {
			t.Fatalf("building %s: %v", name, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, runner := range []step.Runner{mustDiff(t), mustDiffCheck(t)} {
		if _, err := runner.Run(ctx, step.Request{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
		if _, ok := runner.(step.ExecutorAware); !ok {
			t.Fatal("Git diff runner is not executor-aware")
		}
	}
}

func TestGitDiffDocumentationExamples(t *testing.T) {
	paths := []string{"../../docs/steps-system.md", "../../docs/git-changes.md"}
	built := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(path, "steps-system.md") {
			start := strings.Index(string(data), "## `git_diff`")
			end := strings.Index(string(data), "## `git_conventional_commit`")
			if start < 0 || end <= start {
				t.Fatalf("Git diff documentation section not found in %s", path)
			}
			data = data[start:end]
		}
		for blockIndex, block := range regexpYAMLBlocks(data) {
			var value any
			if err := yaml.Unmarshal(block, &value); err != nil {
				t.Fatalf("%s example %d: %v", path, blockIndex+1, err)
			}
			walkDocumentedSteps(t, path, blockIndex+1, value, &built)
		}
	}
	if built < 14 {
		t.Fatalf("built %d documented Git diff steps, want at least 14", built)
	}
}

func walkDocumentedSteps(t *testing.T, path string, block int, value any, built *int) {
	t.Helper()
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			walkDocumentedSteps(t, path, block, item, built)
		}
	case map[string]any:
		compileDocumentedExpressions(t, path, block, typed)
		kind, _ := typed["type"].(string)
		if kind == "git_diff" || kind == "git_diff_check" {
			raw, _ := typed["with"].(map[string]any)
			var err error
			if kind == "git_diff" {
				_, err = NewDiff(raw)
			} else {
				_, err = NewDiffCheck(raw)
			}
			if err != nil {
				t.Fatalf("%s example %d %s: %v", path, block, kind, err)
			}
			*built++
		}
		for _, child := range typed {
			walkDocumentedSteps(t, path, block, child, built)
		}
	}
}

func compileDocumentedExpressions(t *testing.T, path string, block int, value map[string]any) {
	t.Helper()
	compile := func(label, source string, asBool bool) {
		options := []expr.Option{expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables()}
		if asBool {
			options = append(options, expr.AsBool())
		}
		if _, err := wukoexpr.Compile(source, options...); err != nil {
			t.Fatalf("%s example %d %s expression: %v", path, block, label, err)
		}
	}
	if condition, ok := value["if"].(string); ok {
		compile("if", condition, true)
	}
	if value["type"] == "assert" {
		if with, ok := value["with"].(map[string]any); ok {
			if source, ok := with["expr"].(string); ok {
				compile("assert", source, true)
			}
		}
	}
	if foreach, ok := value["foreach"].(map[string]any); ok {
		if source, ok := foreach["items"].(string); ok {
			compile("foreach items", source, false)
		}
		if source, ok := foreach["collect"].(string); ok {
			compile("foreach collect", source, false)
		}
	}
}

func runDiff(t *testing.T, dir string, raw map[string]any) map[string]any {
	t.Helper()
	runner, err := NewDiff(raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return result.Outputs
}

func diffPaths(outputs map[string]any) []string {
	files := outputs["files"].([]any)
	paths := make([]string, len(files))
	for index, file := range files {
		paths[index] = file.(map[string]any)["path"].(string)
	}
	return paths
}

func findDiffFile(t *testing.T, files []any, path string) map[string]any {
	t.Helper()
	for _, item := range files {
		file := item.(map[string]any)
		if file["path"] == path {
			return file
		}
	}
	t.Fatalf("diff file %q not found in %#v", path, files)
	return nil
}

func gitHookProviders(updates []any) provider.Set {
	return provider.Set{Values: map[string]map[string]any{"git": {
		"hook": map[string]any{"name": "pre-push", "payload": map[string]any{"updates": updates}},
	}}}
}

func mustDiff(t *testing.T) step.Runner {
	t.Helper()
	runner, err := NewDiff(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func mustDiffCheck(t *testing.T) step.Runner {
	t.Helper()
	runner, err := NewDiffCheck(map[string]any{"source": "staged"})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}
