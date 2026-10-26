package gittest

import (
	"os"
	"os/exec"
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

func TestInitConfiguresIdentityWithoutGlobalConfiguration(t *testing.T) {
	t.Parallel()
	root := Init(t)
	command := exec.Command("git", "commit", "--quiet", "--allow-empty", "-m", "test identity")
	command.Dir = root
	command.Env = []string{
		"HOME=" + t.TempDir(),
		"XDG_CONFIG_HOME=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("committing without global configuration: %v\n%s", err, output)
	}
	got := Output(t, root, "show", "-s", "--format=%an|%ae|%cn|%ce", "HEAD")
	const want = "Wuko Test|wuko@example.test|Wuko Test|wuko@example.test"
	if got != want {
		t.Fatalf("commit identity = %q, want %q", got, want)
	}
}
