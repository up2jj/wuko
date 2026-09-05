package githook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const dispatcherMarker = "# wuko git hook dispatcher v1"

type State struct {
	Hooks map[string]Record `json:"hooks"`
}

type Record struct {
	Digest     string `json:"digest"`
	Executable string `json:"executable"`
	Chained    bool   `json:"chained,omitempty"`
}

type Status struct {
	Name    string
	State   string
	Path    string
	Chained bool
}

func Install(ctx context.Context, repository Repository, executable string, manifest Manifest, chain bool) ([]Status, error) {
	if !filepath.IsAbs(executable) {
		return nil, fmt.Errorf("Wuko executable path must be absolute: %s", executable)
	}
	if info, err := os.Stat(executable); err != nil {
		return nil, fmt.Errorf("checking Wuko executable %s: %w", executable, err)
	} else if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("Wuko executable %s is not an executable regular file", executable)
	}
	state, err := loadState(repository)
	if err != nil {
		return nil, err
	}
	type target struct {
		name, path string
		record     Record
		preserve   bool
	}
	targets := make([]target, 0, len(manifest.Hooks))
	// Preflight every hook before mutating any of them.
	for _, name := range manifest.HookNames() {
		path, err := repository.HookPath(ctx, name)
		if err != nil {
			return nil, err
		}
		record, managed := state.Hooks[name]
		item := target{name: name, path: path, record: record}
		if managed {
			if err := verifyManaged(path, record); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			if record.Chained {
				if _, err := os.Lstat(backupPath(path)); err != nil {
					return nil, fmt.Errorf("checking preserved Git hook %s: %w", name, err)
				}
			}
		} else if info, err := os.Lstat(path); err == nil {
			if info.IsDir() {
				return nil, fmt.Errorf("Git hook path %s is a directory", path)
			}
			if !chain {
				return nil, fmt.Errorf("Git hook %s already exists at %s; rerun with --chain to preserve it", name, path)
			}
			backup := backupPath(path)
			if _, err := os.Lstat(backup); err == nil {
				return nil, fmt.Errorf("cannot preserve Git hook %s: backup already exists at %s", name, backup)
			} else if !os.IsNotExist(err) {
				return nil, fmt.Errorf("checking preserved Git hook %s: %w", name, err)
			}
			item.preserve = true
			item.record.Chained = true
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("checking Git hook %s: %w", name, err)
		}
		targets = append(targets, item)
	}
	var statuses []Status
	// Every dispatcher written so far must be recorded even when a later one fails, otherwise
	// an installed hook - and any hook it preserved - would be orphaned outside Wuko's state.
	var installErr error
	for _, item := range targets {
		if item.preserve {
			if err := os.Rename(item.path, backupPath(item.path)); err != nil {
				installErr = fmt.Errorf("preserving Git hook %s: %w", item.name, err)
				break
			}
		}
		data := dispatcher(executable, item.name)
		if err := writeExecutable(item.path, data); err != nil {
			if item.preserve {
				_ = os.Rename(backupPath(item.path), item.path)
			}
			installErr = err
			break
		}
		item.record.Digest = digest(data)
		item.record.Executable = executable
		state.Hooks[item.name] = item.record
		statuses = append(statuses, Status{Name: item.name, State: "installed", Path: item.path, Chained: item.record.Chained})
	}
	saveErr := saveState(repository, state)
	if installErr != nil {
		return nil, installErr
	}
	if saveErr != nil {
		return nil, saveErr
	}
	return statuses, nil
}

