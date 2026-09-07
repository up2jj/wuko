package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	pluginpkg "github.com/up2jj/wuko/plugin"
	"github.com/up2jj/wuko/workflow"
)

func newMarketplaceCmd(deps dependencies) *cobra.Command {
	marketplace := &cobra.Command{
		Use:   "marketplace",
		Short: "Create and build workflow and plugin marketplaces",
	}
	marketplace.AddCommand(newMarketplaceInitCmd(deps), newMarketplacePluginCmd(deps), newMarketplaceBuildCmd(deps))
	return marketplace
}

func newMarketplaceInitCmd(deps dependencies) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize a marketplace manifest in the current directory",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cwd, err := deps.cwd()
			if err != nil {
				return fmt.Errorf("finding current directory: %w", err)
			}
			manifestPath := filepath.Join(cwd, "manifest.json")
			if _, err := os.Stat(manifestPath); err == nil {
				return fmt.Errorf("marketplace manifest already exists at %s", manifestPath)
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("checking marketplace manifest %s: %w", manifestPath, err)
			}
			for _, directory := range []string{filepath.Join(cwd, ".wuko", "workflows"), marketplacePluginSourceRoot(cwd)} {
				if err := os.MkdirAll(directory, 0o755); err != nil {
					return fmt.Errorf("creating marketplace source directory %s: %w", directory, err)
				}
			}
			manifest := workflow.MarketplaceManifest{Version: workflow.MarketplaceManifestVersion, Packages: []workflow.MarketplacePackage{}, Plugins: []workflow.MarketplacePluginPackage{}}
			if err := writeJSONAtomically(manifestPath, manifest); err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "created marketplace manifest in %s\n", manifestPath)
			return err
		},
	}
}

func newMarketplaceBuildCmd(deps dependencies) *cobra.Command {
	var check bool
	command := &cobra.Command{
		Use:     "build",
		Short:   "Publish workflows and plugins and rebuild the marketplace manifest",
		Example: "  wuko marketplace build\n  wuko marketplace build --check",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cwd, err := deps.cwd()
			if err != nil {
				return fmt.Errorf("finding current directory: %w", err)
			}
			build, err := buildMarketplaceManifest(cwd, deps.loader, diagnosticsFor(command, deps, cwd))
			if err != nil {
				return err
			}
			defer build.cleanup()
			manifestPath := filepath.Join(cwd, "manifest.json")
			if check {
				if build.changed {
					return fmt.Errorf("marketplace is not up to date; run wuko marketplace build")
				}
				_, err = fmt.Fprintf(command.OutOrStdout(), "marketplace is up to date with %s and %s\n", countMarketplaceItems(len(build.manifest.Packages), "workflow package"), countMarketplaceItems(len(build.manifest.Plugins), "plugin"))
				return err
			}
			if build.changed {
				if err := publishMarketplaceBuild(cwd, manifestPath, build); err != nil {
					return err
				}
			}
			message := "built marketplace manifest"
			if !build.changed {
				message = "marketplace is already up to date"
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "%s with %s and %s in %s\n", message, countMarketplaceItems(len(build.manifest.Packages), "workflow package"), countMarketplaceItems(len(build.manifest.Plugins), "plugin"), manifestPath)
			return err
		},
	}
	command.Flags().BoolVar(&check, "check", false, "fail when generated marketplace files are not up to date")
	return command
}

