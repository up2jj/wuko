package plugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

const maxArchiveBytes int64 = 128 << 20
const maxExtractedBytes int64 = 512 << 20
const maxArchiveEntries = 4096

type Release struct {
	Manifest        Manifest
	ManifestData    []byte
	ManifestDigest  string
	Artifact        Artifact
	ArtifactData    []byte
	CanonicalSource string
}

// Bundle contains a plugin manifest and every platform artifact it declares.
type Bundle struct {
	Manifest        Manifest
	ManifestData    []byte
	ManifestDigest  string
	ArtifactData    map[string][]byte
	CanonicalSource string
}

// FetchBundle downloads and validates a complete multi-platform plugin release.
func FetchBundle(ctx context.Context, source string, client *http.Client) (Bundle, error) {
	manifestData, canonical, resolver, err := fetchManifest(ctx, source, client)
	if err != nil {
		return Bundle{}, err
	}
	manifest, err := ParseManifest(manifestData)
	if err != nil {
		return Bundle{}, err
	}
	bundle := Bundle{
		Manifest: manifest, ManifestData: manifestData, ManifestDigest: digest(manifestData),
		ArtifactData: make(map[string][]byte, len(manifest.Artifacts)), CanonicalSource: canonical,
	}
	for _, artifact := range manifest.Artifacts {
		data, err := resolver(ctx, artifact.Path)
		if err != nil {
			return Bundle{}, fmt.Errorf("fetching plugin artifact %s/%s: %w", artifact.OS, artifact.Arch, err)
		}
		if err := ValidateArtifact(artifact, data); err != nil {
			return Bundle{}, fmt.Errorf("validating plugin artifact %s/%s: %w", artifact.OS, artifact.Arch, err)
		}
		bundle.ArtifactData[artifact.Path] = data
	}
	return bundle, nil
}

// LoadBundle reads and validates a complete plugin release from a directory.
func LoadBundle(directory string) (Bundle, error) {
	manifestPath := filepath.Join(directory, "plugin.json")
	info, err := os.Lstat(manifestPath)
	if err != nil {
		return Bundle{}, fmt.Errorf("reading plugin manifest: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Bundle{}, fmt.Errorf("plugin manifest is not a regular file")
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return Bundle{}, fmt.Errorf("reading plugin manifest: %w", err)
	}
	manifest, err := ParseManifest(manifestData)
	if err != nil {
		return Bundle{}, err
	}
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return Bundle{}, fmt.Errorf("resolving plugin release directory: %w", err)
	}
	bundle := Bundle{Manifest: manifest, ManifestData: manifestData, ManifestDigest: digest(manifestData), ArtifactData: make(map[string][]byte, len(manifest.Artifacts))}
	for _, artifact := range manifest.Artifacts {
		filename := filepath.Join(directory, filepath.FromSlash(artifact.Path))
		if !within(directory, filename) {
			return Bundle{}, fmt.Errorf("plugin artifact escapes release directory")
		}
		info, err := os.Lstat(filename)
		if err != nil {
			return Bundle{}, fmt.Errorf("reading plugin artifact %s/%s: %w", artifact.OS, artifact.Arch, err)
		}
		if !info.Mode().IsRegular() {
			return Bundle{}, fmt.Errorf("plugin artifact %s/%s is not a regular file", artifact.OS, artifact.Arch)
		}
		resolved, err := filepath.EvalSymlinks(filename)
		if err != nil {
			return Bundle{}, fmt.Errorf("resolving plugin artifact %s/%s: %w", artifact.OS, artifact.Arch, err)
		}
		if !within(resolvedDirectory, resolved) {
			return Bundle{}, fmt.Errorf("plugin artifact %s/%s escapes the release directory through a symlink", artifact.OS, artifact.Arch)
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return Bundle{}, fmt.Errorf("reading plugin artifact %s/%s: %w", artifact.OS, artifact.Arch, err)
		}
		if err := ValidateArtifact(artifact, data); err != nil {
			return Bundle{}, fmt.Errorf("validating plugin artifact %s/%s: %w", artifact.OS, artifact.Arch, err)
		}
		bundle.ArtifactData[artifact.Path] = data
	}
	return bundle, nil
}

