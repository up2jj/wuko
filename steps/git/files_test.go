package git

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/gittest"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
)

func TestNewFilesValidatesConfiguration(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"missing source", map[string]any{}, "source is required"},
		{"unknown source", map[string]any{"source": "commit"}, "staged, unstaged, pushed, or all"},
		{"updates on staged", map[string]any{"source": "staged", "updates": map[string]any{"expr": "vars.updates"}}, "only supported with source pushed"},
		{"invalid updates", map[string]any{"source": "pushed", "updates": map[string]any{"expr": "("}}, "compiling updates expr"},
		{"empty paths", map[string]any{"source": "all", "paths": []any{}}, "at least one pathspec"},
		{"blank exclude", map[string]any{"source": "all", "exclude": []any{" "}}, "exclude[0] must not be blank"},
		{"invalid exclude", map[string]any{"source": "all", "exclude": []any{"^dist/", "("}}, "exclude[1]: invalid pattern"},
		{"unknown field", map[string]any{"source": "all", "classify": true}, "field classify"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewFiles(test.raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewFiles(%#v) error = %v, want %q", test.raw, err, test.want)
			}
		})
	}
	for _, raw := range []map[string]any{
		{"source": "staged"}, {"source": "unstaged", "paths": []any{"src"}}, {"source": "all", "exclude": []any{"{{ .vars.pattern }}"}},
		{"source": "pushed", "updates": map[string]any{"expr": "vars.updates"}},
		{"source": "{{ .vars.source }}", "updates": map[string]any{"expr": "vars.updates"}},
	} {
		if _, err := NewFiles(raw); err != nil {
			t.Fatalf("NewFiles(%#v): %v", raw, err)
		}
	}
}

func TestGitFilesChecksATemplatedSourceOnceRendered(t *testing.T) {
	t.Parallel()
	dir := gittest.InitWithCommit(t)
	if _, err := steptest.Build(t, NewFiles, map[string]any{"source": "{{ .vars.source }}"}).Run(t.Context(), step.Request{RunDir: dir}); err == nil || !strings.Contains(err.Error(), "unresolved template") {
		t.Fatalf("unrendered source error = %v", err)
	}
	if _, err := NewFiles(map[string]any{"source": "commit"}); err == nil || !strings.Contains(err.Error(), "staged, unstaged, pushed, or all") {
		t.Fatalf("rendered invalid source error = %v", err)
	}
}

func TestGitFilesStagedSelectsRegularFilesAndClassifiesContent(t *testing.T) {
	t.Parallel()
	dir := gittest.InitWithCommit(t)
	testfs.WriteTree(t, dir, map[string]string{"modified.txt": "old\n", "removed.txt": "gone\n", "untracked-later.txt": "kept\n"})
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-qm", "baseline")
	testfs.WriteTree(t, dir, map[string]string{
		"modified.txt": "new\n", "name with\nnewline.go": "package main\n", "dist/generated.go": "package dist\n",
		"invalid.dat": "\xff\xfe", "nul.dat": "a\x00b", "untracked.txt": "never staged\n",
	})
	if err := os.Symlink("modified.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, dir, "add", "modified.txt", "name with\nnewline.go", "dist/generated.go", "invalid.dat", "nul.dat", "link")
	gittest.Run(t, dir, "rm", "-q", "removed.txt")
	// Removed from the index but still on disk: the change deletes it, so it is not checked.
	gittest.Run(t, dir, "rm", "-q", "--cached", "untracked-later.txt")
	runner := steptest.Build(t, NewFiles, map[string]any{"source": "staged", "exclude": []any{"^dist/"}})
	result, err := runner.Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"source": "staged", "count": 4,
		"files":   []any{"invalid.dat", "modified.txt", "name with\nnewline.go", "nul.dat"},
		"text":    []any{"modified.txt", "name with\nnewline.go"},
		"binary":  []any{"invalid.dat", "nul.dat"},
		"added":   []any{"invalid.dat", "name with\nnewline.go", "nul.dat"},
		"deleted": []any{"removed.txt", "untracked-later.txt"},
	}
	if diff := cmp.Diff(want, result.Outputs); diff != "" {
		t.Fatalf("outputs mismatch (-want +got):\n%s", diff)
	}
}

