// Package keyvalue implements JSON-backed persistent workflow values.
package keyvalue

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	storepkg "github.com/up2jj/wuko/keyvalue"
	"github.com/up2jj/wuko/step"
)

const (
	operationGet    = "get"
	operationSet    = "set"
	operationDelete = "delete"
	operationList   = "list"
)

// Config describes one persistent key-value operation.
type Config struct {
	Operation string `yaml:"operation"`
	Scope     string `yaml:"scope"`
	Store     string `yaml:"store"`
	Key       string `yaml:"key,omitempty"`
	Value     any    `yaml:"value,omitempty"`
}

// Runner executes a key-value operation.
type Runner struct {
	config   Config
	hasValue bool
}

// Register adds the key_value step to a registry.
func Register(registry *step.Registry) error { return registry.Register("key_value", New) }

// New decodes and validates a key_value step configuration.
func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	_, hasValue := raw["value"]
	runner := &Runner{config: config, hasValue: hasValue}
	if err := runner.validateConfig(); err != nil {
		return nil, err
	}
	return runner, nil
}

// Validate checks the selected storage root without touching the filesystem.
func (r *Runner) Validate(_ context.Context, request step.Request) error {
	if templated(r.config.Scope) {
		return nil
	}
	name := r.config.Store
	if templated(name) {
		name = "rendered-store"
	}
	_, err := storepkg.OpenScoped(request.LocalValueDir, request.GlobalValueDir, r.config.Scope, name)
	return err
}

// Run performs the configured operation.
func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := r.validateResolvedConfig(); err != nil {
		return step.Result{}, err
	}
	store, err := storepkg.OpenScoped(request.LocalValueDir, request.GlobalValueDir, r.config.Scope, r.config.Store)
	if err != nil {
		return step.Result{}, err
	}
	switch r.config.Operation {
	case operationGet:
		value, found, err := store.Get(ctx, r.config.Key)
		return step.Result{Outputs: map[string]any{"value": runtimeValue(value), "found": found}}, err
	case operationSet:
		value, err := store.Set(ctx, r.config.Key, r.config.Value)
		return step.Result{Outputs: map[string]any{"value": runtimeValue(value)}}, err
	case operationDelete:
		value, deleted, err := store.Delete(ctx, r.config.Key)
		return step.Result{Outputs: map[string]any{"value": runtimeValue(value), "deleted": deleted}}, err
	case operationList:
		entries, err := store.List(ctx)
		outputs := make([]any, len(entries))
		for i, entry := range entries {
			outputs[i] = map[string]any{"key": entry.Key, "value": runtimeValue(entry.Value)}
		}
		return step.Result{Outputs: map[string]any{"entries": outputs}}, err
	default:
		panic("validated key-value operation")
	}
}

func (r *Runner) validateConfig() error {
	if r.config.Scope == "" {
		return fmt.Errorf("scope is required")
	}
	if r.config.Store == "" {
		return fmt.Errorf("store is required")
	}
	// Open and OpenScoped validate resolved store names and scopes without accessing the filesystem.
	if !templated(r.config.Store) {
		if _, err := storepkg.Open("root", r.config.Store); err != nil {
			return err
		}
	}
	if !templated(r.config.Scope) {
		if _, err := storepkg.OpenScoped("local", "global", r.config.Scope, "store"); err != nil {
			return err
		}
	}
	if templated(r.config.Operation) {
		return nil
	}
	return r.validateOperation()
}

func (r *Runner) validateResolvedConfig() error {
	if templated(r.config.Operation) || templated(r.config.Scope) || templated(r.config.Store) {
		return fmt.Errorf("key-value configuration contains an unresolved template")
	}
	if _, err := storepkg.OpenScoped("local", "global", r.config.Scope, r.config.Store); err != nil {
		return err
	}
	return r.validateOperation()
}

func (r *Runner) validateOperation() error {
	switch r.config.Operation {
	case operationGet, operationDelete:
		if r.config.Key == "" {
			return fmt.Errorf("key is required for %s", r.config.Operation)
		}
		if r.hasValue {
			return fmt.Errorf("value is not allowed for %s", r.config.Operation)
		}
	case operationSet:
		if r.config.Key == "" {
			return fmt.Errorf("key is required for set")
		}
		if !r.hasValue {
			return fmt.Errorf("value is required for set")
		}
		normalized, err := storepkg.Normalize(r.config.Value)
		if err != nil {
			return fmt.Errorf("value is not JSON-compatible: %w", err)
		}
		r.config.Value = normalized
	case operationList:
		if r.config.Key != "" {
			return fmt.Errorf("key is not allowed for list")
		}
		if r.hasValue {
			return fmt.Errorf("value is not allowed for list")
		}
	case "":
		return fmt.Errorf("operation is required")
	default:
		return fmt.Errorf("operation must be get, set, delete, or list")
	}
	return nil
}

func templated(value string) bool { return strings.Contains(value, "{{") }

func runtimeValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if unsigned, err := strconv.ParseUint(string(typed), 10, 64); err == nil {
			return unsigned
		}
		number, err := typed.Float64()
		if err != nil {
			return typed
		}
		return number
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = runtimeValue(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = runtimeValue(item)
		}
		return result
	default:
		return value
	}
}
