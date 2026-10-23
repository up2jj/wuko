package gittest

import (
	"testing"

	"github.com/up2jj/wuko/internal/testhome"
)

func TestMain(m *testing.M) {
	testhome.Run(m)
}

func TestInitWithCommitCreatesOneTrackedCommit(t *testing.T) {
	t.Parallel()
	root := InitWithCommit(t)
	if count := Output(t, root, "rev-list", "--count", "HEAD"); count != "1" {
		t.Fatalf("commit count = %q, want 1", count)
	}
	if tracked := Output(t, root, "ls-files"); tracked != "README.md" {
		t.Fatalf("tracked files = %q, want README.md", tracked)
	}
}

func TestInitCreatesEmptyRepository(t *testing.T) {
	t.Parallel()
	root := Init(t)
	if inside := Output(t, root, "rev-parse", "--is-inside-work-tree"); inside != "true" {
		t.Fatalf("inside work tree = %q", inside)
	}
	if branches := Output(t, root, "branch", "--list"); branches != "" {
		t.Fatalf("branches = %q, want none", branches)
	}
}
