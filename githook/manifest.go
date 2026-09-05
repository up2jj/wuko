// Package githook installs and executes Wuko workflows as client-side Git hooks.
package githook

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/up2jj/wuko/workflow"
	"gopkg.in/yaml.v3"
)

const ManifestPath = ".wuko/git-hooks.yaml"

var supportedHooks = map[string]struct{}{
	"applypatch-msg": {}, "pre-applypatch": {}, "post-applypatch": {},
	"pre-commit": {}, "pre-merge-commit": {}, "prepare-commit-msg": {},
	"commit-msg": {}, "post-commit": {}, "pre-rebase": {}, "post-checkout": {},
	"post-merge": {}, "pre-push": {}, "pre-auto-gc": {}, "post-rewrite": {},
	"sendemail-validate": {}, "post-index-change": {},
}

type Manifest struct {
	Version int                  `yaml:"version"`
	Hooks   map[string][]Binding `yaml:"hooks"`
}

type Binding struct {
	Workflow string `yaml:"workflow"`
	Target   string `yaml:"target,omitempty"`
}

func LoadManifest(root string) (Manifest, error) {
	path := filepath.Join(root, filepath.FromSlash(ManifestPath))
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("reading Git hook manifest %s: %w", path, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decoding Git hook manifest %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple YAML documents are not supported")
		}
		return Manifest{}, fmt.Errorf("decoding Git hook manifest %s: %w", path, err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("validating Git hook manifest %s: %w", path, err)
	}
	return manifest, nil
}

func (manifest Manifest) Validate() error {
	if manifest.Version != 1 {
		return fmt.Errorf("version must be 1")
	}
	if len(manifest.Hooks) == 0 {
		return fmt.Errorf("hooks must contain at least one Git hook")
	}
	for _, name := range manifest.HookNames() {
		if !Supported(name) {
			return fmt.Errorf("unsupported client-side Git hook %q", name)
		}
		bindings := manifest.Hooks[name]
		if len(bindings) == 0 {
			return fmt.Errorf("Git hook %q must contain at least one workflow binding", name)
		}
		for index, binding := range bindings {
			if binding.Workflow == "" {
				return fmt.Errorf("Git hook %q binding %d requires workflow", name, index+1)
			}
			// Hooks fire on ordinary Git commands, so a binding may only name a workflow that
			// local discovery resolves. A remote locator would fetch and run unreviewed code
			// as soon as the manifest changed, without reinstalling anything.
			if !workflow.ValidWorkflowSelector(binding.Workflow) || workflow.IsRemoteLocator(binding.Workflow) {
				return fmt.Errorf("Git hook %q binding %d workflow %q must name a locally discovered workflow", name, index+1, binding.Workflow)
			}
		}
	}
	return nil
}

func (manifest Manifest) HookNames() []string {
	names := mapsKeys(manifest.Hooks)
	slices.Sort(names)
	return names
}

func Supported(name string) bool {
	_, ok := supportedHooks[name]
	return ok
}

func SupportedNames() []string {
	names := mapsKeys(supportedHooks)
	slices.Sort(names)
	return names
}

func mapsKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