// countMarketplaceItems renders a count with its noun, so a marketplace holding
// exactly one plugin does not report "1 plugins".
func countMarketplaceItems(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

type marketplaceBuild struct {
	manifest     workflow.MarketplaceManifest
	changed      bool
	stagingDir   string
	replacements map[string]string
	stale        []string
}

type marketplacePublicationFile struct {
	target    string
	staged    string
	backup    string
	backedUp  bool
	installed bool
}

func (build marketplaceBuild) cleanup() {
	if build.stagingDir != "" {
		_ = os.RemoveAll(build.stagingDir)
	}
}

func publishMarketplaceBuild(cwd, manifestPath string, build marketplaceBuild) error {
	stagingDir := build.stagingDir
	if stagingDir == "" {
		var err error
		stagingDir, err = os.MkdirTemp(cwd, ".wuko-marketplace-build-*")
		if err != nil {
			return fmt.Errorf("creating marketplace staging directory: %w", err)
		}
		defer os.RemoveAll(stagingDir)
	}
	stagedManifest := filepath.Join(stagingDir, "manifest.json")
	if err := writeJSONAtomically(stagedManifest, build.manifest); err != nil {
		return fmt.Errorf("staging marketplace manifest: %w", err)
	}

	replacementPaths := slices.Sorted(maps.Keys(build.replacements))
	stalePaths := slices.Clone(build.stale)
	slices.Sort(stalePaths)
	files := make([]marketplacePublicationFile, 0, len(replacementPaths)+len(stalePaths)+1)
	for _, relative := range replacementPaths {
		files = append(files, marketplacePublicationFile{
			target: filepath.Join(cwd, filepath.FromSlash(relative)),
			staged: build.replacements[relative],
		})
	}
	for _, relative := range stalePaths {
		files = append(files, marketplacePublicationFile{target: filepath.Join(cwd, filepath.FromSlash(relative))})
	}
	files = append(files, marketplacePublicationFile{target: manifestPath, staged: stagedManifest})

	backupDir, err := os.MkdirTemp(cwd, ".wuko-marketplace-backup-*")
	if err != nil {
		return fmt.Errorf("creating marketplace backup directory: %w", err)
	}
	defer os.RemoveAll(backupDir)
	for index := range files {
		files[index].backup = filepath.Join(backupDir, fmt.Sprintf("%d", index))
	}

	rollback := func(cause error) error {
		var rollbackErr error
		for index := len(files) - 1; index >= 0; index-- {
			file := &files[index]
			if file.installed {
				if err := os.Remove(file.target); err != nil && !os.IsNotExist(err) {
					rollbackErr = errors.Join(rollbackErr, fmt.Errorf("removing unpublished file %s: %w", file.target, err))
					continue
				}
			}
			if file.backedUp {
				if err := os.MkdirAll(filepath.Dir(file.target), 0o755); err != nil {
					rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restoring directory for %s: %w", file.target, err))
					continue
				}
				if err := os.Rename(file.backup, file.target); err != nil {
					rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restoring %s: %w", file.target, err))
				}
			}
		}
		return errors.Join(cause, rollbackErr)
	}

	for index := range files {
		file := &files[index]
		info, err := os.Stat(file.target)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return rollback(fmt.Errorf("checking published marketplace file %s: %w", file.target, err))
		}
		if !info.Mode().IsRegular() {
			return rollback(fmt.Errorf("published marketplace file %s is not regular", file.target))
		}
		if err := os.Rename(file.target, file.backup); err != nil {
			return rollback(fmt.Errorf("backing up marketplace file %s: %w", file.target, err))
		}
		file.backedUp = true
	}
	for index := range files {
		file := &files[index]
		if file.staged == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(file.target), 0o755); err != nil {
			return rollback(fmt.Errorf("creating marketplace publication directory for %s: %w", file.target, err))
		}
		if err := os.Rename(file.staged, file.target); err != nil {
			return rollback(fmt.Errorf("publishing marketplace file %s: %w", file.target, err))
		}
		file.installed = true
	}
	if err := syncMarketplacePublicationDirectories(files); err != nil {
		return rollback(err)
	}
	return nil
}

