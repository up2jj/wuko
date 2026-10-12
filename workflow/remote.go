package workflow

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/up2jj/wuko/diagnostic"
)

const (
	defaultRemoteWorkflowFile = "wuko.yaml"
	githubAPIBase             = "https://api.github.com"
)

// IsRemoteLocator reports whether locator selects a workflow outside the local discovery paths.
func IsRemoteLocator(locator string) bool {
	return strings.HasPrefix(locator, "https://") || strings.HasPrefix(locator, "http://") || strings.HasPrefix(locator, "github:")
}

// DecodeRemote fetches and decodes a workflow without preparing its composite actions.
// The returned cleanup function must be called after preparation and execution finish.
func (loader *Loader) DecodeRemote(ctx context.Context, locator string, options LoadOptions) (*Definition, func(), error) {
	location := diagnostic.Location{Source: diagnostic.SafeURLString(locator)}
	fetchStarted := traceStart(options.Diagnostics, diagnostic.PhaseWorkflowFetch, location, "", "", "", "fetching remote workflow")
	path, description, cleanup, bytesFetched, err := loader.materializeRemoteLocator(ctx, locator, options)
	if err != nil {
		traceFinish(options.Diagnostics, fetchStarted, diagnostic.PhaseWorkflowFetch, diagnostic.StatusFailed, location, "", "", "", "", err)
		return nil, func() {}, err
	}
	location.Source = description
	traceFinish(options.Diagnostics, fetchStarted, diagnostic.PhaseWorkflowFetch, diagnostic.StatusSucceeded, location, "", "", "", "", nil, countAttr("bytes", bytesFetched))
	transferred := false
	defer func() {
		if !transferred {
			cleanup()
		}
	}()
	options.sourceRoot = filepath.Dir(path)
	options.sourceLabel = description
	definition, err := loader.decode(ctx, path, options)
	if err != nil {
		return nil, func() {}, err
	}
	remapDefinitionLocations(definition, filepath.Dir(path), description)
	transferred = true
	return definition, cleanup, nil
}

func (loader *Loader) materializeRemoteLocator(ctx context.Context, raw string, options LoadOptions) (string, string, func(), int, error) {
	locator, recognized, err := ParseGitHubWorkflowLocator(raw)
	if err != nil {
		return "", "", func() {}, 0, err
	}
	if recognized {
		if locator.isTree() {
			return "", "", func() {}, 0, fmt.Errorf("GitHub tree locator %q selects a directory; use wuko run --pick", raw)
		}
		// A repository-root workflow would make the whole repository its package, so it is
		// fetched alone through the contents API, as is any workflow when archives are rejected.
		directory := path.Dir(locator.path)
		if directory == "." || options.RejectRemoteArchives {
			payload, description, err := loader.fetchGitHubWorkflowFile(ctx, locator, options)
			if err != nil {
				return "", "", func() {}, 0, err
			}
			workflowPath, cleanup, err := materializeRemoteWorkflow(payload, description, options.RejectRemoteArchives)
			if err != nil {
				return "", "", func() {}, 0, fmt.Errorf("materializing workflow %s: %w", description, err)
			}
			return workflowPath, description, cleanup, len(payload), nil
		}
		snapshot, cleanup, written, err := loader.snapshotGitHubRepositoryScope(ctx, locator, directory, options)
		if err != nil {
			return "", "", func() {}, 0, err
		}
		workflowPath := filepath.Join(snapshot, filepath.FromSlash(path.Base(locator.path)))
		if info, err := os.Stat(workflowPath); err != nil || !info.Mode().IsRegular() {
			cleanup()
			return "", "", func() {}, 0, fmt.Errorf("GitHub workflow %s does not contain %s", locator.repositoryString(), locator.path)
		}
		return workflowPath, diagnostic.SafeURLString(raw), cleanup, int(written), nil
	}
	payload, description, err := loader.fetchWorkflow(ctx, raw)
	if err != nil {
		return "", "", func() {}, 0, err
	}
	workflowPath, cleanup, err := materializeRemoteWorkflow(payload, description, options.RejectRemoteArchives)
	if err != nil {
		return "", "", func() {}, 0, fmt.Errorf("materializing workflow %s: %w", description, err)
	}
	return workflowPath, description, cleanup, len(payload), nil
}

