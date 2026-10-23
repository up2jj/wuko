package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/gittest"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
)

func TestNewWorktreeValidatesOperationSpecificConfiguration(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	items, err := parseWorktreePorcelain("worktree /repo\x00HEAD 1234567890abcdef\x00branch refs/heads/main\x00\x00worktree /repo.topic\x00HEAD fedcba\x00detached\x00locked busy\x00\x00")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Branch != "main" || items[1].Path != "/repo.topic" || !items[1].Detached || !items[1].Locked || items[1].LockReason != "busy" {
		t.Fatalf("items = %#v", items)
	}
}

func TestChangeSummaryHandlesRenameDestinationRecords(t *testing.T) {
	t.Parallel()
	summary := changeSummary("R  old.txt\x00new.txt\x00 D deleted.txt\x00?? new.txt\x00UU conflict.txt\x00")
	want := map[string]int{"total": 4, "renamed": 1, "deleted": 1, "untracked": 1, "conflicted": 1, "staged": 2, "modified": 2}
	for field, value := range want {
		if summary[field] != value {
			t.Errorf("%s = %v, want %d (summary %#v)", field, summary[field], value, summary)
		}
	}
}

func TestWorktreeEnsureListMergeAndSafeRemove(t *testing.T) {
	t.Parallel()
	dir := gittest.InitWithCommit(t)
	base := gittest.Output(t, dir, "branch", "--show-current")
	runner := steptest.Build(t, NewWorktree, map[string]any{"operation": "ensure", "branch": "feature/worktree", "base": base, "create": true})
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
	testfs.WriteFile(t, filepath.Join(path, "feature.txt"), []byte("feature\n"), 0o644)
	gittest.Run(t, path, "add", "feature.txt")
	gittest.Run(t, path, "commit", "--quiet", "-m", "feature")

	list := steptest.Build(t, NewWorktree, map[string]any{"operation": "list", "comparison": base})
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

	remove := steptest.Build(t, NewWorktree, map[string]any{"operation": "remove", "target": "feature/worktree", "delete_branch": "safe", "integrated_into": base})
	removed, err := remove.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if removed.Outputs["branch_deleted"] != false || removed.Outputs["branch_delete_reason"] != "unintegrated" {
		t.Fatalf("unintegrated removal = %#v", removed.Outputs)
	}

	merge := steptest.Build(t, NewMerge, map[string]any{"target": base, "source": "feature/worktree", "mode": "ff_only"})
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
	if got := gittest.Output(t, dir, "branch", "--list", "feature/worktree"); got != "" {
		t.Fatalf("branch still exists: %q", got)
	}
}

func newTestWorktreeRemoval(t *testing.T, path string) step.Runner {
	t.Helper()
	runner := steptest.Build(t, NewWorktree, map[string]any{"operation": "remove", "target": path, "force": true, "delete_branch": "never"})
	return runner
}

func TestSquashCreatesOneCommitAndRecoveryRef(t *testing.T) {
	t.Parallel()
	dir := gittest.InitWithCommit(t)
	baseBranch := gittest.Output(t, dir, "branch", "--show-current")
	base := gittest.Output(t, dir, "rev-parse", "HEAD")
	gittest.Run(t, dir, "switch", "-c", "topic")
	testfs.WriteFile(t, filepath.Join(dir, "one.txt"), []byte("one\n"), 0o644)
	gittest.Run(t, dir, "add", "one.txt")
	gittest.Run(t, dir, "commit", "--quiet", "-m", "one")
	oldHead := gittest.Output(t, dir, "rev-parse", "HEAD")
	testfs.WriteFile(t, filepath.Join(dir, "two.txt"), []byte("two\n"), 0o644)
	runner := steptest.Build(t, NewSquash, map[string]any{"target": baseBranch, "message": "feat: squashed", "stage": "all"})
	result, err := runner.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["old_head"] != oldHead || result.Outputs["base"] != base {
		t.Fatalf("squash result = %#v", result.Outputs)
	}
	if count := gittest.Output(t, dir, "rev-list", "--count", baseBranch+"..topic"); count != "1" {
		t.Fatalf("commit count = %q", count)
	}
	if tree := gittest.Output(t, dir, "ls-tree", "-r", "--name-only", "HEAD"); !strings.Contains(tree, "one.txt") || !strings.Contains(tree, "two.txt") {
		t.Fatalf("squash tree = %q", tree)
	}
	backup := result.Outputs["backup_ref"].(string)
	if got := gittest.Output(t, dir, "rev-parse", backup); got != oldHead {
		t.Fatalf("backup ref = %q, want %q", got, oldHead)
	}
}

