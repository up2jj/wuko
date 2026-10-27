package workflow

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/validation"
)

// maxGitHubWorkflowEntries bounds a GitHub workflow snapshot. Snapshots stream to disk instead of
// memory, so a repository-wide picker may hold more entries than an action package; the
// decompressed archive limit still bounds the bytes written.
const maxGitHubWorkflowEntries = 20 * maxEntries

func (loader *Loader) fetchGitHubWorkflowFile(ctx context.Context, locator GitHubWorkflowLocator, options LoadOptions) ([]byte, string, error) {
	remoteURL, err := githubWorkflowContentsURL(locator)
	if err != nil {
		return nil, "", err
	}
	response, err := loader.open(ctx, remoteURL, githubHeaders(loader.githubWorkflowToken(ctx, options), "application/vnd.github.raw+json"), "workflow")
	if err != nil {
		return nil, "", githubWorkflowFetchError(locator, response, err)
	}
	defer response.Body.Close()
	payload, err := readResponseBody(response, remoteURL, "workflow")
	if err != nil {
		return nil, "", err
	}
	return payload, diagnostic.SafeURLString(locator.original), nil
}

// snapshotGitHubRepositoryScope streams the files under scope ("" for the whole repository) of one
// repository archive into a new temporary directory, so memory stays bounded by the copy buffer
// rather than the snapshot size. It returns the directory, its cleanup, and the bytes written.
func (loader *Loader) snapshotGitHubRepositoryScope(ctx context.Context, locator GitHubWorkflowLocator, scope string, options LoadOptions) (string, func(), int64, error) {
	response, err := loader.openGitHubTarball(ctx, locator.owner, locator.repository, locator.ref, loader.githubWorkflowToken(ctx, options), "GitHub workflow repository archive")
	if err != nil {
		return "", nil, 0, githubWorkflowFetchError(locator, response, err)
	}
	defer response.Body.Close()
	directory, err := os.MkdirTemp("", "wuko-github-")
	if err != nil {
		return "", nil, 0, fmt.Errorf("creating temporary workflow directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	var written int64
	err = walkGitHubArchiveScope(response.Body, githubArchiveScope{
		repository: locator.repositoryString(), directory: scope, label: "GitHub workflow", maxEntries: maxGitHubWorkflowEntries,
	}, func(name string, mode os.FileMode, content io.Reader) error {
		size, err := writeWorkflowFile(directory, name, mode, content)
		written += size
		return err
	})
	if err != nil {
		cleanup()
		return "", nil, 0, err
	}
	return directory, cleanup, written, nil
}

func (loader *Loader) githubWorkflowToken(ctx context.Context, options LoadOptions) string {
	environment := options.BaseEnv
	if environment == nil {
		environment = hostEnvironment()
	}
	return newGitHubCredentials(environment, loader.githubStoredTokenFn).token(ctx, "")
}

func githubWorkflowFetchError(locator GitHubWorkflowLocator, response *http.Response, err error) error {
	return githubFetchError("GitHub workflow repository "+locator.repositoryString(),
		"set GH_TOKEN/GITHUB_TOKEN or authenticate gh", "set GH_TOKEN/GITHUB_TOKEN or authenticate gh", response, err)
}

// DecodeMaterializedRemote decodes a workflow from a temporary remote repository snapshot.
// The caller owns and cleans up the snapshot directory.
func (loader *Loader) DecodeMaterializedRemote(ctx context.Context, filename, logicalSource string, options LoadOptions) (*Definition, error) {
	if PluginLockPolicyFromContext(ctx).Frozen {
		return nil, fmt.Errorf("--frozen-lockfile is only supported for local workflows; save and lock the workflow locally")
	}
	options.remoteOrigin = true
	options.sourceRoot = filepath.Dir(filename)
	options.sourceLabel = logicalSource
	return loader.decode(ctx, filename, options)
}

// LoadMaterializedRemote decodes, selects, and prepares a workflow in a temporary remote snapshot.
func (loader *Loader) LoadMaterializedRemote(ctx context.Context, filename, logicalSource string, options LoadOptions) (*Definition, error) {
	if PluginLockPolicyFromContext(ctx).Frozen {
		return nil, fmt.Errorf("--frozen-lockfile is only supported for local workflows; save and lock the workflow locally")
	}
	options.remoteOrigin = true
	options.sourceRoot = filepath.Dir(filename)
	options.sourceLabel = logicalSource
	return loader.Load(ctx, filename, options)
}

// DiscoverGitHubWorkflows downloads one repository snapshot and discovers package-root
// wuko.yaml/wuko.yml manifests below the selected repository or tree URL.
func (loader *Loader) DiscoverGitHubWorkflows(ctx context.Context, raw string, options LoadOptions) ([]Source, func(), error) {
	if PluginLockPolicyFromContext(ctx).Frozen {
		return nil, func() {}, fmt.Errorf("--frozen-lockfile is only supported for local workflows; save and lock the workflow locally")
	}
	locator, recognized, err := ParseGitHubWorkflowLocator(raw)
	if err != nil {
		return nil, func() {}, err
	}
	if !recognized {
		return nil, func() {}, fmt.Errorf("workflow picker source must be a GitHub repository or tree locator")
	}
	if locator.kind == githubWorkflowBlob && !strings.HasPrefix(locator.original, "github:") {
		return nil, func() {}, fmt.Errorf("GitHub blob locator %q selects one workflow; omit --pick to run it directly", raw)
	}
	scope := ""
	if (locator.kind == githubWorkflowTree || locator.kind == githubWorkflowBlob) && locator.path != "." {
		scope = locator.path
	}
	directory, cleanup, _, err := loader.snapshotGitHubRepositoryScope(ctx, locator, scope, options)
	if err != nil {
		return nil, func() {}, err
	}
	sources, err := loader.discoverGitHubSnapshot(directory, scope, locator, options)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return sources, cleanup, nil
}

// discoverGitHubSnapshot lists the package-root manifests of a snapshot. A manifest inside another
// workflow's directory belongs to that package (a fixture or vendored example) and is skipped; the
// snapshot root is exempt because a repository-root workflow runs without companion files.
// Manifests that fail validation are kept as non-invokable sources, so one broken workflow does not
// hide the others.
func (loader *Loader) discoverGitHubSnapshot(root, scope string, locator GitHubWorkflowLocator, options LoadOptions) ([]Source, error) {
	manifestDirectories := make(map[string]string)
	err := filepath.WalkDir(root, func(filename string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name := strings.ToLower(entry.Name())
		if name != "wuko.yaml" && name != "wuko.yml" {
			return nil
		}
		directory := filepath.Dir(filename)
		if previous, exists := manifestDirectories[directory]; exists {
			return fmt.Errorf("GitHub workflow directory %s contains both %s and %s", directory, filepath.Base(previous), entry.Name())
		}
		manifestDirectories[directory] = filename
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discovering GitHub workflows: %w", err)
	}
	var manifests []string
	for directory, manifest := range manifestDirectories {
		if !nestedInWorkflowPackage(directory, root, manifestDirectories) {
			manifests = append(manifests, manifest)
		}
	}
	slices.Sort(manifests)
	var sources []Source
	for _, manifest := range manifests {
		relative, err := filepath.Rel(root, manifest)
		if err != nil {
			return nil, err
		}
		repositoryPath := filepath.ToSlash(relative)
		if scope != "" {
			repositoryPath = path.Join(scope, repositoryPath)
		}
		canonical := locator.direct(repositoryPath).Canonical()
		definition, err := loadLocalWithDiagnostics(manifest, options.Diagnostics, filepath.Dir(manifest), canonical)
		if err != nil {
			if !validation.Is(err) {
				return nil, fmt.Errorf("discovering %s: %w", canonical, err)
			}
			sources = append(sources, Source{Name: path.Dir(repositoryPath), Path: manifest, Locator: canonical, Scope: "remote"})
			continue
		}
		targets := definition.TargetNames()
		if len(targets) == 0 {
			targets = []string{""}
		}
		for _, target := range targets {
			selected, err := definition.SelectTarget(target)
			if err != nil {
				return nil, err
			}
			sources = append(sources, Source{
				Name: definition.Name, Target: target, Path: manifest, Locator: canonical, Scope: "remote", Effective: true,
				PackageVersion: selected.PackageVersion, Description: selected.Description, WorkflowDescription: definition.Description, Invokable: selected.IsInvokable(),
				DependsOn: maps.Clone(selected.DependsOn), HasForm: selected.HasForm(),
			})
		}
	}
	return sources, nil
}

// nestedInWorkflowPackage reports whether directory lies below another manifest directory other
// than root.
func nestedInWorkflowPackage(directory, root string, manifestDirectories map[string]string) bool {
	if directory == root {
		return false
	}
	for parent := filepath.Dir(directory); parent != root && parent != directory; directory, parent = parent, filepath.Dir(parent) {
		if _, exists := manifestDirectories[parent]; exists {
			return true
		}
	}
	return false
}
