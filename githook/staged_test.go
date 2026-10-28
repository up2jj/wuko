package githook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/gittest"
	"github.com/up2jj/wuko/internal/testfs"
)

func TestStagedGuardIsolatesAndRestoresTrackedFiles(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, path string
		staged     []byte
		unstaged   []byte
		deleted    bool
	}{
		{name: "text", path: "file.txt", staged: []byte("staged\n"), unstaged: []byte("unstaged\n")},
		{name: "binary", path: "file.bin", staged: []byte{0, 1, 255}, unstaged: []byte{0, 255, 3, 4}},
		{name: "deletion", path: "deleted.txt", staged: []byte("staged\n"), deleted: true},
		{name: "pathspec", path: ":(glob)*\n spaced.txt", staged: []byte("staged\n"), unstaged: []byte("unstaged\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository := testRepository(t)
			path := filepath.Join(repository.Root, test.path)
			testfs.WriteFile(t, path, test.staged, 0o644)
			gittest.Run(t, repository.Root, "--literal-pathspecs", "add", "--", test.path)
			tree := gittest.Output(t, repository.Root, "write-tree")
			if test.deleted {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				testfs.WriteFile(t, path, test.unstaged, 0o644)
			}
			testfs.Write(t, filepath.Join(repository.Root, "untracked.txt"), "keep me\n")
			guard, err := IsolateStaged(t.Context(), repository)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = guard.Cleanup(t.Context()) })
			assertStagedFile(t, path, test.staged)
			for name, want := range map[string]os.FileMode{"": 0o700, "unstaged.patch": 0o600, "tree": 0o600} {
				info, err := os.Stat(filepath.Join(guard.directory, name))
				if err != nil || info.Mode().Perm() != want {
					t.Fatalf("recovery %q mode: %v, %v", name, info, err)
				}
			}
			if err := guard.Cleanup(t.Context()); err != nil {
				t.Fatal(err)
			}
			if test.deleted {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("deleted file was not restored to absence: %v", err)
				}
			} else {
				assertStagedFile(t, path, test.unstaged)
			}
			assertStagedFile(t, filepath.Join(repository.Root, "untracked.txt"), []byte("keep me\n"))
			if got := gittest.Output(t, repository.Root, "write-tree"); got != tree {
				t.Fatalf("staged tree = %s, want %s", got, tree)
			}
			if _, err := os.Stat(guard.directory); !os.IsNotExist(err) {
				t.Fatalf("recovery was not removed: %v", err)
			}
		})
	}
}

func TestStagedGuardRestoresSymlinksAndExecutableModes(t *testing.T) {
	t.Parallel()
	repository := testRepository(t)
	path := filepath.Join(repository.Root, "script")
	link := filepath.Join(repository.Root, "link")
	testfs.WriteFile(t, path, []byte("staged\n"), 0o755)
	if err := os.Symlink("staged-target", link); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repository.Root, "add", ".")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("unstaged-target", link); err != nil {
		t.Fatal(err)
	}
	guard, err := IsolateStaged(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guard.Cleanup(t.Context()) })
	if target, err := os.Readlink(link); err != nil || target != "staged-target" {
		t.Fatalf("isolated link = %q, %v", target, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("isolated executable = %v, %v", info, err)
	}
	if err := guard.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(link); err != nil || target != "unstaged-target" {
		t.Fatalf("restored link = %q, %v", target, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("restored executable = %v, %v", info, err)
	}
}

func TestStagedGuardRejectsFormatterEditsAndRestoresUserChanges(t *testing.T) {
	t.Parallel()
	middle := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"
	staged := "first\n" + middle + "last\n"
	for _, test := range []struct {
		name, unstaged, formatted, want string
	}{
		{"no unstaged edits", staged, "formatted\n", "formatted\n"},
		{"nonoverlapping", "first\n" + middle + "user last\n", "formatted first\n" + middle + "last\n", "formatted first\n" + middle + "user last\n"},
		{"conflicting", "user first\n" + middle + "last\n", "formatted first\n" + middle + "last\n", "user first\n" + middle + "last\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository := testRepository(t)
			path := filepath.Join(repository.Root, "file.txt")
			testfs.Write(t, path, staged)
			gittest.Run(t, repository.Root, "add", "file.txt")
			tree := gittest.Output(t, repository.Root, "write-tree")
			testfs.Write(t, path, test.unstaged)
			guard, err := IsolateStaged(t.Context(), repository)
			if err != nil {
				t.Fatal(err)
			}
			testfs.Write(t, path, test.formatted)
			err = guard.Cleanup(t.Context())
			if err == nil || !strings.Contains(err.Error(), "files were modified") {
				t.Fatalf("Cleanup() = %v, want formatter modification failure", err)
			}
			assertStagedFile(t, path, []byte(test.want))
			if got := gittest.Output(t, repository.Root, "write-tree"); got != tree {
				t.Fatalf("formatter changed staged tree: %s", got)
			}
		})
	}
}