func TestRebaseReplaysOntoTargetAndLeavesConflictPolicyToGit(t *testing.T) {
	t.Parallel()
	dir := gittest.InitWithCommit(t)
	baseBranch := gittest.Output(t, dir, "branch", "--show-current")
	worktree := filepath.Join(t.TempDir(), "topic")
	gittest.Run(t, dir, "worktree", "add", "-b", "topic", worktree)
	t.Cleanup(func() { gittest.Run(t, dir, "worktree", "remove", "--force", worktree) })
	testfs.WriteFile(t, filepath.Join(worktree, "topic.txt"), []byte("topic\n"), 0o644)
	gittest.Run(t, worktree, "add", "topic.txt")
	gittest.Run(t, worktree, "commit", "--quiet", "-m", "topic")
	testfs.WriteFile(t, filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644)
	gittest.Run(t, dir, "add", "base.txt")
	gittest.Run(t, dir, "commit", "--quiet", "-m", "base")
	targetHead := gittest.Output(t, dir, "rev-parse", "HEAD")
	runner := steptest.Build(t, NewRebase, map[string]any{"target": baseBranch})
	result, err := runner.Run(t.Context(), step.Request{RunDir: worktree})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["rebased"] != true {
		t.Fatalf("rebase result = %#v", result.Outputs)
	}
	if parent := gittest.Output(t, worktree, "rev-parse", "HEAD^"); parent != targetHead {
		t.Fatalf("rebased parent = %q, want %q", parent, targetHead)
	}
}