func TestGitFilesUnstagedAndAllAreRelativeToTheRunDirectory(t *testing.T) {
	t.Parallel()
	dir := gittest.InitWithCommit(t)
	testfs.WriteTree(t, dir, map[string]string{"src/a.go": "package src\n", "src/b.go": "package src\n", "top.txt": "top\n"})
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-qm", "baseline")
	testfs.WriteTree(t, dir, map[string]string{"src/a.go": "package src // edited\n", "top.txt": "edited\n", "src/new.go": "package src\n"})
	src := filepath.Join(dir, "src")
	for source, want := range map[string][]any{"unstaged": {"a.go"}, "all": {"a.go", "b.go"}} {
		result, err := steptest.Build(t, NewFiles, map[string]any{"source": source}).Run(t.Context(), step.Request{RunDir: src})
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(want, result.Outputs["files"]); diff != "" {
			t.Errorf("%s files mismatch (-want +got):\n%s", source, diff)
		}
		if diff := cmp.Diff([]any{}, result.Outputs["added"]); diff != "" {
			t.Errorf("%s added mismatch (-want +got):\n%s", source, diff)
		}
	}
	result, err := steptest.Build(t, NewFiles, map[string]any{"source": "all", "paths": []any{"*.txt"}}).Run(t.Context(), step.Request{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]any{"top.txt"}, result.Outputs["files"]); diff != "" {
		t.Fatalf("pathspec files mismatch (-want +got):\n%s", diff)
	}
}

func TestGitFilesPushedFollowsHookUpdates(t *testing.T) {
	t.Parallel()
	dir := gittest.InitWithCommit(t)
	base := gittest.Output(t, dir, "rev-parse", "HEAD")
	testfs.WriteTree(t, dir, map[string]string{"kept.txt": "one\n", "temporary.txt": "short-lived\n"})
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-qm", "add files")
	gittest.Run(t, dir, "rm", "-q", "temporary.txt", "README.md")
	gittest.Run(t, dir, "commit", "-qm", "remove files")
	head := gittest.Output(t, dir, "rev-parse", "HEAD")
	zero := strings.Repeat("0", len(head))
	update := func(local, remote string) []any {
		return []any{map[string]any{"local_ref": "refs/heads/main", "local_oid": local, "remote_ref": "refs/heads/main", "remote_oid": remote}}
	}
	run := func(updates []any) map[string]any {
		t.Helper()
		result, err := steptest.Build(t, NewFiles, map[string]any{"source": "pushed"}).Run(t.Context(), step.Request{RunDir: dir, Providers: gitHookProviders(updates)})
		if err != nil {
			t.Fatal(err)
		}
		return result.Outputs
	}
	outputs := run(update(head, base))
	for key, want := range map[string][]any{"files": {"kept.txt"}, "added": {"kept.txt"}, "deleted": {"README.md", "temporary.txt"}} {
		if diff := cmp.Diff(want, outputs[key]); diff != "" {
			t.Errorf("existing ref %s mismatch (-want +got):\n%s", key, diff)
		}
	}
	// A new branch sends only what no remote-tracking ref already has.
	gittest.Run(t, dir, "update-ref", "refs/remotes/origin/main", base)
	if diff := cmp.Diff([]any{"kept.txt"}, run(update(head, zero))["files"]); diff != "" {
		t.Errorf("new branch files mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]any{}, run(update(zero, head))["files"]); diff != "" {
		t.Errorf("deleted ref files mismatch (-want +got):\n%s", diff)
	}
	explicit := steptest.Build(t, NewFiles, map[string]any{"source": "pushed", "updates": map[string]any{"expr": "vars.updates"}})
	result, err := explicit.Run(t.Context(), step.Request{RunDir: dir, Vars: map[string]any{"updates": update(head, base)}})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]any{"kept.txt"}, result.Outputs["files"]); diff != "" {
		t.Errorf("explicit updates files mismatch (-want +got):\n%s", diff)
	}
}