// LoadMarketplacePackage downloads, verifies, materializes, and decodes one marketplace
// package without preparing its composite actions. The returned directory contains the complete
// package tree and must be released with cleanup after it has been copied or otherwise consumed.
func (loader *Loader) LoadMarketplacePackage(ctx context.Context, baseURL string, item MarketplacePackage, options LoadOptions) (*Definition, string, func(), error) {
	if !ValidWorkflowName(item.Name) {
		return nil, "", func() {}, fmt.Errorf("marketplace package name %q is invalid", item.Name)
	}
	if item.Format != "tar.gz" {
		return nil, "", func() {}, fmt.Errorf("marketplace package %q has unsupported format %q", item.Name, item.Format)
	}
	if item.Entry != defaultRemoteWorkflowFile {
		return nil, "", func() {}, fmt.Errorf("marketplace package %q must use entry %q", item.Name, defaultRemoteWorkflowFile)
	}
	packageURL, err := ResolveMarketplacePackage(baseURL, item)
	if err != nil {
		return nil, "", func() {}, err
	}
	remoteURL, err := url.Parse(packageURL)
	if err != nil {
		return nil, "", func() {}, fmt.Errorf("parsing marketplace package URL: %w", err)
	}
	payload, _, err := loader.fetchWithHeadersStatus(ctx, remoteURL, nil, "marketplace package")
	if err != nil {
		return nil, "", func() {}, err
	}
	if err := verifyChecksum(payload, item.SHA256); err != nil {
		return nil, "", func() {}, fmt.Errorf("verifying marketplace package %q: %w", item.Name, err)
	}
	workflowPath, cleanup, err := materializeRemoteWorkflowPackage(payload, packageURL)
	if err != nil {
		return nil, "", func() {}, fmt.Errorf("materializing marketplace package %q: %w", item.Name, err)
	}
	transferred := false
	defer func() {
		if !transferred {
			cleanup()
		}
	}()
	options.sourceRoot = filepath.Dir(workflowPath)
	options.sourceLabel = packageURL
	definition, err := loader.Decode(workflowPath, options)
	if err != nil {
		return nil, "", func() {}, err
	}
	if definition.Name != item.Name {
		return nil, "", func() {}, fmt.Errorf("marketplace package %q declares workflow name %q", item.Name, definition.Name)
	}
	if definition.PackageVersion != item.PackageVersion {
		return nil, "", func() {}, fmt.Errorf("marketplace package %q declares package_version %q, manifest has %q", item.Name, definition.PackageVersion, item.PackageVersion)
	}
	remapDefinitionLocations(definition, filepath.Dir(workflowPath), packageURL)
	transferred = true
	return definition, filepath.Dir(workflowPath), cleanup, nil
}

// LoadRemote fetches and loads a workflow from an HTTPS URL or a public GitHub locator.
// The returned cleanup function removes the temporary materialization directory and must be
// called after the workflow has finished executing.
func (loader *Loader) LoadRemote(ctx context.Context, locator string, options LoadOptions) (*Definition, func(), error) {
	location := diagnostic.Location{Source: diagnostic.SafeURLString(locator)}
	started := traceStart(options.Diagnostics, diagnostic.PhaseLoad, location, "", "", "", "loading remote workflow")
	definition, cleanup, err := loader.DecodeRemote(ctx, locator, options)
	if err != nil {
		traceFinish(options.Diagnostics, started, diagnostic.PhaseLoad, diagnostic.StatusFailed, location, "", "", "", "", nil)
		return nil, func() {}, err
	}
	transferred := false
	defer func() {
		if !transferred {
			cleanup()
		}
	}()
	targetName := options.Target
	if options.Lifecycle && targetName == "" && definition.HasTargets() {
		targetName = definition.TargetNames()[0]
	}
	selected, err := definition.SelectTarget(targetName)
	if err != nil {
		traceFinish(options.Diagnostics, started, diagnostic.PhaseLoad, diagnostic.StatusFailed, definition.Location, definition.Name, "", "", "", err)
		return nil, func() {}, err
	}
	definition = selected
	if err := loader.Prepare(ctx, definition, options); err != nil {
		traceFinish(options.Diagnostics, started, diagnostic.PhaseLoad, diagnostic.StatusFailed, definition.Location, definition.Name, "", "", "", nil)
		return nil, func() {}, err
	}
	traceFinish(options.Diagnostics, started, diagnostic.PhaseLoad, diagnostic.StatusSucceeded, definition.Location, definition.Name, "", "", "", nil, countAttr("steps", len(definition.Steps)))
	transferred = true
	return definition, cleanup, nil
}