func TestNoFFMergeUsesAndCleansTemporaryTargetWorktree(t *testing.T) {
	t.Parallel()
	dir := gittest.InitWithCommit(t)
	gittest.Run(t, dir, "branch", "release")
	testfs.WriteFile(t, filepath.Join(dir, "feature.txt"), []byte("feature\n"), 0o644)
	gittest.Run(t, dir, "add", "feature.txt")
	gittest.Run(t, dir, "commit", "--quiet", "-m", "feature")
	source := gittest.Output(t, dir, "branch", "--show-current")
	runner := steptest.Build(t, NewMerge, map[string]any{"target": "release", "source": source, "mode": "no_ff", "message": "merge feature"})
	result, err := runner.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["merge_commit"] != true || result.Outputs["changed"] != true {
		t.Fatalf("merge result = %#v", result.Outputs)
	}
	if parents := strings.Fields(gittest.Output(t, dir, "show", "-s", "--format=%P", "release")); len(parents) != 2 {
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
	t.Parallel()
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
	t.Parallel()
	dir := gittest.InitWithCommit(t)
	head := gittest.Output(t, dir, "rev-parse", "HEAD")
	gittest.Run(t, dir, "update-ref", "refs/remotes/origin/feature/example", head)
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
	t.Parallel()
	dir := gittest.InitWithCommit(t)
	base := gittest.Output(t, dir, "branch", "--show-current")
	path := filepath.Join(t.TempDir(), "topic")
	gittest.Run(t, dir, "worktree", "add", "-b", "topic", path)
	testfs.WriteFile(t, filepath.Join(path, "topic.txt"), []byte("topic\n"), 0o644)
	gittest.Run(t, path, "add", "topic.txt")
	gittest.Run(t, path, "commit", "--quiet", "-m", "topic")
	gittest.Run(t, dir, "tag", "topic", base)
	remove := steptest.Build(t, NewWorktree, map[string]any{"operation": "remove", "target": "topic", "delete_branch": "safe", "integrated_into": base})
	removed, err := remove.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if removed.Outputs["branch_deleted"] != false || removed.Outputs["branch_delete_reason"] != "unintegrated" {
		t.Fatalf("removal = %#v", removed.Outputs)
	}
	if got := gittest.Output(t, dir, "branch", "--list", "topic"); got == "" {
		t.Fatal("unintegrated branch shadowed by a tag was deleted")
	}
}

func TestMergeResolvesTheDefaultSourceInTheCallingWorktree(t *testing.T) {
	t.Parallel()
	dir := gittest.InitWithCommit(t)
	gittest.Run(t, dir, "branch", "release")
	path := filepath.Join(t.TempDir(), "detached")
	gittest.Run(t, dir, "worktree", "add", "--detach", path)
	t.Cleanup(func() { gittest.Run(t, dir, "worktree", "remove", "--force", path) })
	testfs.WriteFile(t, filepath.Join(path, "detached.txt"), []byte("detached\n"), 0o644)
	gittest.Run(t, path, "add", "detached.txt")
	gittest.Run(t, path, "commit", "--quiet", "-m", "detached work")
	want := gittest.Output(t, path, "rev-parse", "HEAD")
	runner := steptest.Build(t, NewMerge, map[string]any{"target": "release", "mode": "no_ff", "message": "merge detached"})
	result, err := runner.Run(t.Context(), step.Request{RunDir: path})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["merge_commit"] != true || result.Outputs["changed"] != true {
		t.Fatalf("merge result = %#v", result.Outputs)
	}
	if got := gittest.Output(t, dir, "rev-parse", "release^2"); got != want {
		t.Fatalf("merged parent = %q, want the calling worktree HEAD %q", got, want)
	}
}

func TestSquashLeavesTheBranchUntouchedWhenThereIsNothingToSquash(t *testing.T) {
	t.Parallel()
	dir := gittest.InitWithCommit(t)
	base := gittest.Output(t, dir, "branch", "--show-current")
	gittest.Run(t, dir, "switch", "--quiet", "-c", "topic")
	testfs.WriteFile(t, filepath.Join(dir, "temp.txt"), []byte("temp\n"), 0o644)
	gittest.Run(t, dir, "add", "temp.txt")
	gittest.Run(t, dir, "commit", "--quiet", "-m", "add")
	gittest.Run(t, dir, "rm", "--quiet", "temp.txt")
	gittest.Run(t, dir, "commit", "--quiet", "-m", "remove")
	head := gittest.Output(t, dir, "rev-parse", "HEAD")
	runner := steptest.Build(t, NewSquash, map[string]any{"target": base, "message": "feat: nothing"})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: dir}); err == nil || !strings.Contains(err.Error(), "no net changes") {
		t.Fatalf("squash error = %v", err)
	}
	if got := gittest.Output(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %q, want the untouched branch tip %q", got, head)
	}
}

// git worktree add accepts options among its arguments, so a base such as --force would be
// taken as a flag and the branch silently created from HEAD.
func TestWorktreeEnsureRejectsOptionLikeBase(t *testing.T) {
	t.Parallel()
	dir := gittest.InitWithCommit(t)
	runner := steptest.Build(t, NewWorktree, map[string]any{"operation": "ensure", "branch": "feature/option", "base": "--force", "create": true})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: dir}); err == nil || !strings.Contains(err.Error(), "base") {
		t.Fatalf("Run() error = %v, want an option-like base rejected", err)
	}
	if branches := gittest.Output(t, dir, "branch", "--list", "feature/option"); strings.TrimSpace(branches) != "" {
		t.Fatalf("branch was created from an option-like base: %q", branches)
	}
}