func TestStagedGuardRestoresAfterCancellation(t *testing.T) {
	t.Parallel()
	repository := testRepository(t)
	path := filepath.Join(repository.Root, "file.txt")
	testfs.Write(t, path, "staged\n")
	gittest.Run(t, repository.Root, "add", "file.txt")
	testfs.Write(t, path, "unstaged\n")
	ctx, cancel := context.WithCancel(t.Context())
	guard, err := IsolateStaged(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := guard.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := guard.Cleanup(ctx); err != nil {
		t.Fatalf("repeated Cleanup() = %v", err)
	}
	assertStagedFile(t, path, []byte("unstaged\n"))
}

func TestStagedGuardRetainsRecoveryWhenRestorationFails(t *testing.T) {
	t.Parallel()
	repository := testRepository(t)
	path := filepath.Join(repository.Root, "file.txt")
	testfs.Write(t, path, "staged\n")
	gittest.Run(t, repository.Root, "add", "file.txt")
	testfs.Write(t, path, "unstaged\n")
	guard, err := IsolateStaged(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an unavailable object database after a formatter creates a patch conflict.
	objects := filepath.Join(repository.CommonDir, "objects")
	if err := os.Rename(objects, objects+"-saved"); err != nil {
		t.Fatal(err)
	}
	testfs.Write(t, path, "formatted\n")
	err = guard.Cleanup(t.Context())
	if err == nil || !strings.Contains(err.Error(), "recovery patch retained") {
		t.Fatalf("Cleanup() = %v, want retained recovery", err)
	}
	if strings.Contains(err.Error(), "unstaged\n") || strings.Contains(err.Error(), "formatted\n") {
		t.Fatalf("Cleanup() exposed file contents: %v", err)
	}
	if err := os.Rename(objects+"-saved", objects); err != nil {
		t.Fatal(err)
	}
	if _, err := IsolateStaged(t.Context(), repository); err == nil || !strings.Contains(err.Error(), "unfinished staged isolation") {
		t.Fatalf("IsolateStaged() = %v, want unfinished recovery refusal", err)
	}
	testfs.Write(t, path, "staged\n")
	gittest.Run(t, repository.Root, "apply", "--binary", filepath.Join(guard.directory, "unstaged.patch"))
	assertStagedFile(t, path, []byte("unstaged\n"))
}

func TestStagedGuardWaitsForOverlappingRunInOneWorktree(t *testing.T) {
	t.Parallel()
	repository := testRepository(t)
	guard, err := IsolateStaged(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guard.Cleanup(t.Context()) })
	waiting, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := IsolateStaged(waiting, repository); !errors.Is(err, context.Canceled) {
		t.Fatalf("overlapping IsolateStaged() = %v, want a canceled wait", err)
	}
	type result struct {
		guard *StagedGuard
		err   error
	}
	next := make(chan result, 1)
	go func() {
		guard, err := IsolateStaged(t.Context(), repository)
		next <- result{guard, err}
	}()
	if err := guard.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := <-next
	if got.err != nil {
		t.Fatal(got.err)
	}
	if err := got.guard.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestStagedGuardHidesFlaggedIndexEntries(t *testing.T) {
	t.Parallel()
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			t.Parallel()
			repository := testRepository(t)
			path := filepath.Join(repository.Root, "file.txt")
			sparse := filepath.Join(repository.Root, "sparse.txt")
			testfs.Write(t, path, "staged\n")
			testfs.Write(t, sparse, "staged\n")
			gittest.Run(t, repository.Root, "add", ".")
			gittest.Run(t, repository.Root, "update-index", flag, "file.txt")
			gittest.Run(t, repository.Root, "update-index", "--skip-worktree", "sparse.txt")
			if err := os.Remove(sparse); err != nil {
				t.Fatal(err)
			}
			flags := gittest.Output(t, repository.Root, "ls-files", "-v")
			testfs.Write(t, path, "unstaged edit\n")
			guard, err := IsolateStaged(t.Context(), repository)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = guard.Cleanup(t.Context()) })
			assertStagedFile(t, path, []byte("staged\n"))
			if _, err := os.Lstat(sparse); !os.IsNotExist(err) {
				t.Fatalf("absent skip-worktree entry was checked out: %v", err)
			}
			if err := guard.Cleanup(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertStagedFile(t, path, []byte("unstaged edit\n"))
			if got := gittest.Output(t, repository.Root, "ls-files", "-v"); got != flags {
				t.Fatalf("index flags = %q, want %q", got, flags)
			}
			if _, err := os.Stat(guard.index); !os.IsNotExist(err) {
				t.Fatalf("private index was not removed: %v", err)
			}
		})
	}
}