func syncMarketplacePublicationDirectories(files []marketplacePublicationFile) error {
	directories := make(map[string]struct{}, len(files))
	for _, file := range files {
		directories[filepath.Dir(file.target)] = struct{}{}
	}
	for _, directory := range slices.Sorted(maps.Keys(directories)) {
		handle, err := os.Open(directory)
		if err != nil {
			return fmt.Errorf("opening marketplace publication directory %s: %w", directory, err)
		}
		syncErr := handle.Sync()
		closeErr := handle.Close()
		if syncErr != nil {
			return fmt.Errorf("syncing marketplace publication directory %s: %w", directory, syncErr)
		}
		if closeErr != nil {
			return fmt.Errorf("closing marketplace publication directory %s: %w", directory, closeErr)
		}
	}
	return nil
}

func buildMarketplaceManifest(cwd string, loader *workflow.Loader, reporter diagnostic.Reporter) (result marketplaceBuild, resultErr error) {
	if loader == nil {
		loader = workflow.NewLoader(nil)
	}
	result.replacements = make(map[string]string)
	var cleanupDir string
	defer func() {
		if resultErr != nil && cleanupDir != "" {
			_ = os.RemoveAll(cleanupDir)
		}
	}()
	workflowRoot := filepath.Join(cwd, ".wuko", "workflows")
	manifest := workflow.MarketplaceManifest{Version: workflow.MarketplaceManifestVersion, Packages: []workflow.MarketplacePackage{}, Plugins: []workflow.MarketplacePluginPackage{}}
	manifestPath := filepath.Join(cwd, "manifest.json")
	previous, hasPrevious, err := readMarketplaceManifest(manifestPath)
	if err != nil {
		return marketplaceBuild{}, err
	}
	ensureStaging := func() error {
		if result.stagingDir != "" {
			return nil
		}
		result.stagingDir, err = os.MkdirTemp(cwd, ".wuko-marketplace-build-*")
		if err != nil {
			return fmt.Errorf("creating marketplace staging directory: %w", err)
		}
		cleanupDir = result.stagingDir
		return nil
	}
	var sourcePackages []marketplaceSourcePackage
	if _, statErr := os.Stat(workflowRoot); statErr == nil {
		sourcePackages, err = discoverMarketplacePackages(workflowRoot)
		if err != nil {
			return marketplaceBuild{}, err
		}
	} else if !os.IsNotExist(statErr) {
		return marketplaceBuild{}, fmt.Errorf("checking workflow directory %s: %w", workflowRoot, statErr)
	}
	seenNames := make(map[string]string)
	previousBySource := make(map[string]workflow.MarketplacePackage, len(previous.Packages))
	if hasPrevious {
		for _, item := range previous.Packages {
			previousBySource[item.Source] = item
		}
	}
	for _, sourcePackage := range sourcePackages {
		definition, err := loader.Decode(sourcePackage.manifestPath, workflow.LoadOptions{RunDir: cwd, Diagnostics: reporter})
		if err != nil {
			return marketplaceBuild{}, err
		}
		if !workflow.ValidWorkflowName(definition.Name) {
			return marketplaceBuild{}, fmt.Errorf("workflow package %s has name %q, which cannot be used by a marketplace install", sourcePackage.directory, definition.Name)
		}
		if previous, exists := seenNames[definition.Name]; exists {
			return marketplaceBuild{}, fmt.Errorf("workflow name %q is declared by both %s and %s", definition.Name, previous, sourcePackage.directory)
		}
		seenNames[definition.Name] = sourcePackage.directory
		sourcePath, err := filepath.Rel(cwd, sourcePackage.directory)
		if err != nil {
			return marketplaceBuild{}, fmt.Errorf("relating workflow package %s: %w", sourcePackage.directory, err)
		}
		sourcePath = filepath.ToSlash(sourcePath)
		sourceDigest, err := workflow.WorkflowPackageDigest(sourcePackage.directory)
		if err != nil {
			return marketplaceBuild{}, fmt.Errorf("digesting workflow package %s: %w", sourcePackage.directory, err)
		}
		archivePath := filepath.ToSlash(filepath.Join("packages", definition.Name+".tar.gz"))
		item := workflow.MarketplacePackage{
			Name: definition.Name, PackageVersion: definition.PackageVersion, Source: sourcePath, Path: archivePath,
			Format: "tar.gz", Entry: "wuko.yaml", Description: definition.Description, SourceSHA256: sourceDigest,
		}
		old, reused := previousBySource[sourcePath]
		archiveFile := filepath.Join(cwd, filepath.FromSlash(archivePath))
		if reused && old.SourceSHA256 == sourceDigest && old.Path == archivePath && old.Format == item.Format && old.Entry == item.Entry && fileDigestMatches(archiveFile, old.SHA256) {
			item.SHA256 = old.SHA256
		} else {
			if err := ensureStaging(); err != nil {
				return marketplaceBuild{}, err
			}
			stagedArchive := filepath.Join(result.stagingDir, definition.Name+".tar.gz")
			_, archiveDigest, err := workflow.BuildWorkflowPackage(sourcePackage.directory, stagedArchive)
			if err != nil {
				return marketplaceBuild{}, fmt.Errorf("building workflow package %s: %w", sourcePackage.directory, err)
			}
			item.SHA256 = archiveDigest
			result.replacements[archivePath] = stagedArchive
		}
		manifest.Packages = append(manifest.Packages, item)
	}
	published, err := addMarketplacePlugins(cwd, &manifest, previous, &result, ensureStaging)
	if err != nil {
		return marketplaceBuild{}, err
	}
	slices.SortStableFunc(manifest.Packages, func(a, b workflow.MarketplacePackage) int {
		if comparison := strings.Compare(a.Path, b.Path); comparison != 0 {
			return comparison
		}
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortStableFunc(manifest.Plugins, func(a, b workflow.MarketplacePluginPackage) int {
		return strings.Compare(a.Namespace, b.Namespace)
	})
	if err := workflow.ValidateMarketplaceManifest(manifest); err != nil {
		return marketplaceBuild{}, fmt.Errorf("generated marketplace manifest is invalid: %w", err)
	}
	result.manifest = manifest
	result.changed = !hasPrevious || !reflect.DeepEqual(previous, manifest) || len(result.replacements) > 0
	if result.changed {
		result.stale = staleMarketplaceFiles(cwd, previous, manifest, published)
	}
	return result, nil
}

// addMarketplacePlugins appends every imported plugin release to manifest and returns the complete
// set of published file paths it is responsible for, so stale detection never has to reload and
// revalidate the same bundles.
func addMarketplacePlugins(cwd string, manifest *workflow.MarketplaceManifest, previous workflow.MarketplaceManifest, result *marketplaceBuild, ensureStaging func() error) (map[string]struct{}, error) {
	published := make(map[string]struct{})
	root := marketplacePluginSourceRoot(cwd)
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return published, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading marketplace plugin directory %s: %w", root, err)
	}
	previousBySource := make(map[string]workflow.MarketplacePluginPackage, len(previous.Plugins))
	for _, item := range previous.Plugins {
		previousBySource[item.Source] = item
	}
	for _, entry := range entries {
		directory := filepath.Join(root, entry.Name())
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			return nil, fmt.Errorf("marketplace plugin entry %s is not a directory", directory)
		}
		if !workflow.ValidPluginNamespace(entry.Name()) {
			return nil, fmt.Errorf("marketplace plugin directory has invalid namespace %q", entry.Name())
		}
		bundle, err := pluginpkg.LoadBundle(directory)
		if err != nil {
			return nil, fmt.Errorf("loading marketplace plugin %s: %w", entry.Name(), err)
		}
		if bundle.Manifest.Namespace != entry.Name() {
			return nil, fmt.Errorf("marketplace plugin directory %q conflicts with manifest namespace %q", entry.Name(), bundle.Manifest.Namespace)
		}
		metadata, err := readMarketplacePluginSource(directory)
		if err != nil {
			return nil, fmt.Errorf("loading marketplace plugin %s: %w", entry.Name(), err)
		}
		sourcePath, err := filepath.Rel(cwd, directory)
		if err != nil {
			return nil, fmt.Errorf("relating marketplace plugin %s: %w", entry.Name(), err)
		}
		sourcePath = filepath.ToSlash(sourcePath)
		publishedManifest := filepath.ToSlash(filepath.Join("plugins", entry.Name(), "plugin.json"))
		platforms := make([]workflow.MarketplacePlatform, 0, len(bundle.Manifest.Artifacts))
		for _, artifact := range bundle.Manifest.Artifacts {
			platforms = append(platforms, workflow.MarketplacePlatform{OS: artifact.OS, Arch: artifact.Arch})
		}
		slices.SortStableFunc(platforms, func(a, b workflow.MarketplacePlatform) int {
			if comparison := strings.Compare(a.OS, b.OS); comparison != 0 {
				return comparison
			}
			return strings.Compare(a.Arch, b.Arch)
		})
		item := workflow.MarketplacePluginPackage{
			Namespace: entry.Name(), PluginVersion: bundle.Manifest.PluginVersion, Description: metadata.Description,
			Source: sourcePath, SourceSHA256: pluginpkg.DigestBundle(bundle), Path: publishedManifest,
			SHA256: bundle.ManifestDigest, Platforms: platforms,
		}
		files := map[string][]byte{publishedManifest: bundle.ManifestData}
		artifactsMatch := true
		for _, artifact := range bundle.Manifest.Artifacts {
			publishedPath := filepath.ToSlash(filepath.Join("plugins", entry.Name(), filepath.FromSlash(artifact.Path)))
			files[publishedPath] = bundle.ArtifactData[artifact.Path]
			if artifactsMatch && !fileDigestMatches(filepath.Join(cwd, filepath.FromSlash(publishedPath)), artifact.SHA256) {
				artifactsMatch = false
			}
		}
		for relative := range files {
			published[relative] = struct{}{}
		}
		old, reused := previousBySource[sourcePath]
		manifestMatches := fileDigestMatches(filepath.Join(cwd, filepath.FromSlash(publishedManifest)), bundle.ManifestDigest)
		if !(reused && reflect.DeepEqual(old, item) && manifestMatches && artifactsMatch) {
			if err := ensureStaging(); err != nil {
				return nil, err
			}
			for relative, data := range files {
				if fileDigestMatches(filepath.Join(cwd, filepath.FromSlash(relative)), digestBytes(data)) {
					continue
				}
				staged := filepath.Join(result.stagingDir, filepath.FromSlash(relative))
				if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
					return nil, fmt.Errorf("creating staged plugin directory: %w", err)
				}
				if err := os.WriteFile(staged, data, 0o644); err != nil {
					return nil, fmt.Errorf("staging marketplace plugin file %s: %w", relative, err)
				}
				result.replacements[relative] = staged
			}
		}
		manifest.Plugins = append(manifest.Plugins, item)
	}
	return published, nil
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:])
}

