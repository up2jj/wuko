package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestNewWorktreeValidatesOperationSpecificConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "missing operation", raw: map[string]any{}, want: "operation is required"},
		{name: "unknown operation", raw: map[string]any{"operation": "move"}, want: "list, ensure, or remove"},
		{name: "ensure branch", raw: map[string]any{"operation": "ensure"}, want: "branch is required"},
		{name: "remove target", raw: map[string]any{"operation": "remove"}, want: "target is required"},
		{name: "bad deletion", raw: map[string]any{"operation": "remove", "target": "topic", "delete_branch": "yes"}, want: "safe, never, or force"},
		{name: "list rejects mutation", raw: map[string]any{"operation": "list", "force": true}, want: "force is not valid"},
		{name: "unknown field", raw: map[string]any{"operation": "list", "unknown": true}, want: "field unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewWorktree(tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewWorktree(%#v) error = %v, want %q", tt.raw, err, tt.want)
			}
		})
	}
}

func TestParseWorktreePorcelain(t *testing.T) {
	items, err := parseWorktreePorcelain("worktree /repo\x00HEAD 1234567890abcdef\x00branch refs/heads/main\x00\x00worktree /repo.topic\x00HEAD fedcba\x00detached\x00locked busy\x00\x00")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Branch != "main" || items[1].Path != "/repo.topic" || !items[1].Detached || !items[1].Locked || items[1].LockReason != "busy" {
		t.Fatalf("items = %#v", items)
	}
}

func TestChangeSummaryHandlesRenameDestinationRecords(t *testing.T) {
	summary := changeSummary("R  old.txt\x00new.txt\x00 D deleted.txt\x00?? new.txt\x00UU conflict.txt\x00")
	want := map[string]int{"total": 4, "renamed": 1, "deleted": 1, "untracked": 1, "conflicted": 1, "staged": 2, "modified": 2}
	for field, value := range want {
		if summary[field] != value {
			t.Errorf("%s = %v, want %d (summary %#v)", field, summary[field], value, summary)
		}
	}
}

