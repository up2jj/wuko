package environment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"strings"
)

type baseLoader struct {
	name     string
	lookPath LookPath
	run      CommandRunner
}

func (loader baseLoader) Name() string { return loader.name }

func (loader baseLoader) executable(environment map[string]string) (string, error) {
	lookup := loader.lookPath
	if lookup == nil {
		lookup = lookPath
	}
	return lookup(loader.name, environment)
}

func (loader baseLoader) runner() CommandRunner {
	if loader.run != nil {
		return loader.run
	}
	return runCommand
}

type MiseLoader struct{ baseLoader }

func NewMiseLoader() *MiseLoader { return &MiseLoader{baseLoader{name: LoaderMise}} }

func newMiseLoader(lookup LookPath, run CommandRunner) *MiseLoader {
	return &MiseLoader{baseLoader{name: LoaderMise, lookPath: lookup, run: run}}
}

func (loader *MiseLoader) Probe(_ context.Context, request Request) (Probe, error) {
	_, err := loader.executable(request.Env)
	return Probe{Available: err == nil}, nonLookupError(err)
}

func (loader *MiseLoader) Load(ctx context.Context, request Request) (Result, error) {
	executable, err := loader.executable(request.Env)
	if err != nil {
		return Result{}, unavailable(loader.Name(), err)
	}
	result, err := loader.runner()(ctx, Command{Name: executable, Args: []string{"env", "--json"}, Dir: request.Dir, Env: request.Env})
	if err != nil {
		return Result{}, commandError(ctx, loader.Name(), result, err)
	}
	values := make(map[string]string)
	if strings.TrimSpace(result.Stdout) != "" {
		if err := json.Unmarshal([]byte(result.Stdout), &values); err != nil {
			return Result{}, fmt.Errorf("decoding export: %w", err)
		}
	}
	changes := make(Changes, len(values))
	for name, value := range values {
		changes[name] = stringPointer(value)
	}
	return Result{Changes: changes, Applied: len(changes) > 0}, nil
}

type ASDFLoader struct{ baseLoader }

func NewASDFLoader() *ASDFLoader { return &ASDFLoader{baseLoader{name: LoaderASDF}} }

func newASDFLoader(lookup LookPath, run CommandRunner) *ASDFLoader {
	return &ASDFLoader{baseLoader{name: LoaderASDF, lookPath: lookup, run: run}}
}

func (loader *ASDFLoader) Probe(_ context.Context, request Request) (Probe, error) {
	_, err := loader.executable(request.Env)
	return Probe{Available: err == nil}, nonLookupError(err)
}

func (loader *ASDFLoader) Load(ctx context.Context, request Request) (Result, error) {
	executable, err := loader.executable(request.Env)
	if err != nil {
		return Result{}, unavailable(loader.Name(), err)
	}
	// The output is discarded; asdf current merely reports the resolved versions and
	// exits non-zero whenever one is uninstalled or unset, neither of which prevents
	// the shims from working. Only a failure to run asdf at all is fatal.
	result, err := loader.runner()(ctx, Command{Name: executable, Args: []string{"current"}, Dir: request.Dir, Env: request.Env})
	if err != nil && !exitStatus(err) {
		return Result{}, commandError(ctx, loader.Name(), result, err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, ctxErr
	}
	dataDir := request.Env["ASDF_DATA_DIR"]
	if dataDir == "" {
		home := request.Env["HOME"]
		if home == "" {
			return Result{}, fmt.Errorf("resolving shims: HOME and ASDF_DATA_DIR are unset")
		}
		dataDir = filepath.Join(home, ".asdf")
	}
	shims := filepath.Join(dataDir, "shims")
	if !directory(shims) {
		return Result{}, nil
	}
	path := prependPath(request.Env["PATH"], shims)
	return Result{Changes: Changes{"PATH": stringPointer(path)}, Applied: true}, nil
}

type DirenvLoader struct{ baseLoader }

func NewDirenvLoader() *DirenvLoader { return &DirenvLoader{baseLoader{name: LoaderDirenv}} }

func newDirenvLoader(lookup LookPath, run CommandRunner) *DirenvLoader {
	return &DirenvLoader{baseLoader{name: LoaderDirenv, lookPath: lookup, run: run}}
}

func (loader *DirenvLoader) Probe(_ context.Context, request Request) (Probe, error) {
	_, err := loader.executable(request.Env)
	return Probe{Available: err == nil}, nonLookupError(err)
}

func (loader *DirenvLoader) Load(ctx context.Context, request Request) (Result, error) {
	executable, err := loader.executable(request.Env)
	if err != nil {
		return Result{}, unavailable(loader.Name(), err)
	}
	result, err := loader.runner()(ctx, Command{Name: executable, Args: []string{"export", "json"}, Dir: request.Dir, Env: request.Env})
	if err != nil {
		return Result{}, commandError(ctx, loader.Name(), result, err)
	}
	changes := make(Changes)
	if strings.TrimSpace(result.Stdout) != "" {
		if err := json.Unmarshal([]byte(result.Stdout), &changes); err != nil {
			return Result{}, fmt.Errorf("decoding export: %w", err)
		}
	}
	final := maps.Clone(request.Env)
	apply(final, changes)
	return Result{Changes: changes, Applied: final["DIRENV_FILE"] != ""}, nil
}

// exitStatus reports whether a command ran to completion and exited non-zero, as
// opposed to failing to start.
func exitStatus(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

func nonLookupError(err error) error {
	if err == nil || errors.Is(err, exec.ErrNotFound) {
		return nil
	}
	return err
}

func stringPointer(value string) *string { return &value }

func prependPath(path, value string) string {
	parts := filepath.SplitList(path)
	filtered := make([]string, 0, len(parts)+1)
	filtered = append(filtered, value)
	for _, part := range parts {
		if part != value {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, string(filepath.ListSeparator))
}
