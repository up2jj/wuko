package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Source identifies an effective discovered workflow.
type Source struct {
	Name        string
	Path        string
	Description string
}

// Discover finds effective workflows in project, home, and platform configuration directories.
func Discover(cwd, homeDir, configDir string) ([]Source, error) {
	dirs := discoveryDirs(cwd, homeDir, configDir)
	effective := make(map[string]Source)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading workflow directory %s: %w", dir, err)
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
				return nil, fmt.Errorf("workflow %q is declared twice in %s (%s and %s)", name, dir, previous, entry.Name())
			}
			inDir[name] = entry.Name()
		}

		for name, filename := range inDir {
			if _, exists := effective[name]; exists {
				continue
			}
			path := filepath.Join(dir, filename)
			definition, err := Load(path)
			if err != nil {
				return nil, err
			}
			effective[name] = Source{Name: name, Path: path, Description: definition.Description}
		}
	}

	sources := make([]Source, 0, len(effective))
	for _, source := range effective {
		sources = append(sources, source)
	}
	slices.SortFunc(sources, func(a, b Source) int { return strings.Compare(a.Name, b.Name) })
	return sources, nil
}

// Find returns the effective workflow matching name.
func Find(cwd, homeDir, configDir, name string) (Source, error) {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
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

func discoveryDirs(cwd, homeDir, configDir string) []string {
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
	if homeDir != "" {
		dirs = append(dirs, filepath.Join(homeDir, ".wuko", "workflows"))
	}
	if configDir != "" {
		dirs = append(dirs, filepath.Join(configDir, "wuko", "workflows"))
	}

	seen := make(map[string]struct{}, len(dirs))
	unique := dirs[:0]
	for _, dir := range dirs {
		clean := filepath.Clean(dir)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		unique = append(unique, clean)
	}
	return unique
}