// DigestBundle returns a deterministic digest of the publishable release files.
func DigestBundle(bundle Bundle) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, "plugin.json\x00")
	_, _ = hash.Write(bundle.ManifestData)
	paths := make([]string, 0, len(bundle.ArtifactData))
	for path := range bundle.ArtifactData {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		_, _ = io.WriteString(hash, "\x00"+path+"\x00")
		_, _ = hash.Write(bundle.ArtifactData[path])
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

// ValidateArtifact verifies an archive digest and its safe executable contents.
func ValidateArtifact(artifact Artifact, data []byte) error {
	if digest(data) != artifact.SHA256 {
		return fmt.Errorf("plugin artifact sha256 mismatch")
	}
	directory, err := os.MkdirTemp("", "wuko-plugin-artifact-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	_, err = Extract(Release{Artifact: artifact, ArtifactData: data}, directory)
	return err
}

func FetchRelease(ctx context.Context, source string, expectedManifestDigest string, client *http.Client) (Release, error) {
	manifestData, canonical, resolver, err := fetchManifest(ctx, source, client)
	if err != nil {
		return Release{}, err
	}
	manifestDigest := digest(manifestData)
	if expectedManifestDigest != "" && manifestDigest != expectedManifestDigest {
		return Release{}, fmt.Errorf("plugin manifest sha256 mismatch: got %s", manifestDigest)
	}
	manifest, err := ParseManifest(manifestData)
	if err != nil {
		return Release{}, err
	}
	artifact, err := manifest.CurrentArtifact()
	if err != nil {
		return Release{}, err
	}
	artifactData, err := resolver(ctx, artifact.Path)
	if err != nil {
		return Release{}, err
	}
	if digest(artifactData) != artifact.SHA256 {
		return Release{}, fmt.Errorf("plugin artifact sha256 mismatch")
	}
	return Release{Manifest: manifest, ManifestData: manifestData, ManifestDigest: manifestDigest, Artifact: artifact, ArtifactData: artifactData, CanonicalSource: canonical}, nil
}

type artifactResolver func(context.Context, string) ([]byte, error)

func fetchManifest(ctx context.Context, source string, client *http.Client) ([]byte, string, artifactResolver, error) {
	if strings.HasPrefix(source, "github:") {
		owner, repo, ref, manifestPath, err := parseGitHub(source)
		if err != nil {
			return nil, "", nil, err
		}
		base := "https://raw.githubusercontent.com/" + owner + "/" + repo + "/" + url.PathEscape(ref) + "/"
		manifestURL := base + strings.TrimPrefix(manifestPath, "/")
		data, err := download(ctx, manifestURL, client, "raw.githubusercontent.com")
		if err != nil {
			return nil, "", nil, err
		}
		directory := path.Dir(manifestPath)
		resolver := func(ctx context.Context, item string) ([]byte, error) {
			safe, err := safeRelative(item)
			if err != nil {
				return nil, err
			}
			target := path.Clean(path.Join(directory, safe))
			if target == ".." || strings.HasPrefix(target, "../") {
				return nil, fmt.Errorf("artifact escapes manifest directory")
			}
			return download(ctx, base+target, client, "raw.githubusercontent.com")
		}
		return data, "github:" + owner + "/" + repo + "@" + ref + ":" + manifestPath, resolver, nil
	}
	if strings.HasPrefix(source, "https://") {
		parsed, err := url.Parse(source)
		if err != nil || parsed.User != nil {
			return nil, "", nil, fmt.Errorf("invalid HTTPS plugin source")
		}
		if parsed.Path == "" || strings.HasSuffix(parsed.Path, "/") || !strings.Contains(path.Base(parsed.Path), ".") {
			parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/plugin.json"
		}
		manifestURL := parsed.String()
		data, err := download(ctx, manifestURL, client, parsed.Host)
		if err != nil {
			return nil, "", nil, err
		}
		resolver := func(ctx context.Context, item string) ([]byte, error) {
			safe, err := safeRelative(item)
			if err != nil {
				return nil, err
			}
			target := *parsed
			target.Path = path.Join(path.Dir(parsed.Path), safe)
			target.RawQuery = ""
			target.Fragment = ""
			return download(ctx, target.String(), client, parsed.Host)
		}
		return data, manifestURL, resolver, nil
	}
	if strings.Contains(source, "://") {
		return nil, "", nil, fmt.Errorf("plugin sources must be local, HTTPS, or github")
	}
	manifestPath := source
	info, err := os.Lstat(manifestPath)
	if err != nil {
		return nil, "", nil, fmt.Errorf("reading plugin source: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, "", nil, fmt.Errorf("plugin source must not be a symlink")
	}
	if info.IsDir() {
		manifestPath = filepath.Join(manifestPath, "plugin.json")
	}
	manifestPath, err = filepath.Abs(manifestPath)
	if err != nil {
		return nil, "", nil, err
	}
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		return nil, "", nil, fmt.Errorf("reading plugin manifest: %w", err)
	}
	if !manifestInfo.Mode().IsRegular() {
		return nil, "", nil, fmt.Errorf("plugin manifest is not a regular file")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, "", nil, err
	}
	directory := filepath.Dir(manifestPath)
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, "", nil, fmt.Errorf("resolving plugin release directory: %w", err)
	}
	resolver := func(_ context.Context, item string) ([]byte, error) {
		safe, err := safeRelative(item)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(directory, filepath.FromSlash(safe))
		if !within(directory, target) {
			return nil, fmt.Errorf("artifact escapes manifest directory")
		}
		info, err := os.Lstat(target)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("plugin artifact is not a regular file")
		}
		resolvedTarget, err := filepath.EvalSymlinks(target)
		if err != nil {
			return nil, err
		}
		if !within(resolvedDirectory, resolvedTarget) {
			return nil, fmt.Errorf("artifact escapes manifest directory through a symlink")
		}
		return os.ReadFile(target)
	}
	return data, manifestPath, resolver, nil
}