type marketplaceSourcePackage struct {
	directory    string
	manifestPath string
}

func discoverMarketplacePackages(root string) ([]marketplaceSourcePackage, error) {
	var packages []marketplaceSourcePackage
	var visit func(string) error
	visit = func(directory string) error {
		manifestPath, err := workflow.WorkflowPackageManifestPath(directory)
		if err == nil {
			packages = append(packages, marketplaceSourcePackage{directory: directory, manifestPath: manifestPath})
			return nil
		} else if !errors.Is(err, workflow.ErrWorkflowPackageManifestNotFound) {
			return err
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return fmt.Errorf("reading marketplace workflow directory %s: %w", directory, err)
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("marketplace workflow directory contains symlink %s", filepath.Join(directory, entry.Name()))
			}
			if entry.IsDir() {
				if err := visit(filepath.Join(directory, entry.Name())); err != nil {
					return err
				}
				continue
			}
			if entry.Type().IsRegular() {
				return fmt.Errorf("marketplace package directory %s has no root wuko.yaml or wuko.yml", directory)
			}
			return fmt.Errorf("marketplace workflow entry %s is not a regular file or directory", filepath.Join(directory, entry.Name()))
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, fmt.Errorf("discovering marketplace packages: %w", err)
	}
	slices.SortStableFunc(packages, func(a, b marketplaceSourcePackage) int { return strings.Compare(a.directory, b.directory) })
	return packages, nil
}

