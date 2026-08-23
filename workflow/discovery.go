package workflow

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Source identifies a discovered workflow and its precedence scope.
type Source struct {
	Name        string
	Path        string
	Description string
	Invokable   bool
	DependsOn   map[string]string
	HasForm     bool
	Scope       string
	Effective   bool
}

// Discover finds effective workflows in project, home, and platform configuration directories.
func Discover(cwd, homeDir, configDir string) ([]Source, error) {
	sources, err := DiscoverAll(cwd, homeDir, configDir)
	if err != nil {
		return nil, err
	}
	effective := sources[:0]
	for _, source := range sources {
		if source.Effective {
			effective = append(effective, source)
		}
	}
	return effective, nil
}

// DiscoverAll returns every workflow definition in discovery order, including definitions
// shadowed by a closer local or global workflow with the same name.
func DiscoverAll(cwd, homeDir, configDir string) ([]Source, error) {
	locations := discoveryLocations(cwd, homeDir, configDir)
	var sources []Source
	for _, location := range locations {
		entries, err := os.ReadDir(location.dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading workflow directory %s: %w", location.dir, err)
		}

		inDir := make(map[string]string)
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if ext != ".yaml" && ext != ".yml" {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			if previous, ok := inDir[name]; ok {
				return nil, fmt.Errorf("workflow %q is declared twice in %s (%s and %s)", name, location.dir, previous, entry.Name())
			}
			inDir[name] = entry.Name()
		}

		names := make([]string, 0, len(inDir))
		for name := range inDir {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			path := filepath.Join(location.dir, inDir[name])
			definition, err := loadLocal(path)
			if err != nil {
				return nil, err
			}
			sources = append(sources, Source{
				Name: name, Path: path, Description: definition.Description,
				Invokable: definition.IsInvokable(), DependsOn: maps.Clone(definition.DependsOn),
				HasForm: definition.HasForm(), Scope: location.scope,
			})
		}
	}

	effective := make(map[string]struct{}, len(sources))
	for i := range sources {
		if _, exists := effective[sources[i].Name]; exists {
			continue
		}
		sources[i].Effective = true
		effective[sources[i].Name] = struct{}{}
	}
	slices.SortStableFunc(sources, func(a, b Source) int { return strings.Compare(a.Name, b.Name) })
	return sources, nil
}

// Find returns the effective workflow matching name.
func Find(cwd, homeDir, configDir, name string) (Source, error) {
	if !ValidWorkflowName(name) {
		return Source{}, fmt.Errorf("invalid workflow name %q", name)
	}
	sources, err := Discover(cwd, homeDir, configDir)
	if err != nil {
		return Source{}, err
	}
	for _, source := range sources {
		if source.Name == name {
			return source, nil
		}
	}
	return Source{}, fmt.Errorf("workflow %q not found", name)
}

type discoveryLocation struct {
	dir   string
	scope string
}

func discoveryLocations(cwd, homeDir, configDir string) []discoveryLocation {
	var dirs []string
	current, err := filepath.Abs(cwd)
	if err == nil {
		for {
			dirs = append(dirs, filepath.Join(current, ".wuko", "workflows"))
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}
	localCount := len(dirs)
	if homeDir != "" {
		dirs = append(dirs, filepath.Join(homeDir, ".wuko", "workflows"))
	}
	if configDir != "" {
		dirs = append(dirs, filepath.Join(configDir, "wuko", "workflows"))
	}

	seen := make(map[string]struct{}, len(dirs))
	locations := make([]discoveryLocation, 0, len(dirs))
	for i, dir := range dirs {
		clean := filepath.Clean(dir)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		scope := "local"
		if i >= localCount {
			scope = "global"
		}
		locations = append(locations, discoveryLocation{dir: clean, scope: scope})
	}
	return locations
}
