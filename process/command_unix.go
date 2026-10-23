//go:build darwin || linux

package process

import (
	"os/exec"
	"path/filepath"
	"strings"
)

func localCommand(options Options) *exec.Cmd {
	command := exec.Command(options.Command, options.Args...)
	if _, configured := options.Env["PATH"]; configured && !strings.ContainsRune(options.Command, filepath.Separator) {
		// exec.Command searches the host PATH before Cmd.Env is assigned. Replace
		// that lookup, including any error, with the workflow's explicit PATH.
		command.Path, command.Err = ResolveExecutable(options.Command, options.Dir, options.Env)
	}
	command.Dir = options.Dir
	command.Env = environment(options.Env)
	return command
}

// ResolveExecutable resolves a local command using the supplied PATH. Relative
// PATH entries and explicit relative command paths are based on dir. An absent
// PATH retains host lookup; an explicitly empty PATH searches no directories.
// Explicit command paths are returned without checking access, which the child
// process checks after applying its configured user identity.
func ResolveExecutable(name, dir string, environment map[string]string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		if !filepath.IsAbs(name) {
			name = filepath.Join(dir, name)
		}
		return filepath.Abs(name)
	}
	path, configured := environment["PATH"]
	if !configured {
		resolved, err := exec.LookPath(name)
		if err != nil {
			return "", err
		}
		return filepath.Abs(resolved)
	}
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for _, directory := range filepath.SplitList(path) {
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(base, directory)
		}
		if resolved, err := exec.LookPath(filepath.Join(directory, name)); err == nil {
			return resolved, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}