func TestGitFilesPushedOutsideAHookUsesTheCurrentBranch(t *testing.T) {
	t.Parallel()
	pushed := func(dir string) []any {
		t.Helper()
		result, err := steptest.Build(t, NewFiles, map[string]any{"source": "pushed"}).Run(t.Context(), step.Request{RunDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		return result.Outputs["files"].([]any)
	}
	dir := gittest.InitWithCommit(t)
	// Without an upstream or any remote-tracking ref, the branch is new and sends everything.
	testfs.Write(t, filepath.Join(dir, "first.txt"), "one\n")
	gittest.Run(t, dir, "add", "first.txt")
	gittest.Run(t, dir, "commit", "-qm", "first")
	if diff := cmp.Diff([]any{"README.md", "first.txt"}, pushed(dir)); diff != "" {
		t.Errorf("new branch mismatch (-want +got):\n%s", diff)
	}
	trackUpstream(t, dir)
	if diff := cmp.Diff([]any{}, pushed(dir)); diff != "" {
		t.Errorf("up-to-date branch mismatch (-want +got):\n%s", diff)
	}
	testfs.Write(t, filepath.Join(dir, "second.txt"), "two\n")
	gittest.Run(t, dir, "add", "second.txt")
	gittest.Run(t, dir, "commit", "-qm", "second")
	if diff := cmp.Diff([]any{"second.txt"}, pushed(dir)); diff != "" {
		t.Errorf("ahead of upstream mismatch (-want +got):\n%s", diff)
	}
	// Detached HEAD has no upstream, so it sends what no remote-tracking ref has.
	gittest.Run(t, dir, "checkout", "-q", "--detach")
	if diff := cmp.Diff([]any{"second.txt"}, pushed(dir)); diff != "" {
		t.Errorf("detached HEAD mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]any{}, pushed(gittest.Init(t))); diff != "" {
		t.Errorf("repository without commits mismatch (-want +got):\n%s", diff)
	}
}

func TestMergeNameStatusKeepsAPathAnotherRefStillHas(t *testing.T) {
	t.Parallel()
	changes := make(map[string]fileChange)
	if err := mergeNameStatus(changes, "\x01\x00\nD\x00shared.txt\x00\x01\x00\nA\x00shared.txt\x00"); err != nil {
		t.Fatal(err)
	}
	if err := mergeNameStatus(changes, "\x01\x00\nM\x00shared.txt\x00D\x00gone.txt\x00"); err != nil {
		t.Fatal(err)
	}
	want := map[string]fileChange{"shared.txt": {added: true}, "gone.txt": {deleted: true}}
	if diff := cmp.Diff(want, changes, cmp.AllowUnexported(fileChange{})); diff != "" {
		t.Fatalf("changes mismatch (-want +got):\n%s", diff)
	}
	if err := mergeNameStatus(changes, "MM\x00path\x00"); err == nil {
		t.Fatal("malformed status accepted")
	}
}

func TestIsTextIgnoresARuneCutAtTheSniffLimit(t *testing.T) {
	t.Parallel()
	cut := append(bytes.Repeat([]byte("a"), textSniffBytes-1), "✓"...)
	for _, test := range []struct {
		name   string
		prefix []byte
		want   bool
	}{
		{"empty", nil, true},
		{"utf8", []byte("wuko ✓\n"), true},
		{"nul", []byte("a\x00b"), false},
		{"invalid", []byte{0xff}, false},
		{"cut rune", cut, true},
		{"invalid before limit", append([]byte{0xff}, cut[1:]...), false},
	} {
		if got := isText(test.prefix); got != test.want {
			t.Errorf("%s: isText = %v, want %v", test.name, got, test.want)
		}
	}
}
