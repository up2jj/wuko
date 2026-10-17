package plugin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/up2jj/wuko/workflow"
	"gopkg.in/yaml.v3"
)

type workflowLockInput struct {
	path         string
	original     []byte
	document     yaml.Node
	requests     []pluginRequest
	dependencies map[string]string
	migrated     bool
}

func readWorkflowLockInput(path string, migrate bool) (*workflowLockInput, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	input := &workflowLockInput{path: path, original: data}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&input.document); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("expected one workflow YAML document")
	}
	if len(input.document.Content) != 1 || input.document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("workflow must be an object")
	}
	root := input.document.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != "plugins" {
			continue
		}
		plugins := root.Content[i+1]
		if plugins.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("plugins must be an object")
		}
		for j := 0; j < len(plugins.Content); j += 2 {
			namespace, declaration := plugins.Content[j].Value, plugins.Content[j+1]
			if declaration.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("plugin declaration must be an object")
			}
			request := pluginRequest{namespace: namespace}
			for k := 0; k < len(declaration.Content); k += 2 {
				if declaration.Content[k].Value != "sha256" {
					continue
				}
				if !migrate {
					return nil, fmt.Errorf("inline plugin sha256 is no longer supported; run wuko plugin lock FILE --migrate")
				}
				request.legacy = strings.ToLower(strings.TrimSpace(declaration.Content[k+1].Value))
				if !validDigest(request.legacy) {
					return nil, fmt.Errorf("invalid legacy plugin sha256 for %q", namespace)
				}
				declaration.Content = append(declaration.Content[:k], declaration.Content[k+2:]...)
				input.migrated = true
				break
			}
			if err := declaration.Decode(&request.source); err != nil {
				return nil, err
			}
			input.requests = append(input.requests, request)
		}
	}
	var header struct {
		Version   int               `yaml:"version"`
		Name      string            `yaml:"name"`
		DependsOn map[string]string `yaml:"depends_on"`
	}
	if err := root.Decode(&header); err != nil {
		return nil, err
	}
	if header.Version != 1 || header.Name == "" {
		return nil, fmt.Errorf("workflow requires version 1 and a name")
	}
	input.dependencies = header.DependsOn
	return input, nil
}

// LockWorkflows locks selected files and their declared dependencies without loading
// helpers or executing plugin processes. Each directory is published as one lock update.
func (m *Manager) LockWorkflows(ctx context.Context, paths []string, update, migrate bool) ([]string, error) {
	if update && migrate {
		return nil, fmt.Errorf("--migrate cannot be combined with --update")
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("at least one workflow file is required")
	}
	cwd, err := m.config.CWD()
	if err != nil {
		return nil, err
	}
	home, err := m.config.HomeDir()
	if err != nil {
		return nil, err
	}
	config, err := m.config.ConfigDir()
	if err != nil {
		return nil, err
	}
	inputs := make(map[string]*workflowLockInput)
	visiting := make(map[string]bool)
	var visit func(string) error
	visit = func(path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		path, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if visiting[path] {
			return fmt.Errorf("workflow dependency cycle at %s", path)
		}
		if inputs[path] != nil {
			return nil
		}
		visiting[path] = true
		input, err := readWorkflowLockInput(path, migrate)
		if err != nil {
			return fmt.Errorf("reading workflow %s: %w", path, err)
		}
		for _, alias := range slices.Sorted(maps.Keys(input.dependencies)) {
			dependency, err := workflow.FindForPluginLock(cwd, home, config, input.dependencies[alias])
			if err != nil {
				return err
			}
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[path] = false
		inputs[path] = input
		return nil
	}
	for _, path := range paths {
		if err := visit(path); err != nil {
			return nil, err
		}
	}
	groups := make(map[string][]pluginRequest)
	for _, path := range slices.Sorted(maps.Keys(inputs)) {
		dir := filepath.Dir(path)
		groups[dir] = append(groups[dir], inputs[path].requests...)
	}
	var written []string
	for _, dir := range slices.Sorted(maps.Keys(groups)) {
		if _, err := m.resolveRequests(ctx, workflow.PluginOrigin{Dir: dir}, groups[dir], update); err != nil {
			return written, err
		}
		if len(groups[dir]) > 0 {
			written = append(written, filepath.Join(dir, LockfileName))
		}
	}
	// The pins have been published first. Preserve the original source if an editor
	// changed it while releases were fetched, rather than overwriting concurrent edits.
	for _, path := range slices.Sorted(maps.Keys(inputs)) {
		input := inputs[path]
		if !input.migrated {
			continue
		}
		current, err := os.ReadFile(path)
		if err != nil {
			return written, err
		}
		if !bytes.Equal(current, input.original) {
			return written, fmt.Errorf("workflow %s changed during migration; retry", path)
		}
		var buffer bytes.Buffer
		encoder := yaml.NewEncoder(&buffer)
		encoder.SetIndent(2)
		if err := encoder.Encode(&input.document); err != nil {
			return written, err
		}
		if err := encoder.Close(); err != nil {
			return written, err
		}
		if err := writeAtomic(path, buffer.Bytes()); err != nil {
			return written, err
		}
	}
	return written, nil
}
