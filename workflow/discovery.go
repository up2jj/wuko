package workflow

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Source identifies a discovered workflow and its precedence scope.
type Source struct {
	Name           string
	Target         string
	Path           string
	PackageDir     string
	PackageVersion string
	Description    string
	Invokable      bool
	DependsOn      map[string]string
	HasForm        bool
	Scope          string
	Effective      bool
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
		locationSources, err := discoverDirectory(location.dir, location.scope)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		sources = append(sources, locationSources...)
	}

	// A working directory inside a workflow root can cause the same file to be
	// discovered through both the current-directory walk and a parent/home
	// workflow root. Keep the first occurrence because discovery order encodes
	// precedence, while avoiding duplicate picker entries for the same source.
	unique := make([]Source, 0, len(sources))
	seen := make(map[struct {
		path   string
		target string
	}]struct{}, len(sources))
	for _, source := range sources {
		key := struct {
			path   string
			target string
		}{path: filepath.Clean(source.Path), target: source.Target}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, source)
	}
	sources = unique

	effective := make(map[string]string, len(sources))
	for i := range sources {
		path, exists := effective[sources[i].Name]
		if !exists {
			sources[i].Effective = true
			effective[sources[i].Name] = sources[i].Path
			continue
		}
		sources[i].Effective = path == sources[i].Path
	}
	slices.SortStableFunc(sources, func(a, b Source) int {
		if comparison := strings.Compare(a.Name, b.Name); comparison != 0 {
			return comparison
		}
		return strings.Compare(a.Target, b.Target)
	})
	return sources, nil
}

// DiscoverDirectory returns workflows rooted directly below directory. It is used by lifecycle
// commands that need to search one storage scope without traversing other precedence scopes.
func DiscoverDirectory(directory, scope string) ([]Source, error) {
	return discoverDirectory(directory, scope)
}

// FindInDirectory returns a workflow from one storage directory, including installed packages.
func FindInDirectory(directory, name string) (Source, error) {
	if !ValidWorkflowSelector(name) {
		return Source{}, fmt.Errorf("invalid workflow name %q", name)
	}
	sources, err := DiscoverDirectory(directory, "local")
	if err != nil {
		return Source{}, err
	}
	for _, source := range sources {
		if source.Name == name && source.Effective {
			return source, nil
		}
	}
	return Source{}, fmt.Errorf("workflow %q not found in %s", name, directory)
}

func discoverDirectory(directory, scope string) ([]Source, error) {
	files, err := discoverWorkflowFiles(directory)
	if err != nil {
		return nil, err
	}
	var sources []Source
	for _, file := range files {
		name := file.name
		definition, err := loadLocal(file.path)
		if err != nil {
			return nil, err
		}
		if file.packageDir != "" {
			if !ValidWorkflowName(definition.Name) {
				return nil, fmt.Errorf("installed package %s has invalid workflow name %q", file.packageDir, definition.Name)
			}
			name = definition.Name
		}
		targetNames := definition.TargetNames()
		if len(targetNames) == 0 {
			targetNames = []string{""}
		}
		for _, targetName := range targetNames {
			selected, err := definition.SelectTarget(targetName)
			if err != nil {
				return nil, fmt.Errorf("selecting workflow %q target %q: %w", name, targetName, err)
			}
			sources = append(sources, Source{
				Name: name, Target: targetName, Path: file.path, PackageDir: file.packageDir,
				PackageVersion: selected.PackageVersion,
				Description:    selected.Description, Invokable: selected.IsInvokable(),
				DependsOn: maps.Clone(selected.DependsOn), HasForm: selected.HasForm(), Scope: scope,
			})
		}
	}
	effective := make(map[string]string, len(sources))
	for i := range sources {
		path, exists := effective[sources[i].Name]
		if !exists {
			sources[i].Effective = true
			effective[sources[i].Name] = sources[i].Path
			continue
		}
		sources[i].Effective = path == sources[i].Path
	}
	slices.SortStableFunc(sources, func(a, b Source) int {
		if comparison := strings.Compare(a.Name, b.Name); comparison != 0 {
			return comparison
		}
		return strings.Compare(a.Target, b.Target)
	})
	return sources, nil
}

type discoveredWorkflowFile struct {
	name       string
	path       string
	packageDir string
}

func discoverWorkflowFiles(root string) ([]discoveredWorkflowFile, error) {
	var files []discoveredWorkflowFile
	var visit func(string, string) error
	visit = func(directory, relativeDirectory string) error {
		if packagePath, ok, err := installedPackageManifest(directory); err != nil {
			return err
		} else if ok {
			files = append(files, discoveredWorkflowFile{path: packagePath, packageDir: directory})
			return nil
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return fmt.Errorf("reading workflow directory %s: %w", directory, err)
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
				return fmt.Errorf("workflow %q is declared twice in %s (%s and %s)", name, directory, previous, entry.Name())
			}
			inDir[name] = entry.Name()
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			relativePath := filepath.Join(relativeDirectory, entry.Name())
			if err := visit(filepath.Join(directory, entry.Name()), relativePath); err != nil {
				return err
			}
		}
		for _, entryName := range inDir {
			relativePath := filepath.Join(relativeDirectory, entryName)
			name := strings.TrimSuffix(filepath.ToSlash(relativePath), filepath.Ext(relativePath))
			files = append(files, discoveredWorkflowFile{name: name, path: filepath.Join(root, relativePath)})
		}
		return nil
	}
	if err := visit(root, ""); err != nil {
		return nil, err
	}
	slices.SortStableFunc(files, func(a, b discoveredWorkflowFile) int { return strings.Compare(a.name, b.name) })
	return files, nil
}

func installedPackageManifest(directory string) (string, bool, error) {
	marker := filepath.Join(directory, WorkflowPackageMarkerName)
	info, err := os.Stat(marker)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("checking workflow package marker %s: %w", marker, err)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("workflow package marker %s is not a regular file", marker)
	}
	path, err := WorkflowPackageManifestPath(directory)
	if err != nil {
		return "", false, err
	}
	return path, true, nil
}

// Find returns the effective workflow matching name.
func Find(cwd, homeDir, configDir, name string) (Source, error) {
	if !ValidWorkflowSelector(name) {
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
