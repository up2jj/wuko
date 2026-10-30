//go:build darwin || linux

package process

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// Replacement describes a program that takes over the current process. It is resolved
// before the host commits to replacing itself, so Replace fails only for reasons the
// operating system reports at exec time.
type Replacement struct {
	Path string
	Argv []string
	Env  []string
	Dir  string
}

// ResolveReplacement resolves argv against the supplied environment and working directory
// and checks that the result can be executed. argv[0] is kept as the program's own name.
func ResolveReplacement(argv []string, dir string, env map[string]string) (Replacement, error) {
	if len(argv) == 0 || argv[0] == "" {
		return Replacement{}, errors.New("replacement command is empty")
	}
	if dir == "" {
		// An empty directory means the current one, as it does for exec.Cmd.
		current, err := os.Getwd()
		if err != nil {
			return Replacement{}, fmt.Errorf("replacement working directory: %w", err)
		}
		dir = current
	}
	info, err := os.Stat(dir)
	if err != nil {
		return Replacement{}, fmt.Errorf("replacement working directory: %w", err)
	}
	if !info.IsDir() {
		return Replacement{}, fmt.Errorf("replacement working directory %q is not a directory", dir)
	}
	path, err := ResolveExecutable(argv[0], dir, env)
	if err != nil {
		return Replacement{}, fmt.Errorf("resolving replacement command %q: %w", argv[0], err)
	}
	// ResolveExecutable leaves explicit paths unchecked; exec would report the same
	// failure only after the run had already committed to replacing itself.
	info, err = os.Stat(path)
	if err != nil {
		return Replacement{}, fmt.Errorf("resolving replacement command %q: %w", argv[0], err)
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return Replacement{}, fmt.Errorf("replacement command %q is not executable", path)
	}
	return Replacement{Path: path, Argv: append([]string(nil), argv...), Env: environment(env), Dir: dir}, nil
}

// Replace replaces the current process image with replacement. It returns only when the
// replacement could not be started. File descriptors opened by Go are close-on-exec and
// signal handlers reset to their defaults, so the program inherits only stdio, the
// terminal, and its process identity.
func Replace(replacement Replacement) error {
	if err := os.Chdir(replacement.Dir); err != nil {
		return fmt.Errorf("replacing wuko with %q: %w", replacement.Path, err)
	}
	if err := syscall.Exec(replacement.Path, replacement.Argv, replacement.Env); err != nil {
		return fmt.Errorf("replacing wuko with %q: %w", replacement.Path, err)
	}
	return nil
}