func (loader *Loader) fetchWorkflow(ctx context.Context, locator string) ([]byte, string, error) {
	switch {
	case strings.HasPrefix(locator, "https://") || strings.HasPrefix(locator, "http://"):
		remoteURL, err := url.Parse(locator)
		if err != nil {
			return nil, "", fmt.Errorf("invalid workflow URL: %w", err)
		}
		if remoteURL.Scheme != "https" || remoteURL.Host == "" {
			return nil, "", fmt.Errorf("workflow URL must use HTTPS and include a host")
		}
		if remoteURL.User != nil {
			return nil, "", fmt.Errorf("workflow URL user information is not allowed")
		}
		payload, err := loader.fetchWithHeaders(ctx, remoteURL, nil, "workflow")
		return payload, safeURL(remoteURL), err
	default:
		return nil, "", fmt.Errorf("unsupported remote workflow locator %q", locator)
	}
}

func githubWorkflowURL(locator string) (*url.URL, error) {
	parsed, recognized, err := ParseGitHubWorkflowLocator(locator)
	if err != nil {
		return nil, err
	}
	if !recognized || parsed.isTree() {
		return nil, fmt.Errorf("invalid GitHub workflow locator %q", locator)
	}
	return githubWorkflowContentsURL(parsed)
}

func githubWorkflowContentsURL(parsed GitHubWorkflowLocator) (*url.URL, error) {
	segments := strings.Split(parsed.path, "/")
	encodedPath := make([]string, len(segments))
	for i, segment := range segments {
		encodedPath[i] = url.PathEscape(segment)
	}
	remoteURL, err := url.Parse(githubAPIBase + "/repos/" + url.PathEscape(parsed.owner) + "/" + url.PathEscape(parsed.repository) + "/contents/" + strings.Join(encodedPath, "/"))
	if err != nil {
		return nil, fmt.Errorf("building GitHub workflow URL: %w", err)
	}
	if parsed.ref != "" {
		query := remoteURL.Query()
		query.Set("ref", parsed.ref)
		remoteURL.RawQuery = query.Encode()
	}
	return remoteURL, nil
}

func validGitHubPart(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '.' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validateRemotePath(value string) error {
	if value == "" || strings.ContainsAny(value, "\\\x00") || path.IsAbs(value) || path.Clean(value) != value {
		return fmt.Errorf("path must be a relative safe path")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("path must be a relative safe path")
		}
	}
	return nil
}

func materializeRemoteWorkflow(payload []byte, description string, rejectArchives bool) (string, func(), error) {
	manifest, files, archive, err := decodeRemoteWorkflowPayload(payload)
	if err != nil {
		return "", nil, fmt.Errorf("decoding workflow %s: %w", description, err)
	}
	if rejectArchives && archive {
		return "", nil, fmt.Errorf("workflow archives are not supported for installation")
	}
	if files == nil {
		files = map[string]ActionFile{defaultRemoteWorkflowFile: {Data: manifest, Mode: 0o644}}
	}

	return materializeWorkflowFiles(defaultRemoteWorkflowFile, manifest, files, description)
}

func materializeRemoteWorkflowPackage(payload []byte, description string) (string, func(), error) {
	if len(payload) < 2 || payload[0] != 0x1f || payload[1] != 0x8b {
		return "", nil, fmt.Errorf("marketplace package must be a tar.gz archive")
	}
	manifest, files, archive, err := decodeRemoteWorkflowPayload(payload)
	if err != nil {
		return "", nil, fmt.Errorf("decoding workflow package %s: %w", description, err)
	}
	if !archive {
		return "", nil, fmt.Errorf("marketplace package must be a tar.gz archive")
	}
	return materializeWorkflowFiles(defaultRemoteWorkflowFile, manifest, files, description)
}

func materializeWorkflowFiles(entry string, manifest []byte, files map[string]ActionFile, description string) (string, func(), error) {
	if files == nil {
		files = map[string]ActionFile{defaultRemoteWorkflowFile: {Data: manifest, Mode: 0o644}}
	}

	directory, err := os.MkdirTemp("", "wuko-workflow-")
	if err != nil {
		return "", nil, fmt.Errorf("creating temporary workflow directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	for name, file := range files {
		if _, err := writeWorkflowFile(directory, name, file.Mode, bytes.NewReader(file.Data)); err != nil {
			cleanup()
			return "", nil, err
		}
	}

	workflowPath := filepath.Join(directory, filepath.FromSlash(entry))
	if _, err := os.Stat(workflowPath); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("locating materialized workflow: %w", err)
	}
	return workflowPath, cleanup, nil
}

