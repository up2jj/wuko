package testhome

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupIsolatesHomeAndGitButKeepsGoCaches(t *testing.T) {
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	wantGoCache := os.Getenv("GOCACHE")
	if wantGoCache == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			t.Fatal(err)
		}
		wantGoCache = filepath.Join(cache, "go-build")
	}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "GIT_CONFIG_NOSYSTEM", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV"} {
		t.Setenv(key, os.Getenv(key))
	}

	root, err := Setup()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if home == realHome || !strings.HasPrefix(home, root) {
		t.Fatalf("HOME = %q, want a directory under %q", home, root)
	}
	if got := os.Getenv("GOCACHE"); got != wantGoCache {
		t.Fatalf("GOCACHE = %q, want the developer's %q", got, wantGoCache)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	output, err := exec.Command("git", "config", "--global", "user.email").Output()
	if err != nil || strings.TrimSpace(string(output)) != "wuko@example.test" {
		t.Fatalf("global git user.email = %q, %v", output, err)
	}
}
