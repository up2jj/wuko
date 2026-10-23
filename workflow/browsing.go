package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// DiscoveryWarning describes an invalid workflow omitted from browsing results.
// Source retains its precedence, including whether it shadows valid workflows.
type DiscoveryWarning struct {
	Source Source
	Err    error
}

// DiscoverForBrowsing returns valid effective and shadowed workflows, reporting
// invalid definitions separately. Invalid definitions still reserve their names.
// Filesystem errors and ambiguous identities remain fatal.
func DiscoverForBrowsing(cwd, homeDir, configDir string) ([]Source, []DiscoveryWarning, error) {
	invalid := make(map[string]error)
	sources, err := discoverAll(cwd, homeDir, configDir, func(source Source, err error) {
		invalid[source.Path] = err
	})
	if err != nil {
		return nil, nil, err
	}
	var warnings []DiscoveryWarning
	valid := sources[:0]
	for _, source := range sources {
		if err, ok := invalid[source.Path]; ok {
			warnings = append(warnings, DiscoveryWarning{Source: source, Err: err})
			continue
		}
		valid = append(valid, source)
	}
	return valid, warnings, nil
}

func invalidDiscoverySource(file discoveredWorkflowFile, scope string) (Source, error) {
	source := Source{Name: file.name, Path: file.path, Scope: scope, PackageDir: file.packageDir, MarketplaceURL: file.marketplaceURL}
	if file.packageDir == "" {
		return source, nil
	}
	// Package selectors come from the manifest rather than the installation path.
	// A damaged manifest can recover its installed name from the package marker.
	data, err := os.ReadFile(file.path)
	if err != nil {
		return Source{}, fmt.Errorf("reading workflow %s: %w", file.path, err)
	}
	var header struct {
		Name string `yaml:"name" json:"name"`
	}
	if err := yaml.Unmarshal(data, &header); err == nil && ValidWorkflowName(header.Name) {
		source.Name = header.Name
		return source, nil
	}
	marker := filepath.Join(file.packageDir, WorkflowPackageMarkerName)
	data, err = os.ReadFile(marker)
	if err != nil {
		return Source{}, fmt.Errorf("reading workflow package marker %s: %w", marker, err)
	}
	header.Name = ""
	if err := json.Unmarshal(data, &header); err != nil {
		return Source{}, fmt.Errorf("decoding workflow package marker %s: %w", marker, err)
	}
	if !ValidWorkflowName(header.Name) {
		return Source{}, fmt.Errorf("cannot determine the name of invalid workflow %s; refusing to expose a lower-precedence workflow", file.path)
	}
	source.Name = header.Name
	return source, nil
}
