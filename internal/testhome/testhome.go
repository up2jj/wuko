// Package testhome gives a test binary its own home directory, so tests neither read nor
// write the developer's ~/.wuko, configuration, caches or Git settings. A package opts in by
// calling Run from its TestMain.
package testhome

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// gitConfig is the whole global Git configuration tests see. It names a committer, so commits
// work on a machine without one, and turns off settings a developer may have enabled globally
// that would change or break test repositories. A repository's own config still wins.
const gitConfig = `[user]
	name = Wuko Test
	email = wuko@example.test
[init]
	defaultBranch = main
[commit]
	gpgSign = false
[tag]
	gpgSign = false
[core]
	autocrlf = false
`

// Run is a TestMain body: it isolates the environment, runs the tests and exits.
func Run(m *testing.M) {
	root, err := Setup()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testhome:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

// Setup points HOME and the XDG base directories at a new temporary root and returns it. Go's
// own caches and settings keep their current locations: they derive from HOME by default, and a
// test that compiles a module should neither rebuild the standard library nor download modules.
func Setup() (string, error) {
	keep, err := goLocations()
	if err != nil {
		return "", err
	}
	root, err := os.MkdirTemp("", "wuko-test-home-")
	if err != nil {
		return "", err
	}
	env := map[string]string{
		"HOME":                filepath.Join(root, "home"),
		"XDG_CONFIG_HOME":     filepath.Join(root, "config"),
		"XDG_CACHE_HOME":      filepath.Join(root, "cache"),
		"XDG_DATA_HOME":       filepath.Join(root, "data"),
		"XDG_STATE_HOME":      filepath.Join(root, "state"),
		"GIT_CONFIG_NOSYSTEM": "1",
	}
	for _, dir := range env {
		if filepath.IsAbs(dir) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return root, err
			}
		}
	}
	if err := os.WriteFile(filepath.Join(env["HOME"], ".gitconfig"), []byte(gitConfig), 0o644); err != nil {
		return root, err
	}
	for key, value := range keep {
		env[key] = value
	}
	for key, value := range env {
		if err := os.Setenv(key, value); err != nil {
			return root, err
		}
	}
	return root, nil
}

// goLocations resolves the Go cache, module cache, GOPATH and settings file as the go command
// would with the current HOME, unless they are already set explicitly.
func goLocations() (map[string]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		gopath = filepath.Join(home, "go")
	}
	defaults := map[string]string{
		"GOPATH":     gopath,
		"GOMODCACHE": filepath.Join(filepath.SplitList(gopath)[0], "pkg", "mod"),
		"GOCACHE":    filepath.Join(cache, "go-build"),
		"GOENV":      filepath.Join(config, "go", "env"),
	}
	for key := range defaults {
		if value := os.Getenv(key); value != "" {
			defaults[key] = value
		}
	}
	return defaults, nil
}