func parseGitHub(source string) (owner, repo, ref, manifestPath string, err error) {
	value := strings.TrimPrefix(source, "github:")
	at := strings.IndexByte(value, '@')
	if at <= 0 {
		return "", "", "", "", fmt.Errorf("GitHub plugin source requires an explicit ref")
	}
	repository, tail := value[:at], value[at+1:]
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", "", fmt.Errorf("invalid GitHub plugin source")
	}
	colon := strings.IndexByte(tail, ':')
	ref = tail
	manifestPath = "plugin.json"
	if colon >= 0 {
		ref = tail[:colon]
		manifestPath = tail[colon+1:]
		if manifestPath == "" {
			manifestPath = "plugin.json"
		}
	}
	if ref == "" {
		return "", "", "", "", fmt.Errorf("GitHub plugin source requires an explicit ref")
	}
	manifestPath, err = safeRelative(manifestPath)
	return parts[0], parts[1], ref, manifestPath, err
}

func download(ctx context.Context, address string, client *http.Client, origin string) ([]byte, error) {
	if client == nil {
		client = &http.Client{CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if request.URL.Scheme != "https" || request.URL.Host != origin {
				return fmt.Errorf("plugin redirect left original origin")
			}
			if len(via) > 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		}}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.Request.URL.Scheme != "https" || response.Request.URL.Host != origin || response.Request.URL.User != nil {
		return nil, fmt.Errorf("plugin redirect left original origin")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("downloading %s: %s", address, response.Status)
	}
	limited := io.LimitReader(response.Body, maxArchiveBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxArchiveBytes {
		return nil, fmt.Errorf("plugin download exceeds byte limit")
	}
	return data, nil
}

func safeRelative(value string) (string, error) {
	if value == "" || strings.Contains(value, "\\") || path.IsAbs(value) || path.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") {
		return "", fmt.Errorf("unsafe plugin path %q", value)
	}
	return value, nil
}
func within(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func Extract(release Release, directory string) (string, error) {
	reader, err := gzip.NewReader(bytes.NewReader(release.ArtifactData))
	if err != nil {
		return "", fmt.Errorf("opening plugin archive: %w", err)
	}
	defer reader.Close()
	archive := tar.NewReader(reader)
	seen := make(map[string]bool)
	var total int64
	entries := 0
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		entries++
		if entries > maxArchiveEntries {
			return "", fmt.Errorf("plugin archive has too many entries")
		}
		name, err := safeRelative(header.Name)
		if err != nil {
			return "", err
		}
		if seen[name] {
			return "", fmt.Errorf("duplicate plugin archive entry %q", name)
		}
		seen[name] = true
		if header.Typeflag != tar.TypeReg {
			return "", fmt.Errorf("plugin archive entry %q is not a regular file", name)
		}
		if header.Size < 0 || total+header.Size > maxExtractedBytes {
			return "", fmt.Errorf("plugin archive exceeds extracted byte limit")
		}
		total += header.Size
		target := filepath.Join(directory, filepath.FromSlash(name))
		if !within(directory, target) {
			return "", fmt.Errorf("plugin archive entry escapes destination")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return "", err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		_, copyErr := io.CopyN(file, archive, header.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if name == release.Artifact.Entry {
			if header.Mode&0111 == 0 {
				return "", fmt.Errorf("plugin entry is not executable")
			}
			if err := os.Chmod(target, 0700); err != nil {
				return "", err
			}
		}
	}
	entry := filepath.Join(directory, release.Artifact.Entry)
	info, err := os.Stat(entry)
	if err != nil {
		return "", fmt.Errorf("plugin entry is missing: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", fmt.Errorf("plugin entry is not executable")
	}
	return entry, nil
}
