package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/engine"
	executorpkg "github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/internal/gittest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/process/processtest"
	"github.com/up2jj/wuko/provider"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestRepositoryHookWorkflowPreservesChecksAndContinuesAfterFailure(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"commit", "push", "all"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			root := gittest.InitWithCommit(t)
			base := gittest.Output(t, root, "rev-parse", "HEAD")
			testfs.WriteTree(t, root, map[string]string{
				"main.go": "package check \n", "go.mod": "module example\n",
				"config.yaml": "name: value\n", "config.toml": "name = 'value'\n",
			})
			gittest.Run(t, root, "add", ".")
			gittest.Run(t, root, "commit", "-qm", "fixture")
			head := gittest.Output(t, root, "rev-parse", "HEAD")
			gittest.Run(t, root, "reset", "--soft", base)
			definition := loadRepositoryChecks(t, target)
			executor := &repositoryCheckExecutor{}
			var output bytes.Buffer
			reporter := &gitHookReporter{workflowName: definition.Name, output: &output}
			_, err := repositoryCheckEngine(t).Run(t.Context(), definition, engine.Options{
				RunDir: root, Executor: executor, Stdout: io.Discard, Stderr: io.Discard, Progress: reporter.Progress,
				Providers: prePushProviders([]any{map[string]any{
					"local_ref": "refs/heads/main", "local_oid": head, "remote_ref": "refs/heads/main", "remote_oid": base,
				}}),
			})
			if err == nil {
				t.Fatal("failed policy check did not reject the workflow")
			}
			checks := []string{
				"trailing_whitespace", "end_of_file_fixer", "check_yaml", "check_toml", "check_merge_conflict",
				"check_added_large_files", "check_case_conflict", "detect_private_key", "gofmt", "go_vet", "go_mod_tidy", "staticcheck", "go_test",
			}
			for _, check := range checks {
				if strings.Count(output.String(), definition.Name+" / "+check+".") != 1 {
					t.Errorf("missing or repeated check row %s:\n%s\nrun error: %v", check, output.String(), err)
				}
			}
			for _, line := range strings.Split(output.String(), "\n") {
				for _, check := range checks {
					if !strings.Contains(line, " / "+check+".") {
						continue
					}
					want := "Passed"
					if check == "trailing_whitespace" {
						want = "Failed"
					} else if check == "check_merge_conflict" || target != "push" && (check == "staticcheck" || check == "go_test") ||
						target == "all" && check == "check_added_large_files" { // all tracked files have no additions
						want = "Skipped"
					}
					if !strings.HasSuffix(line, want) {
						t.Errorf("check row %q should end with %s", line, want)
					}
				}
			}
			var got []string
			for _, call := range executor.Calls() {
				got = append(got, call.Command+" "+strings.Join(call.Args, " "))
			}
			want := []string{"go vet ./...", "go mod tidy -diff"}
			if target == "push" {
				want = append(want, "sh scripts/lint.sh", "go test -race ./...")
			}
			// The tool checks run concurrently, so their call order is not fixed.
			slices.Sort(want)
			slices.Sort(got)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("checks mismatch (-want +got):\n%s\nrun error: %v", diff, err)
			}
		})
	}
}

// Keep real Git discovery while recording Go tools, so tests never run the test suite
// recursively or depend on a downloaded linter.
type repositoryCheckExecutor struct {
	processtest.Executor
	executorpkg.LocalFileSystem
}

func (executor *repositoryCheckExecutor) Run(ctx context.Context, options process.Options) (process.Result, error) {
	if options.Command == "git" {
		return process.LocalExecutor{}.Run(ctx, options)
	}
	return executor.Executor.Run(ctx, options)
}

