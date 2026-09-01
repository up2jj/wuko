package environment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

type Command struct {
	Name string
	Args []string
	Dir  string
	Env  map[string]string
}

type CommandResult struct {
	Stdout string
	Stderr string
}

type CommandRunner func(context.Context, Command) (CommandResult, error)

type LookPath func(string, map[string]string) (string, error)

func runCommand(ctx context.Context, command Command) (CommandResult, error) {
	executable := exec.CommandContext(ctx, command.Name, command.Args...)
	executable.Dir = command.Dir
	executable.Env = environmentList(command.Env)
	var stdout, stderr bytes.Buffer
	executable.Stdout = &stdout
	executable.Stderr = &stderr
	err := executable.Run()
	return CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

func environmentList(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func lookPath(name string, environment map[string]string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		return executableFile(name)
	}
	for _, directory := range filepath.SplitList(environment["PATH"]) {
		if directory == "" {
			directory = "."
		}
		path := filepath.Join(directory, name)
		resolved, err := executableFile(path)
		if err == nil {
			return resolved, nil
		}
	}
	return "", exec.ErrNotFound
}

func executableFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() || runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		return "", fs.ErrPermission
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path, nil
	}
	return absolute, nil
}

func commandError(ctx context.Context, name string, result CommandResult, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	// Only stderr is quoted: loader stdout is the exported environment itself, and
	// this error is built before any workflow secret session exists to redact it.
	diagnostic := strings.TrimSpace(result.Stderr)
	if diagnostic != "" {
		return fmt.Errorf("running %s: %s: %w", name, diagnostic, err)
	}
	return fmt.Errorf("running %s: %w", name, err)
}

func unavailable(name string, err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("environment loader %q is unavailable: executable not found in PATH", name)
	}
	return fmt.Errorf("finding environment loader %q: %w", name, err)
}