func readMarketplaceManifest(filename string) (workflow.MarketplaceManifest, bool, error) {
	data, err := os.ReadFile(filename)
	if os.IsNotExist(err) {
		return workflow.MarketplaceManifest{}, false, nil
	}
	if err != nil {
		return workflow.MarketplaceManifest{}, false, fmt.Errorf("reading marketplace manifest %s: %w", filename, err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var manifest workflow.MarketplaceManifest
	if err := decoder.Decode(&manifest); err != nil {
		return workflow.MarketplaceManifest{}, false, fmt.Errorf("decoding marketplace manifest %s: %w", filename, err)
	}
	if err := workflow.ValidateMarketplaceManifest(manifest); err != nil {
		return workflow.MarketplaceManifest{}, false, fmt.Errorf("validating marketplace manifest %s: %w", filename, err)
	}
	return manifest, true, nil
}

func fileDigestMatches(filename, expected string) bool {
	data, err := os.ReadFile(filename)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(data)
	return strings.EqualFold(fmt.Sprintf("%x", digest[:]), expected)
}

func staleMarketplaceFiles(cwd string, previous, current workflow.MarketplaceManifest, publishedPlugins map[string]struct{}) []string {
	var stale []string
	currentPaths := make(map[string]struct{}, len(current.Packages)+len(publishedPlugins))
	for _, item := range current.Packages {
		currentPaths[item.Path] = struct{}{}
	}
	maps.Copy(currentPaths, publishedPlugins)
	for _, item := range previous.Packages {
		if _, exists := currentPaths[item.Path]; exists || !strings.HasPrefix(item.Path, "packages/") {
			continue
		}
		filename := filepath.Join(cwd, filepath.FromSlash(item.Path))
		if !fileDigestMatches(filename, item.SHA256) {
			continue
		}
		stale = append(stale, item.Path)
	}
	for _, item := range previous.Plugins {
		if !strings.HasPrefix(item.Path, "plugins/") {
			continue
		}
		publishedManifest := filepath.Join(cwd, filepath.FromSlash(item.Path))
		if _, exists := currentPaths[item.Path]; !exists && fileDigestMatches(publishedManifest, item.SHA256) {
			stale = append(stale, item.Path)
		}
		data, err := os.ReadFile(publishedManifest)
		if err != nil || digestBytes(data) != item.SHA256 {
			continue
		}
		pluginManifest, err := pluginpkg.ParseManifest(data)
		if err != nil {
			continue
		}
		for _, artifact := range pluginManifest.Artifacts {
			relative := filepath.ToSlash(filepath.Join(filepath.Dir(item.Path), filepath.FromSlash(artifact.Path)))
			if _, exists := currentPaths[relative]; exists || !strings.HasPrefix(relative, "plugins/") {
				continue
			}
			if fileDigestMatches(filepath.Join(cwd, filepath.FromSlash(relative)), artifact.SHA256) {
				stale = append(stale, relative)
			}
		}
	}
	slices.Sort(stale)
	return stale
}

func writeJSONAtomically(path string, value any) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("creating temporary file for %s: %w", path, err)
	}
	temporary := file.Name()
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o644); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", path, err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	removeTemporary = false
	directoryFile, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("opening marketplace directory: %w", err)
	}
	defer directoryFile.Close()
	if err := directoryFile.Sync(); err != nil {
		return fmt.Errorf("syncing marketplace directory: %w", err)
	}
	return nil
}
