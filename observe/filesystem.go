package observe

import (
	"context"
	"maps"
	"slices"

	"github.com/up2jj/wuko/fswatch"
)

type FilesystemBuilder struct {
	Factory fswatch.Factory
}

func (FilesystemBuilder) Type() string { return "filesystem" }

type filesystemConfig struct {
	Root   string   `yaml:"root,omitempty"`
	Paths  []string `yaml:"paths"`
	Ignore []string `yaml:"ignore,omitempty"`
	Events []string `yaml:"events,omitempty"`
}

func (builder FilesystemBuilder) Validate(raw map[string]any) error {
	_, err := builder.normalize(raw, false)
	return err
}

func (builder FilesystemBuilder) Open(ctx context.Context, request OpenRequest) (Source, error) {
	config, err := builder.normalize(request.Config, true)
	if err != nil {
		return nil, err
	}
	observer, err := fswatch.Open(ctx, request.RunDir, config, builder.Factory)
	if err != nil {
		return nil, err
	}
	return &filesystemSource{observer: observer, config: config}, nil
}

func (FilesystemBuilder) normalize(raw map[string]any, resolved bool) (fswatch.Config, error) {
	var declared filesystemConfig
	if err := decodeConfig(raw, &declared); err != nil {
		return fswatch.Config{}, err
	}
	_, hasRoot := raw["root"]
	_, hasEvents := raw["events"]
	return fswatch.Normalize(fswatch.Config{Root: declared.Root, Patterns: declared.Paths, Ignore: declared.Ignore, Events: declared.Events}, hasRoot, hasEvents, resolved)
}

type filesystemSource struct {
	observer *fswatch.Observer
	config   fswatch.Config
}

func (*filesystemSource) Initial() any { return nil }

func (source *filesystemSource) Next(ctx context.Context) (any, error) {
	return source.observer.Next(ctx)
}

func (*filesystemSource) NewBatch() Batch {
	return &filesystemBatch{changes: make(map[string]map[string]bool)}
}

func (source *filesystemSource) Metadata() map[string]any {
	paths := make([]any, len(source.config.Patterns))
	for index, value := range source.config.Patterns {
		paths[index] = value
	}
	ignore := make([]any, len(source.config.Ignore))
	for index, value := range source.config.Ignore {
		ignore[index] = value
	}
	events := make([]any, len(source.config.Events))
	for index, value := range source.config.Events {
		events[index] = value
	}
	return map[string]any{"root": source.observer.Root(), "paths": paths, "ignore": ignore, "events": events}
}

func (source *filesystemSource) Close() error { return source.observer.Close() }

type filesystemBatch struct {
	changes map[string]map[string]bool
}

func (batch *filesystemBatch) Add(value any) {
	change := value.(fswatch.Change)
	if change.Path == "" {
		return
	}
	operations := batch.changes[change.Path]
	if operations == nil {
		operations = make(map[string]bool)
		batch.changes[change.Path] = operations
	}
	for _, operation := range change.Operations {
		operations[operation] = true
	}
}

// Merge takes ownership of the other batch, per the Batch contract, so a path this batch has
// not seen adopts its operation set outright. Round-tripping through Add rebuilt every change
// as a one-element slice, which is a lot of garbage for a tree that churns.
func (batch *filesystemBatch) Merge(other Batch) {
	for path, operations := range other.(*filesystemBatch).changes {
		merged := batch.changes[path]
		if merged == nil {
			batch.changes[path] = operations
			continue
		}
		maps.Copy(merged, operations)
	}
}

func (batch *filesystemBatch) Empty() bool { return len(batch.changes) == 0 }

func (batch *filesystemBatch) Binding() map[string]any {
	paths := slices.Sorted(maps.Keys(batch.changes))
	pathValues := make([]any, len(paths))
	changes := make([]any, len(paths))
	order := fswatch.EventNames()
	for index, path := range paths {
		pathValues[index] = path
		operations := make([]any, 0, len(batch.changes[path]))
		for _, operation := range order {
			if batch.changes[path][operation] {
				operations = append(operations, operation)
			}
		}
		changes[index] = map[string]any{"path": path, "operations": operations}
	}
	return map[string]any{"filesystem": map[string]any{"paths": pathValues, "changes": changes}}
}