func TestStagedGuardSavesConflictingHookEdits(t *testing.T) {
	t.Parallel()
	repository := testRepository(t)
	path := filepath.Join(repository.Root, "file.txt")
	testfs.Write(t, path, "staged\n")
	gittest.Run(t, repository.Root, "add", "file.txt")
	testfs.Write(t, path, "unstaged\n")
	guard, err := IsolateStaged(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	testfs.Write(t, path, "formatted\n")
	err = guard.Cleanup(t.Context())
	saved := filepath.Join(repository.GitDir, "wuko", "hook-edits.patch")
	if err == nil || !strings.Contains(err.Error(), saved) {
		t.Fatalf("Cleanup() = %v, want the saved hook edits path", err)
	}
	assertStagedFile(t, path, []byte("unstaged\n"))
	testfs.Write(t, path, "staged\n")
	gittest.Run(t, repository.Root, "apply", "--binary", saved)
	assertStagedFile(t, path, []byte("formatted\n"))
}

func TestStagedGuardRejectsIndexEditsWithoutLosingUserChanges(t *testing.T) {
	t.Parallel()
	repository := testRepository(t)
	path := filepath.Join(repository.Root, "file.txt")
	testfs.Write(t, path, "staged\n")
	gittest.Run(t, repository.Root, "add", "file.txt")
	testfs.Write(t, path, "unstaged\n")
	guard, err := IsolateStaged(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	testfs.Write(t, filepath.Join(repository.Root, "added.txt"), "hook addition\n")
	gittest.Run(t, repository.Root, "add", "added.txt")
	err = guard.Cleanup(t.Context())
	if err == nil || !strings.Contains(err.Error(), "modified the index") {
		t.Fatalf("Cleanup() = %v, want index modification failure", err)
	}
	assertStagedFile(t, path, []byte("unstaged\n"))
}

func TestStagedGuardRefusesIntentToAddWithoutMutation(t *testing.T) {
	t.Parallel()
	repository := testRepository(t)
	path := filepath.Join(repository.Root, "file.txt")
	testfs.Write(t, path, "unstaged\n")
	gittest.Run(t, repository.Root, "add", "-N", "file.txt")
	if _, err := IsolateStaged(t.Context(), repository); err == nil || !strings.Contains(err.Error(), "intent-to-add") {
		t.Fatalf("IsolateStaged() = %v, want an intent-to-add error", err)
	}
	assertStagedFile(t, path, []byte("unstaged\n"))
}

func TestStagedGuardRefusesUnmergedIndexWithoutMutation(t *testing.T) {
	t.Parallel()
	repository := testRepository(t)
	path := filepath.Join(repository.Root, "file.txt")
	testfs.Write(t, path, "staged\n")
	gittest.Run(t, repository.Root, "add", "file.txt")
	object := gittest.Output(t, repository.Root, "rev-parse", ":file.txt")
	command := exec.CommandContext(t.Context(), "git", "update-index", "--index-info")
	command.Dir = repository.Root
	command.Stdin = strings.NewReader(fmt.Sprintf("0 %s\tfile.txt\n100644 %s 1\tfile.txt\n100644 %s 2\tfile.txt\n", strings.Repeat("0", len(object)), object, object))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("creating unmerged index: %v\n%s", err, output)
	}
	unmerged := gittest.Output(t, repository.Root, "ls-files", "--unmerged")
	testfs.Write(t, path, "unstaged\n")
	if _, err := IsolateStaged(t.Context(), repository); err == nil || !strings.Contains(err.Error(), "index conflicts") {
		t.Fatalf("IsolateStaged() = %v, want an unmerged index error", err)
	}
	assertStagedFile(t, path, []byte("unstaged\n"))
	if got := gittest.Output(t, repository.Root, "ls-files", "--unmerged"); got != unmerged {
		t.Fatalf("unmerged index changed: %q", got)
	}
}

func TestStagedGuardHonorsAlternateIndex(t *testing.T) {
	// GIT_INDEX_FILE is process-wide; this test cannot run in parallel.
	repository := testRepository(t)
	path := filepath.Join(repository.Root, "file.txt")
	testfs.Write(t, path, "original index\n")
	gittest.Run(t, repository.Root, "add", "file.txt")
	original, err := os.ReadFile(filepath.Join(repository.GitDir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	alternate := filepath.Join(repository.GitDir, "alternate-index")
	testfs.WriteFile(t, alternate, original, 0o600)
	t.Setenv("GIT_INDEX_FILE", alternate)
	testfs.Write(t, path, "alternate index\n")
	gittest.Run(t, repository.Root, "add", "file.txt")
	testfs.Write(t, path, "unstaged\n")
	guard, err := IsolateStaged(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	assertStagedFile(t, path, []byte("alternate index\n"))
	if err := guard.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertStagedFile(t, path, []byte("unstaged\n"))
	assertStagedFile(t, filepath.Join(repository.GitDir, "index"), original)
}

func TestStagedGuardPreservesUntrackedDirectoryObstructions(t *testing.T) {
	t.Parallel()
	for _, duringHook := range []bool{false, true} {
		name := "before isolation"
		if duringHook {
			name = "during hook"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repository := testRepository(t)
			path := filepath.Join(repository.Root, "file.txt")
			testfs.Write(t, path, "staged\n")
			gittest.Run(t, repository.Root, "add", "file.txt")
			testfs.Write(t, path, "unstaged\n")
			var guard *StagedGuard
			if duringHook {
				var err error
				guard, err = IsolateStaged(t.Context(), repository)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			testfs.Write(t, filepath.Join(path, "untracked.txt"), "keep me\n")
			if duringHook {
				if err := guard.Cleanup(t.Context()); err == nil || !strings.Contains(err.Error(), "recovery patch retained") {
					t.Fatalf("Cleanup() = %v, want retained recovery for an obstructing directory", err)
				}
			} else if _, err := IsolateStaged(t.Context(), repository); err == nil || !strings.Contains(err.Error(), "replacing a directory") {
				t.Fatalf("IsolateStaged() = %v, want refusal before replacing a directory", err)
			}
			assertStagedFile(t, filepath.Join(path, "untracked.txt"), []byte("keep me\n"))
		})
	}
}

func TestStagedGuardAllowsIndependentLinkedWorktree(t *testing.T) {
	t.Parallel()
	root := gittest.InitWithCommit(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gittest.Run(t, root, "worktree", "add", "--detach", linked, "HEAD")
	var guards []*StagedGuard
	for _, path := range []string{root, linked} {
		repository, err := Discover(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		testfs.Write(t, filepath.Join(path, "README.md"), "unstaged\n")
		guard, err := IsolateStaged(t.Context(), repository)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = guard.Cleanup(t.Context()) })
		guards = append(guards, guard)
		assertStagedFile(t, filepath.Join(path, "README.md"), []byte("initial\n"))
	}
	for _, guard := range guards {
		if err := guard.Cleanup(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertStagedFile(t, filepath.Join(guard.repository.Root, "README.md"), []byte("unstaged\n"))
	}
}

func assertStagedFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("file %s mismatch (-want +got):\n%s", path, diff)
	}
}
