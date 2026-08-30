// Package observe defines background event sources and their source-owned coalescing behavior.
package observe

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sync"

	"gopkg.in/yaml.v3"
)

// Batch is bounded source-owned coalescing state. The observe scheduler controls when
// batches are run but does not inspect or interpret their events.
type Batch interface {
	Add(any)
	Merge(Batch)
	Empty() bool
	Binding() map[string]any
}

// Source is an already-ready producer. Initial is delivered to the first body run.
type Source interface {
	Initial() any
	Next(context.Context) (any, error)
	NewBatch() Batch
	Metadata() map[string]any
	Close() error
}

// OpenRequest carries declaration-time rendered source configuration and run context.
// New source-neutral capabilities can be added here without changing every engine call site.
type OpenRequest struct {
	RunDir string
	// Env is the workflow environment where the control was declared, including any enclosing
	// transparent env scope. Sources that run commands start from it.
	Env    map[string]string
	Config map[string]any
}

// Builder validates and opens one source type.
type Builder interface {
	Type() string
	Validate(map[string]any) error
	Open(context.Context, OpenRequest) (Source, error)
}

// Registry owns available source implementations. It is safe to share between engines.
type Registry struct {
	mu       sync.RWMutex
	builders map[string]Builder
}

func NewRegistry(builders ...Builder) *Registry {
	registry := &Registry{builders: make(map[string]Builder, len(builders))}
	for _, builder := range builders {
		if builder != nil {
			registry.builders[builder.Type()] = builder
		}
	}
	return registry
}

// NewDefaultRegistry returns the built-in source set. The observe control depends on this
// registry; the engine does not import this package or any concrete source implementation.
func NewDefaultRegistry() *Registry {
	return NewRegistry(FilesystemBuilder{}, HTTPBuilder{}, ShellBuilder{})
}

// Register adds a source type. Duplicate types are rejected.
func (registry *Registry) Register(builder Builder) error {
	if builder == nil || builder.Type() == "" {
		return fmt.Errorf("observe source builder requires a type")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.builders[builder.Type()]; exists {
		return fmt.Errorf("observe source type %q is already registered", builder.Type())
	}
	registry.builders[builder.Type()] = builder
	return nil
}

func (registry *Registry) Validate(sourceType string, config map[string]any) error {
	builder, err := registry.lookup(sourceType)
	if err != nil {
		return err
	}
	return builder.Validate(config)
}

func (registry *Registry) Open(ctx context.Context, sourceType string, request OpenRequest) (Source, error) {
	builder, err := registry.lookup(sourceType)
	if err != nil {
		return nil, err
	}
	return builder.Open(ctx, request)
}

func (registry *Registry) Types() []string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	types := make([]string, 0, len(registry.builders))
	for sourceType := range registry.builders {
		types = append(types, sourceType)
	}
	slices.Sort(types)
	return types
}

func (registry *Registry) lookup(sourceType string) (Builder, error) {
	registry.mu.RLock()
	builder := registry.builders[sourceType]
	registry.mu.RUnlock()
	if builder == nil {
		return nil, fmt.Errorf("observe source type %q is not registered (available: %v)", sourceType, registry.Types())
	}
	return builder, nil
}

// latestBatch coalesces polling observations by keeping the most recent one. Polling sources
// describe a current state rather than a stream of increments, so an older poll carries nothing
// the newest one has not already replaced.
type latestBatch struct {
	root   string
	latest map[string]any
}

func (batch *latestBatch) Add(value any) { batch.latest = cloneMap(value.(map[string]any)) }

func (batch *latestBatch) Merge(other Batch) {
	if latest := other.(*latestBatch).latest; latest != nil {
		batch.latest = cloneMap(latest)
	}
}

func (batch *latestBatch) Empty() bool { return batch.latest == nil }

func (batch *latestBatch) Binding() map[string]any {
	observation := map[string]any{}
	if batch.latest != nil {
		observation = cloneMap(batch.latest)
	}
	return map[string]any{batch.root: observation}
}

func decodeConfig(raw map[string]any, target any) error {
	data, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("encoding source configuration: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decoding source configuration: %w", err)
	}
	return nil
}