// writeWorkflowFile writes one archive member below directory and returns the bytes written. The
// name is validated so it cannot escape directory, and an existing file is never overwritten.
func writeWorkflowFile(directory, name string, mode os.FileMode, content io.Reader) (int64, error) {
	if err := validateArchivePath(name); err != nil {
		return 0, err
	}
	target := filepath.Join(directory, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, fmt.Errorf("creating directory for %s: %w", name, err)
	}
	mode = mode.Perm()
	if mode == 0 {
		mode = 0o644
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return 0, fmt.Errorf("writing workflow file %s: %w", name, err)
	}
	written, err := io.Copy(file, content)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return written, fmt.Errorf("writing workflow file %s: %w", name, err)
	}
	return written, nil
}

func decodeRemoteWorkflowPayload(payload []byte) ([]byte, map[string]ActionFile, bool, error) {
	switch {
	case isZIP(payload):
		files, err := unpackRemoteZIP(payload)
		if err != nil {
			return nil, nil, true, err
		}
		manifest, files, err := remoteWorkflowManifest(files)
		return manifest, files, true, err
	case len(payload) >= 2 && payload[0] == 0x1f && payload[1] == 0x8b:
		files, err := unpackRemoteTarGzip(payload)
		if err != nil {
			return nil, nil, true, err
		}
		manifest, files, err := remoteWorkflowManifest(files)
		return manifest, files, true, err
	default:
		if len(payload) > maxManifestSize {
			return nil, nil, false, fmt.Errorf("manifest exceeds %d-byte limit", maxManifestSize)
		}
		return payload, nil, false, nil
	}
}

func remoteWorkflowManifest(files map[string]ActionFile) ([]byte, map[string]ActionFile, error) {
	var manifest []byte
	for _, name := range []string{"wuko.yaml", "wuko.yml"} {
		if file, ok := files[name]; ok {
			if manifest != nil {
				return nil, nil, fmt.Errorf("archive contains multiple workflow manifests")
			}
			manifest = file.Data
		}
	}
	if manifest == nil {
		return nil, nil, fmt.Errorf("archive must contain wuko.yaml or wuko.yml at its root")
	}
	if len(manifest) > maxManifestSize {
		return nil, nil, fmt.Errorf("manifest exceeds %d-byte limit", maxManifestSize)
	}
	if _, ok := files["wuko.yaml"]; !ok {
		files["wuko.yaml"] = files["wuko.yml"]
		delete(files, "wuko.yml")
	}
	return manifest, files, nil
}

func unpackRemoteZIP(payload []byte) (map[string]ActionFile, error) {
	reader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return nil, fmt.Errorf("opening ZIP workflow: %w", err)
	}
	if len(reader.File) > maxEntries {
		return nil, fmt.Errorf("archive exceeds %d-entry limit", maxEntries)
	}
	files := make(map[string]ActionFile)
	seen := make(map[string]struct{})
	var total int64
	for _, entry := range reader.File {
		if err := validateArchivePath(entry.Name); err != nil {
			return nil, err
		}
		cleanName := strings.TrimSuffix(entry.Name, "/")
		if _, exists := seen[cleanName]; exists {
			return nil, fmt.Errorf("archive contains duplicate path %q", entry.Name)
		}
		seen[cleanName] = struct{}{}
		mode := entry.Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return nil, fmt.Errorf("archive entry %q is not a regular file or directory", entry.Name)
		}
		if mode.IsDir() {
			continue
		}
		content, err := readArchiveFile(entry.Open, &total)
		if err != nil {
			return nil, fmt.Errorf("reading archive entry %q: %w", entry.Name, err)
		}
		files[entry.Name] = ActionFile{Data: content, Mode: mode}
	}
	return files, nil
}

func unpackRemoteTarGzip(payload []byte) (map[string]ActionFile, error) {
	archive, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("opening gzip workflow: %w", err)
	}
	defer archive.Close()
	reader := tar.NewReader(archive)
	files := make(map[string]ActionFile)
	seen := make(map[string]struct{})
	var total int64
	entries := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar workflow: %w", err)
		}
		entries++
		if entries > maxEntries {
			return nil, fmt.Errorf("archive exceeds %d-entry limit", maxEntries)
		}
		if err := validateArchivePath(header.Name); err != nil {
			return nil, err
		}
		cleanName := strings.TrimSuffix(header.Name, "/")
		if _, exists := seen[cleanName]; exists {
			return nil, fmt.Errorf("archive contains duplicate path %q", header.Name)
		}
		seen[cleanName] = struct{}{}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("archive entry %q is not a regular file or directory", header.Name)
		}
		content, err := readArchiveFile(func() (io.ReadCloser, error) { return io.NopCloser(reader), nil }, &total)
		if err != nil {
			return nil, fmt.Errorf("reading archive entry %q: %w", header.Name, err)
		}
		files[header.Name] = ActionFile{Data: content, Mode: os.FileMode(header.Mode)}
	}
	return files, nil
}
