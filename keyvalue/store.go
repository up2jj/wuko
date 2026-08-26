// Package keyvalue provides JSON-backed named key-value stores.
package keyvalue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"

	"github.com/up2jj/wuko/internal/filelock"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

const (
	// Local selects the values directory beside the top-level workflow.
	Local = "local"
	// Global selects the values directory in Wuko's platform configuration directory.
	Global = "global"
)

// Entry is one key and value returned by Store.List.
type Entry struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

// Store is one named JSON object rooted in a managed values directory.
type Store struct {
	path     string
	lockPath string
}

// Open validates name and returns a store rooted at dir. It does not access the filesystem.
func Open(dir, name string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("store directory is unavailable")
	}
	if !namePattern.MatchString(name) || name == "." || name == ".." {
		return nil, fmt.Errorf("invalid store name %q", name)
	}
	return &Store{
		path:     filepath.Join(dir, name+".json"),
		lockPath: filepath.Join(dir, name+".lock"),
	}, nil
}

// OpenScoped selects a root explicitly by scope and opens a named store in it.
func OpenScoped(localDir, globalDir, scope, name string) (*Store, error) {
	var dir string
	switch scope {
	case Local:
		dir = localDir
		if dir == "" {
			return nil, fmt.Errorf("local key-value storage is unavailable for this workflow")
		}
	case Global:
		dir = globalDir
		if dir == "" {
			return nil, fmt.Errorf("global key-value storage is unavailable")
		}
	default:
		return nil, fmt.Errorf("scope must be %q or %q", Local, Global)
	}
	return Open(dir, name)
}

// Get returns a cloned JSON value and whether key exists.
func (s *Store) Get(ctx context.Context, key string) (any, bool, error) {
	if err := validateKey(key); err != nil {
		return nil, false, err
	}
	var value any
	var found bool
	err := s.withLock(ctx, func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		value, found = values[key]
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return clone(value), found, nil
}

// Set stores value under key and returns its normalized JSON representation.
func (s *Store) Set(ctx context.Context, key string, value any) (any, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	normalized, err := Normalize(value)
	if err != nil {
		return nil, fmt.Errorf("value is not JSON-compatible: %w", err)
	}
	err = s.withLock(ctx, func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		values[key] = normalized
		return s.write(values)
	})
	if err != nil {
		return nil, err
	}
	return clone(normalized), nil
}

// SetIfDifferent atomically stores value when it differs from the current value. It returns true
// when the key was absent or its value changed. An unchanged value does not rewrite the store.
func (s *Store) SetIfDifferent(ctx context.Context, key string, value any) (bool, error) {
	if err := validateKey(key); err != nil {
		return false, err
	}
	normalized, err := Normalize(value)
	if err != nil {
		return false, fmt.Errorf("value is not JSON-compatible: %w", err)
	}
	changed := false
	err = s.withLock(ctx, func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		if previous, found := values[key]; found && reflect.DeepEqual(previous, normalized) {
			return nil
		}
		values[key] = normalized
		changed = true
		return s.write(values)
	})
	return changed, err
}

// Delete removes key and returns its previous value and whether it existed.
func (s *Store) Delete(ctx context.Context, key string) (any, bool, error) {
	if err := validateKey(key); err != nil {
		return nil, false, err
	}
	var value any
	var deleted bool
	err := s.withLock(ctx, func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		value, deleted = values[key]
		if !deleted {
			return nil
		}
		delete(values, key)
		return s.write(values)
	})
	if err != nil {
		return nil, false, err
	}
	return clone(value), deleted, nil
}

// List returns all entries ordered by key.
func (s *Store) List(ctx context.Context) ([]Entry, error) {
	var entries []Entry
	err := s.withLock(ctx, func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		entries = make([]Entry, 0, len(keys))
		for _, key := range keys {
			entries = append(entries, Entry{Key: key, Value: clone(values[key])})
		}
		return nil
	})
	return entries, err
}

func (s *Store) withLock(ctx context.Context, operation func() error) (resultErr error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("creating store directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("setting store directory permissions: %w", err)
	}
	lock, err := filelock.Acquire(ctx, s.lockPath)
	if err != nil {
		return fmt.Errorf("store %s: %w", s.path, err)
	}
	defer func() {
		if err := lock.Release(); resultErr == nil && err != nil {
			resultErr = fmt.Errorf("store %s: %w", s.path, err)
		}
	}()
	return operation()
}

func (s *Store) read() (map[string]any, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]any), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading store %s: %w", s.path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil {
		return nil, fmt.Errorf("decoding store %s: %w", s.path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decoding store %s: multiple JSON values", s.path)
		}
		return nil, fmt.Errorf("decoding store %s: %w", s.path, err)
	}
	if values == nil {
		return nil, fmt.Errorf("decoding store %s: root must be a JSON object", s.path)
	}
	return values, nil
}

func (s *Store) write(values map[string]any) (resultErr error) {
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding store: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	temporary, err := os.CreateTemp(dir, ".wuko-values-*")
	if err != nil {
		return fmt.Errorf("creating temporary store: %w", err)
	}
	temporaryPath := temporary.Name()
	open := true
	defer func() {
		_ = os.Remove(temporaryPath)
		if open {
			err = temporary.Close()
		}
		if resultErr == nil && err != nil {
			resultErr = fmt.Errorf("closing temporary store: %w", err)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("setting store permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("writing temporary store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("syncing temporary store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing temporary store: %w", err)
	}
	open = false
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replacing store %s: %w", s.path, err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening store directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("syncing store directory: %w", err)
	}
	return nil
}

func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}
	return nil
}

// Normalize converts a value to its lossless encoding/json representation.
func Normalize(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func clone(value any) any {
	cloned, err := Normalize(value)
	if err != nil {
		panic("keyvalue: stored value is not JSON-compatible")
	}
	return cloned
}