func TestRepositoryFilePolicies(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, rule, input, want string
		fail                    bool
	}{
		{"trailing whitespace", "trailing_whitespace", "hello \t\r\nworld\t", "hello\r\nworld", true},
		{"markdown default", "trailing_whitespace", "line  \n", "line\n", true},
		{"ASCII whitespace", "trailing_whitespace", "line\v\f \t\r\n", "line\r\n", true},
		{"unicode spaces", "trailing_whitespace", "line\u00a0\n", "line\u00a0\n", false},
		{"EOF missing", "end_of_file_fixer", "hello", "hello\n", true},
		{"EOF multiple", "end_of_file_fixer", "hello\r\n\n\r\n", "hello\r\n", true},
		{"EOF only newlines", "end_of_file_fixer", "\r\n\n", "", true},
		{"EOF empty", "end_of_file_fixer", "", "", false},
		{"EOF CR", "end_of_file_fixer", "hello\r", "hello\r", false},
		{"valid YAML", "check_yaml", "name: value\n", "name: value\n", false},
		{"empty YAML", "check_yaml", "", "", false},
		{"bad YAML", "check_yaml", "name: [\n", "name: [\n", true},
		{"duplicate YAML", "check_yaml", "name: a\nname: b\n", "name: a\nname: b\n", true},
		{"multiple YAML documents", "check_yaml", "a: 1\n---\nb: 2\n", "a: 1\n---\nb: 2\n", true},
		{"unsafe YAML tag", "check_yaml", "value: !custom hello\n", "value: !custom hello\n", true},
		{"non JSON YAML", "check_yaml", "1: .inf\n", "1: .inf\n", false},
		{"valid TOML", "check_toml", "name = 'value'\n", "name = 'value'\n", false},
		{"bad TOML", "check_toml", "name = [\n", "name = [\n", true},
		{"duplicate TOML", "check_toml", "name = 1\nname = 2\n", "name = 1\nname = 2\n", true},
		{"non JSON TOML", "check_toml", "value = +inf\n", "value = +inf\n", false},
		{"no private key", "detect_private_key", "public data\n", "public data\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := gittest.Init(t)
			name := "file.txt"
			if test.rule == "check_yaml" {
				name = "file.yaml"
			} else if test.rule == "check_toml" {
				name = "file.toml"
			}
			path := filepath.Join(root, name)
			testfs.Write(t, path, test.input)
			gittest.Run(t, root, "add", ".")
			_, err := runRepositoryPolicy(t, root, test.rule)
			if (err != nil) != test.fail {
				t.Fatalf("policy error = %v, want failure %v", err, test.fail)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(test.want, string(data)); diff != "" {
				t.Fatalf("file mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRepositoryPrivateKeySignaturesDoNotExposeContents(t *testing.T) {
	t.Parallel()
	for _, tokens := range [][]string{
		{"BEGIN", "RSA", "PRIVATE", "KEY"}, {"BEGIN", "DSA", "PRIVATE", "KEY"},
		{"BEGIN", "EC", "PRIVATE", "KEY"}, {"BEGIN", "OPENSSH", "PRIVATE", "KEY"},
		{"BEGIN", "PRIVATE", "KEY"}, {"PuTTY-User-Key-File-", "2"},
		{"BEGIN", "SSH2", "ENCRYPTED", "PRIVATE", "KEY"}, {"BEGIN", "PGP", "PRIVATE", "KEY", "BLOCK"},
		{"BEGIN", "ENCRYPTED", "PRIVATE", "KEY"}, {"BEGIN", "OpenVPN", "Static", "key", "V1"},
	} {
		separator := " "
		if len(tokens) == 2 {
			separator = ""
		}
		signature := strings.Join(tokens, separator)
		t.Run(strings.Join(tokens[1:], "_"), func(t *testing.T) {
			t.Parallel()
			root := gittest.Init(t)
			testfs.Write(t, filepath.Join(root, "key"), signature+"\nprivate-material\n")
			gittest.Run(t, root, "add", ".")
			output, err := runRepositoryPolicy(t, root, "detect_private_key")
			if err == nil {
				t.Fatal("private key was accepted")
			}
			if strings.Contains(output+err.Error(), signature) || strings.Contains(output+err.Error(), "private-material") {
				t.Fatalf("diagnostics disclosed key contents: %s\n%v", output, err)
			}
		})
	}
}

func TestRepositoryLargeFilesThresholdAddedOnlyAndLFS(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	path := filepath.Join(root, "large.dat")
	testfs.WriteFile(t, path, bytes.Repeat([]byte{'x'}, 500*1024), 0o644)
	gittest.Run(t, root, "add", ".")
	if _, err := runRepositoryPolicy(t, root, "check_added_large_files"); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 500*1024+1); err != nil {
		t.Fatal(err)
	}
	if _, err := runRepositoryPolicy(t, root, "check_added_large_files"); err == nil {
		t.Fatal("oversized addition accepted")
	}
	testfs.Write(t, filepath.Join(root, ".gitattributes"), "*.dat filter=lfs\n")
	if _, err := runRepositoryPolicy(t, root, "check_added_large_files"); err != nil {
		t.Fatalf("LFS file rejected: %v", err)
	}
	if err := os.Remove(filepath.Join(root, ".gitattributes")); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "commit", "-qm", "baseline")
	gittest.Run(t, root, "add", ".")
	if _, err := runRepositoryPolicy(t, root, "check_added_large_files"); err != nil {
		t.Fatalf("existing file rejected: %v", err)
	}
}

func TestRepositoryCaseCollisionsIncludeDirectoriesAndIndex(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"readme.md", "Src/new.txt"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			root := gittest.InitWithCommit(t)
			testfs.Write(t, filepath.Join(root, "src", "file.txt"), "hello\n")
			gittest.Run(t, root, "add", ".")
			gittest.Run(t, root, "commit", "-qm", "baseline")
			testfs.Write(t, filepath.Join(root, path), "hello\n")
			blob := gittest.Output(t, root, "hash-object", "-w", "--", path)
			// Explicit index names work on case-insensitive filesystems too.
			gittest.Run(t, root, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path)
			if _, err := runRepositoryPolicy(t, root, "check_case_conflict"); err == nil {
				t.Fatalf("case collision %s accepted", path)
			}
		})
	}
}

