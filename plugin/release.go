package plugin

import (
	"archive/tar"
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
	info, err := os.Stat(manifestPath)
	if err != nil {
		return nil, "", nil, fmt.Errorf("reading plugin source: %w", err)
	}
	if info.IsDir() {
		manifestPath = filepath.Join(manifestPath, "plugin.json")
	}
	manifestPath, err = filepath.Abs(manifestPath)
	if err != nil {
		return nil, "", nil, err
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, "", nil, err
	}
	directory := filepath.Dir(manifestPath)
	resolver := func(_ context.Context, item string) ([]byte, error) {
		safe, err := safeRelative(item)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(directory, filepath.FromSlash(safe))
		if !within(directory, target) {
			return nil, fmt.Errorf("artifact escapes manifest directory")
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
