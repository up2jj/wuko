package workflow

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
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
	payload, description, err := loader.fetchWorkflow(ctx, locator)
	if err != nil {
		traceFinish(options.Diagnostics, fetchStarted, diagnostic.PhaseWorkflowFetch, diagnostic.StatusFailed, location, "", "", "", "", err)
		return nil, func() {}, err
	}
	location.Source = description
	traceFinish(options.Diagnostics, fetchStarted, diagnostic.PhaseWorkflowFetch, diagnostic.StatusSucceeded, location, "", "", "", "", nil, countAttr("bytes", len(payload)))

	path, cleanup, err := materializeRemoteWorkflow(payload, description, options.RejectRemoteArchives)
	if err != nil {
		return nil, func() {}, fmt.Errorf("materializing workflow %s: %w", description, err)
	}
	transferred := false
	defer func() {
		if !transferred {
			cleanup()
		}
	}()
	options.sourceRoot = filepath.Dir(path)
	options.sourceLabel = description
	definition, err := loader.Decode(path, options)
	if err != nil {
		return nil, func() {}, err
	}
	remapDefinitionLocations(definition, filepath.Dir(path), description)
	transferred = true
	return definition, cleanup, nil
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
	case strings.HasPrefix(locator, "github:"):
		remoteURL, err := githubWorkflowURL(locator)
		if err != nil {
			return nil, "", err
		}
		headers := make(http.Header)
		headers.Set("Accept", "application/vnd.github.raw+json")
		headers.Set("User-Agent", "wuko")
		payload, err := loader.fetchWithHeaders(ctx, remoteURL, headers, "workflow")
		return payload, locator, err
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
	value := strings.TrimPrefix(locator, "github:")
	if value == "" {
		return nil, fmt.Errorf("invalid GitHub workflow locator %q: owner/repo is required", locator)
	}

	repository := value
	workflowPath := defaultRemoteWorkflowFile
	if before, after, found := strings.Cut(value, ":"); found {
		repository, workflowPath = before, after
		if workflowPath == "" {
			return nil, fmt.Errorf("invalid GitHub workflow locator %q: path is empty", locator)
		}
	}
	ownerRepo := repository
	ref := ""
	if before, after, found := strings.Cut(repository, "@"); found {
		ownerRepo, ref = before, after
		if ref == "" || strings.Contains(after, "@") {
			return nil, fmt.Errorf("invalid GitHub workflow locator %q: ref is invalid", locator)
		}
	}
	parts := strings.Split(ownerRepo, "/")
	if len(parts) != 2 || !validGitHubPart(parts[0]) || !validGitHubPart(parts[1]) {
		return nil, fmt.Errorf("invalid GitHub workflow locator %q: expected owner/repo[@ref][:path]", locator)
	}
	if err := validateRemotePath(workflowPath); err != nil {
		return nil, fmt.Errorf("invalid GitHub workflow locator %q: %w", locator, err)
	}
	if strings.ContainsAny(ref, "\\\x00") || strings.TrimSpace(ref) != ref {
		return nil, fmt.Errorf("invalid GitHub workflow locator %q: ref is invalid", locator)
	}

	segments := strings.Split(workflowPath, "/")
	encodedPath := make([]string, len(segments))
	for i, segment := range segments {
		encodedPath[i] = url.PathEscape(segment)
	}
	remoteURL, err := url.Parse(githubAPIBase + "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/contents/" + strings.Join(encodedPath, "/"))
	if err != nil {
		return nil, fmt.Errorf("building GitHub workflow URL: %w", err)
	}
	if ref != "" {
		query := remoteURL.Query()
		query.Set("ref", ref)
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

	directory, err := os.MkdirTemp("", "wuko-workflow-")
	if err != nil {
		return "", nil, fmt.Errorf("creating temporary workflow directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	for name, file := range files {
		if err := validateArchivePath(name); err != nil {
			cleanup()
			return "", nil, err
		}
		target := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("creating directory for %s: %w", name, err)
		}
		mode := file.Mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(target, file.Data, mode); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("writing workflow file %s: %w", name, err)
		}
	}

	workflowPath := filepath.Join(directory, defaultRemoteWorkflowFile)
	if _, err := os.Stat(workflowPath); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("locating materialized workflow: %w", err)
	}
	return workflowPath, cleanup, nil
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
