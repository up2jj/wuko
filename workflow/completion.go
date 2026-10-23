package workflow

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// DiscoverForCompletion reads only workflow metadata, skipping malformed YAML.
// It never prepares workflows or initializes their plugins and helpers. Directory
// and file I/O errors still fail discovery rather than presenting incomplete results.
func DiscoverForCompletion(cwd, homeDir, configDir string) ([]Source, error) {
	var sources []Source
	seen := make(map[string]bool)
	for _, location := range discoveryLocations(cwd, homeDir, configDir) {
		files, err := discoverWorkflowFiles(location.dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			name := file.name
			if name != "" {
				if seen[name] {
					continue
				}
				// Even an unfinished local file shadows a global workflow of the same name.
				seen[name] = true
			}
			data, err := os.ReadFile(file.path)
			if err != nil {
				return nil, fmt.Errorf("reading workflow metadata %s: %w", file.path, err)
			}
			var metadata struct {
				Name        string    `yaml:"name"`
				Description string    `yaml:"description"`
				Invokable   *bool     `yaml:"invokable"`
				Form        yaml.Node `yaml:"form"`
				Targets     map[string]struct {
					Description string    `yaml:"description"`
					Form        yaml.Node `yaml:"form"`
				} `yaml:"targets"`
			}
			if err := yaml.Unmarshal(data, &metadata); err != nil {
				continue
			}
			if file.packageDir != "" {
				name = metadata.Name
				if !ValidWorkflowName(name) || seen[name] {
					continue
				}
				seen[name] = true
			}
			source := Source{
				Name: name, Path: file.path, Scope: location.scope, Effective: true,
				PackageDir: file.packageDir, MarketplaceURL: file.marketplaceURL,
				Description: metadata.Description, Invokable: metadata.Invokable == nil || *metadata.Invokable,
				HasForm: (&Definition{Form: metadata.Form}).HasForm(),
			}
			if len(metadata.Targets) == 0 {
				sources = append(sources, source)
			}
			for name, target := range metadata.Targets {
				selected := source
				selected.Target = name
				selected.Description = firstNonEmpty(target.Description, metadata.Description)
				selected.HasForm = source.HasForm || (TargetDefinition{Form: target.Form}).HasForm()
				sources = append(sources, selected)
			}
		}
	}
	slices.SortFunc(sources, func(a, b Source) int {
		if comparison := strings.Compare(a.Name, b.Name); comparison != 0 {
			return comparison
		}
		return strings.Compare(a.Target, b.Target)
	})
	return sources, nil
}