func TestRepositoryMergeMarkersOnlyFailDuringMerge(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{"<<<<<<< ours\n", "=======\n", "=======\r\n", "======= extra\n", ">>>>>>> theirs\n"} {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()
			root := gittest.InitWithCommit(t)
			testfs.Write(t, filepath.Join(root, "file.txt"), marker)
			gittest.Run(t, root, "add", ".")
			if _, err := runRepositoryPolicy(t, root, "check_merge_conflict"); err != nil {
				t.Fatalf("non-merge marker rejected: %v", err)
			}
			testfs.Write(t, filepath.Join(root, ".git", "MERGE_MSG"), "merge\n")
			testfs.Write(t, filepath.Join(root, ".git", "MERGE_HEAD"), "merge\n")
			if _, err := runRepositoryPolicy(t, root, "check_merge_conflict"); err == nil {
				t.Fatal("merge marker accepted")
			}
		})
	}
}

func TestRepositoryGofmtFixesThenPasses(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	testfs.Write(t, filepath.Join(root, "-space name.go"), "package check\nfunc f(){println(1)}\n")
	gittest.Run(t, root, "add", ".")
	if _, err := runRepositoryPolicy(t, root, "gofmt"); err == nil {
		t.Fatal("gofmt edits did not fail check")
	}
	if _, err := runRepositoryPolicy(t, root, "gofmt"); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryFileSelectionPreservesFiltersAndNames(t *testing.T) {
	t.Parallel()
	root := gittest.InitWithCommit(t)
	testfs.WriteTree(t, root, map[string]string{
		"main.go": "package main\n", "go.mod": "module example\n", "config.yml": "a: 1\n",
		"config.toml": "a=1\n", "dist/generated.go": "package generated\n", "completions/wuko": "text\n",
		"name with\nnewline.txt": "hello\n", ".wuko/git-hooks.yaml": "version: 1\n",
	})
	for _, path := range []string{"image.png", "binary", "invalid-utf8"} {
		testfs.WriteFile(t, filepath.Join(root, path), []byte{255, 1, 2}, 0o644)
	}
	if err := os.Symlink("missing", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "add", ".")
	gittest.Run(t, root, "rm", "README.md")
	selected := runRepositorySelection(t, root, "staged", nil)
	for key, want := range map[string][]any{
		"files": {".wuko/git-hooks.yaml", "binary", "config.toml", "config.yml", "go.mod", "image.png", "invalid-utf8", "main.go", "name with\nnewline.txt"},
		"text":  {".wuko/git-hooks.yaml", "config.toml", "config.yml", "go.mod", "main.go", "name with\nnewline.txt"},
		"yaml":  {".wuko/git-hooks.yaml", "config.yml"}, "toml": {"config.toml"}, "go": {"main.go"},
	} {
		if diff := cmp.Diff(want, selected[key]); diff != "" {
			t.Errorf("%s mismatch (-want +got):\n%s", key, diff)
		}
	}
	if selected["tidy"] != true || selected["lint"] != true {
		t.Fatalf("Go triggers = %#v", selected)
	}
}

func TestRepositoryVetSelectionScopesChangedPackages(t *testing.T) {
	t.Parallel()
	root := gittest.InitWithCommit(t)
	testfs.WriteTree(t, root, map[string]string{
		"go.mod": "module example\n", "keep/keep.go": "package keep\n", "gone/gone.go": "package gone\n",
		"emptied/old.go": "package emptied\n", "emptied/README.md": "docs\n",
	})
	gittest.Run(t, root, "add", ".")
	gittest.Run(t, root, "commit", "-qm", "baseline")
	testfs.WriteTree(t, root, map[string]string{
		"main.go": "package main\n", "pkg/a.go": "package pkg\n", "pkg/b.go": "package pkg\n",
		"pkg/testdata/x.go": "package x\n", "_tools/t.go": "package tools\n", ".hidden/h.go": "package hidden\n",
		"nested/go.mod": "module nested\n", "nested/inner/n.go": "package inner\n", "keep/doc.md": "docs\n",
	})
	gittest.Run(t, root, "add", ".")
	gittest.Run(t, root, "rm", "-q", "keep/keep.go", "gone/gone.go", "emptied/old.go")
	testfs.Write(t, filepath.Join(root, "keep", "other.go"), "package keep\n")
	selected := runRepositorySelection(t, root, "staged", nil)
	if diff := cmp.Diff([]any{".", "./keep", "./pkg"}, selected["vet"]); diff != "" {
		t.Fatalf("vet packages mismatch (-want +got):\n%s", diff)
	}
	testfs.Write(t, filepath.Join(root, "go.mod"), "module example\n\ngo 1.26\n")
	gittest.Run(t, root, "add", "go.mod")
	if diff := cmp.Diff([]any{"./..."}, runRepositorySelection(t, root, "staged", nil)["vet"]); diff != "" {
		t.Fatalf("module change mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]any{"./..."}, runRepositorySelection(t, root, "all", nil)["vet"]); diff != "" {
		t.Fatalf("all files mismatch (-want +got):\n%s", diff)
	}
}

func TestRepositoryPushSelectionHandlesNewExistingDeletedAndRepeatedRefs(t *testing.T) {
	t.Parallel()
	root := gittest.InitWithCommit(t)
	base := gittest.Output(t, root, "rev-parse", "HEAD")
	zero := strings.Repeat("0", len(base))
	selected := runRepositorySelection(t, root, "pushed", []any{pushUpdate(base, zero)})
	if diff := cmp.Diff([]any{"README.md"}, selected["files"]); diff != "" {
		t.Fatalf("first push mismatch (-want +got):\n%s", diff)
	}
	gittest.Run(t, root, "update-ref", "refs/remotes/origin/main", base)
	testfs.Write(t, filepath.Join(root, "new.go"), "package test\n")
	gittest.Run(t, root, "add", ".")
	gittest.Run(t, root, "commit", "-qm", "change")
	head := gittest.Output(t, root, "rev-parse", "HEAD")
	for _, remote := range []string{base, zero} {
		updates := []any{
			pushUpdate(head, remote), pushUpdate(head, remote),
			pushUpdate(zero, base),
		}
		selected := runRepositorySelection(t, root, "pushed", updates)
		if diff := cmp.Diff([]any{"new.go"}, selected["files"]); diff != "" {
			t.Fatalf("push mismatch (-want +got):\n%s", diff)
		}
		// The large-file policy checks what the push adds.
		if diff := cmp.Diff([]any{"new.go"}, selected["added"]); diff != "" {
			t.Fatalf("pushed additions mismatch (-want +got):\n%s", diff)
		}
	}
	selected = runRepositorySelection(t, root, "pushed", []any{pushUpdate(base, zero)})
	if got := selected["files"]; len(got.(map[string]any)) != 0 {
		t.Fatalf("already remote branch files = %v", got)
	}
}

func TestRepositoryAllSelectionOnlyIncludesTrackedFiles(t *testing.T) {
	t.Parallel()
	root := gittest.InitWithCommit(t)
	testfs.Write(t, filepath.Join(root, "untracked.txt"), "hello\n")
	selected := runRepositorySelection(t, root, "all", nil)
	if diff := cmp.Diff([]any{"README.md"}, selected["files"]); diff != "" {
		t.Fatalf("all files mismatch (-want +got):\n%s", diff)
	}
}

func runRepositorySelection(t *testing.T, root, source string, updates []any) map[string]any {
	t.Helper()
	target := map[string]string{"staged": "commit", "pushed": "push", "all": "all"}[source]
	definition := loadRepositoryChecks(t, target)
	definition.Steps = definition.Steps[:len(definition.Steps)-1]
	state, err := repositoryCheckEngine(t).Run(t.Context(), definition, engine.Options{
		RunDir: root, Stdout: io.Discard, Stderr: io.Discard, Progress: func(engine.ProgressEvent) {},
		Providers: prePushProviders(updates),
	})
	if err != nil {
		t.Fatal(err)
	}
	return state.Steps["selected"].(map[string]any)
}

func pushUpdate(local, remote string) map[string]any {
	return map[string]any{"local_ref": "refs/heads/main", "local_oid": local, "remote_ref": "refs/heads/main", "remote_oid": remote}
}

// prePushProviders supplies the Git context a pre-push hook run receives.
func prePushProviders(updates []any) provider.Set {
	return provider.Set{
		Schemas: map[string]provider.Schema{"git": provider.NewGit().Schema()},
		Values: map[string]map[string]any{"git": {"hook": map[string]any{
			"name": "pre-push", "payload": map[string]any{"updates": updates},
		}}},
	}
}

func loadRepositoryChecks(t *testing.T, target string) *workflow.Definition {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.NewLoader(nil).Load(t.Context(), filepath.Join(root, ".wuko", "workflows", "repository-checks.yaml"), workflow.LoadOptions{RunDir: root, Target: target})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func repositoryCheckEngine(t *testing.T) *engine.Engine {
	t.Helper()
	registry := step.NewRegistry()
	if err := registerBuiltinSteps(registry); err != nil {
		t.Fatal(err)
	}
	return engine.New(registry)
}

func runRepositoryPolicy(t *testing.T, root, id string) (string, error) {
	t.Helper()
	definition := loadRepositoryChecks(t, "commit")
	group := definition.Steps[len(definition.Steps)-1].Concurrent
	index := slices.IndexFunc(group.Steps, func(s workflow.Step) bool { return s.ID == id })
	if index < 0 {
		t.Fatalf("missing repository check %s", id)
	}
	// Condition-skipped siblings satisfy the check's after edges without running.
	for i := range group.Steps {
		if i != index {
			group.Steps[i].If = "false"
		}
	}
	var output bytes.Buffer
	_, err := repositoryCheckEngine(t).Run(t.Context(), definition, engine.Options{RunDir: root, Stdout: &output, Stderr: &output, Progress: func(engine.ProgressEvent) {}})
	return output.String(), err
}
