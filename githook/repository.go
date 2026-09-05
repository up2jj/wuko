package githook

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Repository struct {
	Root      string
	GitDir    string
	CommonDir string
}

func Discover(ctx context.Context, cwd string) (Repository, error) {
	run := func(arguments ...string) (string, error) {
		command := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, arguments...)...)
		output, err := command.Output()
		if err != nil {
			return "", fmt.Errorf("running git %s: %w", strings.Join(arguments, " "), err)
		}
		return strings.TrimSpace(string(output)), nil
	}
	bare, err := run("rev-parse", "--is-bare-repository")
	if err != nil {
		return Repository{}, fmt.Errorf("finding Git repository: %w", err)
	}
	if bare == "true" {
		return Repository{}, fmt.Errorf("server-side hooks in bare Git repositories are not supported")
	}
	root, err := run("rev-parse", "--show-toplevel")
	if err != nil {
		return Repository{}, fmt.Errorf("finding Git worktree root: %w", err)
	}
	gitDir, err := run("rev-parse", "--git-dir")
	if err != nil {
		return Repository{}, fmt.Errorf("finding Git directory: %w", err)
	}
	commonDir, err := run("rev-parse", "--git-common-dir")
	if err != nil {
		return Repository{}, fmt.Errorf("finding common Git directory: %w", err)
	}
	return Repository{Root: root, GitDir: absoluteGitPath(root, gitDir), CommonDir: absoluteGitPath(root, commonDir)}, nil
}

func (repository Repository) HookPath(ctx context.Context, name string) (string, error) {
	command := exec.CommandContext(ctx, "git", "-C", repository.Root, "rev-parse", "--git-path", "hooks/"+name)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("resolving Git hook path for %s: %w", name, err)
	}
	path := strings.TrimSpace(string(output))
	if !filepath.IsAbs(path) {
		path = filepath.Join(repository.Root, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving Git hook path %s: %w", path, err)
	}
	physical, err := physicalPath(path)
	if err != nil {
		return "", fmt.Errorf("resolving effective Git hook path %s: %w", path, err)
	}
	physicalRoot, err := physicalPath(repository.Root)
	if err != nil {
		return "", fmt.Errorf("resolving Git worktree root: %w", err)
	}
	physicalCommon, err := physicalPath(repository.CommonDir)
	if err != nil {
		return "", fmt.Errorf("resolving common Git directory: %w", err)
	}
	if !within(physical, physicalRoot) && !within(physical, physicalCommon) {
		return "", fmt.Errorf("effective Git hook path %s is shared outside this repository; integrate wuko git hook run manually", path)
	}
	return path, nil
}

func physicalPath(path string) (string, error) {
	current := filepath.Clean(path)
	var suffix []string
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return resolved, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return current, nil
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func absoluteGitPath(root, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	absolute, err := filepath.Abs(path)
	if err == nil {
		return absolute
	}
	return filepath.Clean(path)
}

func within(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