func Uninstall(ctx context.Context, repository Repository, names []string) ([]Status, error) {
	state, err := loadState(repository)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		names = mapsKeys(state.Hooks)
		slices.Sort(names)
	}
	type target struct {
		name, path string
		record     Record
	}
	targets := make([]target, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	// Verify the complete removal set before deleting any dispatcher.
	for _, name := range names {
		record, ok := state.Hooks[name]
		if !ok {
			return nil, fmt.Errorf("Git hook %s is not managed by Wuko", name)
		}
		// A repeated name would remove the hook restored by its own earlier pass.
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		path, err := repository.HookPath(ctx, name)
		if err != nil {
			return nil, err
		}
		// A dispatcher that is already gone leaves nothing to protect; refusing here would
		// strand its state record and block removing every other managed hook.
		if err := verifyManaged(path, record); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if record.Chained {
			if info, err := os.Lstat(backupPath(path)); err != nil {
				return nil, fmt.Errorf("checking preserved Git hook %s: %w", name, err)
			} else if info.IsDir() {
				return nil, fmt.Errorf("preserved Git hook %s is a directory", name)
			}
		}
		targets = append(targets, target{name: name, path: path, record: record})
	}
	var statuses []Status
	for _, item := range targets {
		if err := os.Remove(item.path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("removing Git hook %s: %w", item.name, err)
		}
		if item.record.Chained {
			if err := os.Rename(backupPath(item.path), item.path); err != nil {
				return nil, fmt.Errorf("restoring preserved Git hook %s: %w", item.name, err)
			}
		}
		delete(state.Hooks, item.name)
		statuses = append(statuses, Status{Name: item.name, State: "uninstalled", Path: item.path, Chained: item.record.Chained})
	}
	if err := saveState(repository, state); err != nil {
		return nil, err
	}
	return statuses, nil
}

func Inspect(ctx context.Context, repository Repository, manifest Manifest) ([]Status, error) {
	state, err := loadState(repository)
	if err != nil {
		return nil, err
	}
	names := manifest.HookNames()
	for name := range state.Hooks {
		if _, declared := manifest.Hooks[name]; !declared {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	statuses := make([]Status, 0, len(names))
	for _, name := range names {
		path, err := repository.HookPath(ctx, name)
		if err != nil {
			return nil, err
		}
		record, managed := state.Hooks[name]
		status := Status{Name: name, Path: path, Chained: record.Chained}
		data, readErr := os.ReadFile(path)
		switch {
		case managed && readErr == nil && digest(data) == record.Digest:
			if info, err := os.Stat(record.Executable); err != nil || info.Mode()&0o111 == 0 {
				status.State = "broken binary"
			} else {
				status.State = "installed"
			}
		case managed && os.IsNotExist(readErr):
			status.State = "missing"
		case managed:
			status.State = "modified"
		case readErr == nil:
			status.State = "conflicting"
		default:
			status.State = "not installed"
		}
		if _, declared := manifest.Hooks[name]; !declared {
			status.State = "stale"
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func PreservedPath(ctx context.Context, repository Repository, name string) (string, bool, error) {
	state, err := loadState(repository)
	if err != nil {
		return "", false, err
	}
	record, ok := state.Hooks[name]
	if !ok || !record.Chained {
		return "", false, nil
	}
	path, err := repository.HookPath(ctx, name)
	if err != nil {
		return "", false, err
	}
	return backupPath(path), true, nil
}

func dispatcher(executable, name string) []byte {
	return []byte("#!/bin/sh\n" + dispatcherMarker + "\nexec " + shellQuote(executable) + " git hook run " + shellQuote(name) + " -- \"$@\"\n")
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
func backupPath(path string) string  { return path + ".wuko-chain" }
func digest(data []byte) string      { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func statePath(repository Repository) string {
	return filepath.Join(repository.CommonDir, "wuko", "git-hooks.json")
}

func loadState(repository Repository) (State, error) {
	state := State{Hooks: make(map[string]Record)}
	data, err := os.ReadFile(statePath(repository))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("reading Git hook state: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("decoding Git hook state: %w", err)
	}
	if state.Hooks == nil {
		state.Hooks = make(map[string]Record)
	}
	return state, nil
}

func saveState(repository Repository, state State) error {
	path := statePath(repository)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating Git hook state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding Git hook state: %w", err)
	}
	data = append(data, '\n')
	return writeFile(path, data, 0o600)
}

func verifyManaged(path string, record Record) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if digest(data) != record.Digest || !strings.Contains(string(data), dispatcherMarker) {
		return fmt.Errorf("Git hook at %s was modified after Wuko installed it; refusing to overwrite it", path)
	}
	return nil
}

func writeExecutable(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating Git hooks directory: %w", err)
	}
	if err := writeFile(path, data, 0o755); err != nil {
		return fmt.Errorf("writing Git hook %s: %w", path, err)
	}
	return nil
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".wuko-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