func TestWorktreeEnsureListMergeAndSafeRemove(t *testing.T) {
	dir := initGitRepository(t)
	base := strings.TrimSpace(runGitTest(t, dir, "branch", "--show-current"))
	runner, err := NewWorktree(map[string]any{"operation": "ensure", "branch": "feature/worktree", "base": base, "create": true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	worktree := result.Outputs["worktree"].(map[string]any)
	path := worktree["path"].(string)
	t.Cleanup(func() {
		if _, err := os.Stat(path); err == nil {
			command := newTestWorktreeRemoval(t, path)
			_, _ = command.Run(t.Context(), step.Request{RunDir: dir})
		}
	})
	if result.Outputs["created"] != true || result.Outputs["branch_created"] != true {
		t.Fatalf("ensure result = %#v", result.Outputs)
	}
	result, err = runner.Run(t.Context(), step.Request{RunDir: path})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["created"] != false {
		t.Fatalf("second ensure result = %#v", result.Outputs)
	}
	if err := os.WriteFile(filepath.Join(path, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, path, "add", "feature.txt")
	runGitTest(t, path, "commit", "--quiet", "-m", "feature")

	list, err := NewWorktree(map[string]any{"operation": "list", "comparison": base})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := list.Run(t.Context(), step.Request{RunDir: path})
	if err != nil {
		t.Fatal(err)
	}
	items := listed.Outputs["items"].([]any)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || listed.Outputs["repository_root"] != realDir {
		t.Fatalf("list result = %#v", listed.Outputs)
	}
	feature := items[1].(map[string]any)
	if feature["branch"] != "feature/worktree" || feature["ahead"] != 1 || feature["clean"] != true || feature["current"] != true {
		t.Fatalf("feature item = %#v", feature)
	}

	remove, err := NewWorktree(map[string]any{"operation": "remove", "target": "feature/worktree", "delete_branch": "safe", "integrated_into": base})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := remove.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if removed.Outputs["branch_deleted"] != false || removed.Outputs["branch_delete_reason"] != "unintegrated" {
		t.Fatalf("unintegrated removal = %#v", removed.Outputs)
	}

	merge, err := NewMerge(map[string]any{"target": base, "source": "feature/worktree", "mode": "ff_only"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := merge.Run(t.Context(), step.Request{RunDir: dir}); err != nil {
		t.Fatal(err)
	}
	result, err = runner.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	path = result.Outputs["worktree"].(map[string]any)["path"].(string)
	removed, err = remove.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if removed.Outputs["branch_deleted"] != true || removed.Outputs["branch_delete_reason"] != "same_commit" {
		t.Fatalf("integrated removal = %#v", removed.Outputs)
	}
	if got := strings.TrimSpace(runGitTest(t, dir, "branch", "--list", "feature/worktree")); got != "" {
		t.Fatalf("branch still exists: %q", got)
	}
}

func newTestWorktreeRemoval(t *testing.T, path string) step.Runner {
	t.Helper()
	runner, err := NewWorktree(map[string]any{"operation": "remove", "target": path, "force": true, "delete_branch": "never"})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestSquashCreatesOneCommitAndRecoveryRef(t *testing.T) {
	dir := initGitRepository(t)
	baseBranch := strings.TrimSpace(runGitTest(t, dir, "branch", "--show-current"))
	base := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	runGitTest(t, dir, "switch", "-c", "topic")
	if err := os.WriteFile(filepath.Join(dir, "one.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "one.txt")
	runGitTest(t, dir, "commit", "--quiet", "-m", "one")
	oldHead := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(dir, "two.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner, err := NewSquash(map[string]any{"target": baseBranch, "message": "feat: squashed", "stage": "all"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["old_head"] != oldHead || result.Outputs["base"] != base {
		t.Fatalf("squash result = %#v", result.Outputs)
	}
	if count := strings.TrimSpace(runGitTest(t, dir, "rev-list", "--count", baseBranch+"..topic")); count != "1" {
		t.Fatalf("commit count = %q", count)
	}
	if tree := strings.TrimSpace(runGitTest(t, dir, "ls-tree", "-r", "--name-only", "HEAD")); !strings.Contains(tree, "one.txt") || !strings.Contains(tree, "two.txt") {
		t.Fatalf("squash tree = %q", tree)
	}
	backup := result.Outputs["backup_ref"].(string)
	if got := strings.TrimSpace(runGitTest(t, dir, "rev-parse", backup)); got != oldHead {
		t.Fatalf("backup ref = %q, want %q", got, oldHead)
	}
}

func TestRebaseReplaysOntoTargetAndLeavesConflictPolicyToGit(t *testing.T) {
	dir := initGitRepository(t)
	baseBranch := strings.TrimSpace(runGitTest(t, dir, "branch", "--show-current"))
	worktree := filepath.Join(t.TempDir(), "topic")
	runGitTest(t, dir, "worktree", "add", "-b", "topic", worktree)
	t.Cleanup(func() { runGitTest(t, dir, "worktree", "remove", "--force", worktree) })
	if err := os.WriteFile(filepath.Join(worktree, "topic.txt"), []byte("topic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, worktree, "add", "topic.txt")
	runGitTest(t, worktree, "commit", "--quiet", "-m", "topic")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "base.txt")
	runGitTest(t, dir, "commit", "--quiet", "-m", "base")
	targetHead := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	runner, err := NewRebase(map[string]any{"target": baseBranch})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{RunDir: worktree})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["rebased"] != true {
		t.Fatalf("rebase result = %#v", result.Outputs)
	}
	if parent := strings.TrimSpace(runGitTest(t, worktree, "rev-parse", "HEAD^")); parent != targetHead {
		t.Fatalf("rebased parent = %q, want %q", parent, targetHead)
	}
}

func TestNoFFMergeUsesAndCleansTemporaryTargetWorktree(t *testing.T) {
	dir := initGitRepository(t)
	runGitTest(t, dir, "branch", "release")
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "feature.txt")
	runGitTest(t, dir, "commit", "--quiet", "-m", "feature")
	source := strings.TrimSpace(runGitTest(t, dir, "branch", "--show-current"))
	runner, err := NewMerge(map[string]any{"target": "release", "source": source, "mode": "no_ff", "message": "merge feature"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["merge_commit"] != true || result.Outputs["changed"] != true {
		t.Fatalf("merge result = %#v", result.Outputs)
	}
	if parents := strings.Fields(strings.TrimSpace(runGitTest(t, dir, "show", "-s", "--format=%P", "release"))); len(parents) != 2 {
		t.Fatalf("release parents = %#v", parents)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(dir), ".wuko-merge-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary worktrees remain: %#v", matches)
	}
}

func TestLifecycleRunnersAreHostLocal(t *testing.T) {
	for name, constructor := range map[string]func(map[string]any) (step.Runner, error){
		"worktree": func(raw map[string]any) (step.Runner, error) { return NewWorktree(raw) },
		"squash":   func(raw map[string]any) (step.Runner, error) { return NewSquash(raw) },
		"rebase":   func(raw map[string]any) (step.Runner, error) { return NewRebase(raw) },
		"merge":    func(raw map[string]any) (step.Runner, error) { return NewMerge(raw) },
	} {
		t.Run(name, func(t *testing.T) {
			raw := map[string]any{}
			switch name {
			case "worktree":
				raw = map[string]any{"operation": "list"}
			case "squash":
				raw = map[string]any{"message": "subject"}
			}
			runner, err := constructor(raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := runner.(step.ExecutorAware); ok {
				t.Fatalf("%s runner is executor-aware", name)
			}
		})
	}
}

func TestMatchingRemoteBranchesRequiresAnExactBranchSegment(t *testing.T) {
	dir := initGitRepository(t)
	head := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	runGitTest(t, dir, "update-ref", "refs/remotes/origin/feature/example", head)
	for branch, want := range map[string][]string{
		"example":         nil,
		"feature/example": {"origin/feature/example"},
	} {
		got, err := matchingRemoteBranches(t.Context(), step.Request{RunDir: dir}, branch)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) || (len(want) == 1 && got[0] != want[0]) {
			t.Errorf("matchingRemoteBranches(%q) = %#v, want %#v", branch, got, want)
		}
	}
}

func TestSafeRemovalIgnoresATagThatShadowsTheBranchName(t *testing.T) {
	dir := initGitRepository(t)
	base := strings.TrimSpace(runGitTest(t, dir, "branch", "--show-current"))
	path := filepath.Join(t.TempDir(), "topic")
	runGitTest(t, dir, "worktree", "add", "-b", "topic", path)
	if err := os.WriteFile(filepath.Join(path, "topic.txt"), []byte("topic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, path, "add", "topic.txt")
	runGitTest(t, path, "commit", "--quiet", "-m", "topic")
	runGitTest(t, dir, "tag", "topic", base)
	remove, err := NewWorktree(map[string]any{"operation": "remove", "target": "topic", "delete_branch": "safe", "integrated_into": base})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := remove.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if removed.Outputs["branch_deleted"] != false || removed.Outputs["branch_delete_reason"] != "unintegrated" {
		t.Fatalf("removal = %#v", removed.Outputs)
	}
	if got := strings.TrimSpace(runGitTest(t, dir, "branch", "--list", "topic")); got == "" {
		t.Fatal("unintegrated branch shadowed by a tag was deleted")
	}
}

func TestMergeResolvesTheDefaultSourceInTheCallingWorktree(t *testing.T) {
	dir := initGitRepository(t)
	runGitTest(t, dir, "branch", "release")
	path := filepath.Join(t.TempDir(), "detached")
	runGitTest(t, dir, "worktree", "add", "--detach", path)
	t.Cleanup(func() { runGitTest(t, dir, "worktree", "remove", "--force", path) })
	if err := os.WriteFile(filepath.Join(path, "detached.txt"), []byte("detached\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, path, "add", "detached.txt")
	runGitTest(t, path, "commit", "--quiet", "-m", "detached work")
	want := strings.TrimSpace(runGitTest(t, path, "rev-parse", "HEAD"))
	runner, err := NewMerge(map[string]any{"target": "release", "mode": "no_ff", "message": "merge detached"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{RunDir: path})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["merge_commit"] != true || result.Outputs["changed"] != true {
		t.Fatalf("merge result = %#v", result.Outputs)
	}
	if got := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "release^2")); got != want {
		t.Fatalf("merged parent = %q, want the calling worktree HEAD %q", got, want)
	}
}

func TestSquashLeavesTheBranchUntouchedWhenThereIsNothingToSquash(t *testing.T) {
	dir := initGitRepository(t)
	base := strings.TrimSpace(runGitTest(t, dir, "branch", "--show-current"))
	runGitTest(t, dir, "switch", "--quiet", "-c", "topic")
	if err := os.WriteFile(filepath.Join(dir, "temp.txt"), []byte("temp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "temp.txt")
	runGitTest(t, dir, "commit", "--quiet", "-m", "add")
	runGitTest(t, dir, "rm", "--quiet", "temp.txt")
	runGitTest(t, dir, "commit", "--quiet", "-m", "remove")
	head := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD"))
	runner, err := NewSquash(map[string]any{"target": base, "message": "feat: nothing"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{RunDir: dir}); err == nil || !strings.Contains(err.Error(), "no net changes") {
		t.Fatalf("squash error = %v", err)
	}
	if got := strings.TrimSpace(runGitTest(t, dir, "rev-parse", "HEAD")); got != head {
		t.Fatalf("HEAD = %q, want the untouched branch tip %q", got, head)
	}
}
