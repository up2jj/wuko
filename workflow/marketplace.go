package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"unicode"
)

const MarketplaceManifestVersion = 1

// MarketplacePluginSourceDir holds imported plugin releases inside a marketplace repository. It is
// deliberately distinct from ".wuko/plugins", which is the local plugin *installation* root: an
// imported release has no executable, so sharing the directory would make local plugin discovery
// fail for that namespace in the marketplace repository and every directory below it.
const MarketplacePluginSourceDir = ".wuko/plugin-sources"

// ErrMarketplaceNotFound indicates that an HTTPS source does not expose a marketplace manifest.
var ErrMarketplaceNotFound = errors.New("marketplace manifest not found")

// MarketplaceManifest lists workflow and plugin packages published by a marketplace.
type MarketplaceManifest struct {
	Version  int                        `json:"version"`
	Packages []MarketplacePackage       `json:"packages"`
	Plugins  []MarketplacePluginPackage `json:"plugins"`
}

// MarketplacePluginPackage identifies a copied plugin release manifest.
type MarketplacePluginPackage struct {
	Namespace     string                `json:"namespace"`
	PluginVersion string                `json:"plugin_version"`
	Description   string                `json:"description,omitempty"`
	Source        string                `json:"source"`
	SourceSHA256  string                `json:"source_sha256"`
	Path          string                `json:"path"`
	SHA256        string                `json:"sha256"`
	Platforms     []MarketplacePlatform `json:"platforms"`
}

// MarketplacePlatform identifies an operating system and architecture pair.
type MarketplacePlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// MarketplacePackage identifies an archived workflow package relative to a marketplace root.
// PackageVersion is publisher metadata and is independent of the workflow schema version.
type MarketplacePackage struct {
	Name           string `json:"name"`
	PackageVersion string `json:"package_version,omitempty"`
	Source         string `json:"source"`
	Path           string `json:"path"`
	Format         string `json:"format"`
	Entry          string `json:"entry"`
	Description    string `json:"description,omitempty"`
	SourceSHA256   string `json:"source_sha256"`
	SHA256         string `json:"sha256"`
}

// DiscoverMarketplace fetches and validates the versioned manifest at baseURL.
// ErrMarketplaceNotFound means callers may fall back to direct workflow loading.
func (loader *Loader) DiscoverMarketplace(ctx context.Context, baseURL string) (MarketplaceManifest, error) {
	base, err := marketplaceContentBaseURL(baseURL)
	if err != nil {
		return MarketplaceManifest{}, err
	}
	manifestURL := *base
	manifestURL.Path += "manifest.json"
	manifestURL.RawPath = ""

	payload, status, err := loader.fetchWithHeadersStatus(ctx, &manifestURL, nil, "marketplace manifest")
	if status == 404 {
		return MarketplaceManifest{}, ErrMarketplaceNotFound
	}
	if err != nil {
		return MarketplaceManifest{}, err
	}
	if len(payload) > maxManifestSize {
		return MarketplaceManifest{}, fmt.Errorf("marketplace manifest exceeds %d-byte limit", maxManifestSize)
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var manifest MarketplaceManifest
	if err := decoder.Decode(&manifest); err != nil {
		return MarketplaceManifest{}, fmt.Errorf("decoding marketplace manifest %s: %w", safeURL(&manifestURL), err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return MarketplaceManifest{}, fmt.Errorf("decoding marketplace manifest %s: multiple JSON values are not supported", safeURL(&manifestURL))
	} else if !errors.Is(err, io.EOF) {
		return MarketplaceManifest{}, fmt.Errorf("decoding marketplace manifest %s: %w", safeURL(&manifestURL), err)
	}
	if err := ValidateMarketplaceManifest(manifest); err != nil {
		return MarketplaceManifest{}, fmt.Errorf("validating marketplace manifest %s: %w", safeURL(&manifestURL), err)
	}
	return manifest, nil
}

// ValidateMarketplaceManifest validates the version and all package metadata.
func ValidateMarketplaceManifest(manifest MarketplaceManifest) error {
	if manifest.Version != MarketplaceManifestVersion {
		return fmt.Errorf("unsupported version %d (want %d)", manifest.Version, MarketplaceManifestVersion)
	}
	seenSources := make(map[string]struct{}, len(manifest.Packages))
	seenPaths := make(map[string]struct{}, len(manifest.Packages))
	seenNames := make(map[string]struct{}, len(manifest.Packages))
	for index, item := range manifest.Packages {
		if !ValidWorkflowName(item.Name) {
			return fmt.Errorf("package %d: invalid name %q", index+1, item.Name)
		}
		if strings.TrimSpace(item.PackageVersion) != item.PackageVersion {
			return fmt.Errorf("package %d: package_version must not have leading or trailing whitespace", index+1)
		}
		if _, err := validateMarketplacePath(item.Source); err != nil {
			return fmt.Errorf("package %d source: %w", index+1, err)
		}
		if item.Format != "tar.gz" {
			return fmt.Errorf("package %d: unsupported format %q (want tar.gz)", index+1, item.Format)
		}
		if item.Entry != defaultRemoteWorkflowFile {
			return fmt.Errorf("package %d: entry must be %q", index+1, defaultRemoteWorkflowFile)
		}
		if !sha256Pattern.MatchString(item.SourceSHA256) {
			return fmt.Errorf("package %d: source_sha256 must be a 64-character hexadecimal digest", index+1)
		}
		if !sha256Pattern.MatchString(item.SHA256) {
			return fmt.Errorf("package %d: sha256 must be a 64-character hexadecimal digest", index+1)
		}
		if _, err := validateMarketplacePath(item.Path); err != nil {
			return fmt.Errorf("package %d path: %w", index+1, err)
		}
		if _, exists := seenSources[item.Source]; exists {
			return fmt.Errorf("package source %q is duplicated", item.Source)
		}
		seenSources[item.Source] = struct{}{}
		if _, exists := seenPaths[item.Path]; exists {
			return fmt.Errorf("package path %q is duplicated", item.Path)
		}
		seenPaths[item.Path] = struct{}{}
		if _, exists := seenNames[item.Name]; exists {
			return fmt.Errorf("package name %q is duplicated", item.Name)
		}
		seenNames[item.Name] = struct{}{}
	}
	seenNamespaces := make(map[string]struct{}, len(manifest.Plugins))
	seenPluginSources := make(map[string]struct{}, len(manifest.Plugins))
	seenPluginPaths := make(map[string]struct{}, len(manifest.Plugins))
	for index, item := range manifest.Plugins {
		if !ValidPluginNamespace(item.Namespace) {
			return fmt.Errorf("plugin %d: invalid namespace %q", index+1, item.Namespace)
		}
		if strings.TrimSpace(item.PluginVersion) == "" || strings.TrimSpace(item.PluginVersion) != item.PluginVersion {
			return fmt.Errorf("plugin %d: plugin_version must be non-empty without surrounding whitespace", index+1)
		}
		if strings.TrimSpace(item.Description) != item.Description {
			return fmt.Errorf("plugin %d: description must not have leading or trailing whitespace", index+1)
		}
		if _, err := validateMarketplacePath(item.Source); err != nil {
			return fmt.Errorf("plugin %d source: %w", index+1, err)
		}
		if item.Source != path.Join(MarketplacePluginSourceDir, item.Namespace) {
			return fmt.Errorf("plugin %d: source must be %q", index+1, path.Join(MarketplacePluginSourceDir, item.Namespace))
		}
		if !sha256Pattern.MatchString(item.SourceSHA256) {
			return fmt.Errorf("plugin %d: source_sha256 must be a 64-character hexadecimal digest", index+1)
		}
		if _, err := validateMarketplacePath(item.Path); err != nil {
			return fmt.Errorf("plugin %d path: %w", index+1, err)
		}
		if path.Base(item.Path) != "plugin.json" {
			return fmt.Errorf("plugin %d: path must end in plugin.json", index+1)
		}
		if item.Path != path.Join("plugins", item.Namespace, "plugin.json") {
			return fmt.Errorf("plugin %d: path must be %q", index+1, path.Join("plugins", item.Namespace, "plugin.json"))
		}
		if !sha256Pattern.MatchString(item.SHA256) {
			return fmt.Errorf("plugin %d: sha256 must be a 64-character hexadecimal digest", index+1)
		}
		if len(item.Platforms) == 0 {
			return fmt.Errorf("plugin %d: at least one platform is required", index+1)
		}
		seenPlatforms := make(map[string]struct{}, len(item.Platforms))
		for _, platform := range item.Platforms {
			if (platform.OS != "darwin" && platform.OS != "linux") || (platform.Arch != "amd64" && platform.Arch != "arm64") {
				return fmt.Errorf("plugin %d: unsupported platform %s/%s", index+1, platform.OS, platform.Arch)
			}
			key := platform.OS + "/" + platform.Arch
			if _, exists := seenPlatforms[key]; exists {
				return fmt.Errorf("plugin %d: duplicate platform %s", index+1, key)
			}
			seenPlatforms[key] = struct{}{}
		}
		if _, exists := seenNamespaces[item.Namespace]; exists {
			return fmt.Errorf("plugin namespace %q is duplicated", item.Namespace)
		}
		seenNamespaces[item.Namespace] = struct{}{}
		if _, exists := seenPluginSources[item.Source]; exists {
			return fmt.Errorf("plugin source %q is duplicated", item.Source)
		}
		seenPluginSources[item.Source] = struct{}{}
		if _, exists := seenPluginPaths[item.Path]; exists {
			return fmt.Errorf("plugin path %q is duplicated", item.Path)
		}
		seenPluginPaths[item.Path] = struct{}{}
	}
	return nil
}

// ResolveMarketplacePackage resolves an archive path against an HTTPS marketplace base URL.
func ResolveMarketplacePackage(baseURL string, item MarketplacePackage) (string, error) {
	base, err := marketplaceContentBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	if _, err := validateMarketplacePath(item.Path); err != nil {
		return "", err
	}
	entry, err := url.Parse(item.Path)
	if err != nil {
		return "", fmt.Errorf("parsing marketplace package path %q: %w", item.Path, err)
	}
	resolved := base.ResolveReference(entry)
	if resolved.Scheme != "https" || resolved.Host != base.Host {
		return "", fmt.Errorf("marketplace package path %q resolves outside the marketplace", item.Path)
	}
	resolved.RawQuery = base.RawQuery
	resolved.ForceQuery = base.ForceQuery
	return resolved.String(), nil
}

// ResolveMarketplacePlugin resolves a plugin manifest path against a marketplace URL.
func ResolveMarketplacePlugin(baseURL string, item MarketplacePluginPackage) (string, error) {
	base, err := marketplaceContentBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	if _, err := validateMarketplacePath(item.Path); err != nil {
		return "", err
	}
	entry, err := url.Parse(item.Path)
	if err != nil {
		return "", fmt.Errorf("parsing marketplace plugin path %q: %w", item.Path, err)
	}
	resolved := base.ResolveReference(entry)
	if resolved.Scheme != "https" || resolved.Host != base.Host {
		return "", fmt.Errorf("marketplace plugin path %q resolves outside the marketplace", item.Path)
	}
	resolved.RawQuery = base.RawQuery
	resolved.ForceQuery = base.ForceQuery
	return resolved.String(), nil
}

// MarketplaceRepositoryName returns a readable, filesystem-safe directory name for a marketplace.
func MarketplaceRepositoryName(baseURL string) (string, error) {
	base, err := marketplaceBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	name := path.Base(strings.TrimSuffix(base.Path, "/"))
	if name == "." || name == "/" || name == "" {
		name = base.Hostname()
	}
	name = strings.TrimSuffix(name, ".git")
	var result strings.Builder
	separator := false
	for _, character := range name {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_' || character == '-' {
			result.WriteRune(character)
			separator = false
			continue
		}
		if !separator {
			result.WriteByte('-')
			separator = true
		}
	}
	clean := strings.Trim(result.String(), "-.")
	if clean == "" || clean == "." || clean == ".." {
		return "", fmt.Errorf("marketplace URL %q has no usable repository name", baseURL)
	}
	return clean, nil
}

// MarketplaceURL returns the canonical marketplace base URL used for collision checks.
func MarketplaceURL(raw string) (string, error) {
	base, err := marketplaceBaseURL(raw)
	if err != nil {
		return "", err
	}
	base.RawQuery = ""
	return base.String(), nil
}

func marketplaceBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid marketplace URL: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("marketplace URL must use HTTPS and include a host")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("marketplace URL user information is not allowed")
	}
	if parsed.Fragment != "" {
		return nil, fmt.Errorf("marketplace URL fragments are not allowed")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/"
	parsed.RawPath = ""
	return parsed, nil
}

func marketplaceContentBaseURL(raw string) (*url.URL, error) {
	base, err := marketplaceBaseURL(raw)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(base.Hostname(), "github.com") || base.Port() != "" {
		return base, nil
	}

	segments := strings.Split(strings.Trim(base.Path, "/"), "/")
	if len(segments) != 2 {
		return base, nil
	}
	owner, repository := segments[0], strings.TrimSuffix(segments[1], ".git")
	if !validGitHubPart(owner) || !validGitHubPart(repository) {
		return base, nil
	}

	return &url.URL{
		Scheme: "https",
		Host:   "raw.githubusercontent.com",
		Path:   "/" + owner + "/" + repository + "/HEAD/",
	}, nil
}

func validateMarketplacePath(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("workflow path is required")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid workflow path %q: %w", value, err)
	}
	if parsed.IsAbs() || parsed.Host != "" || parsed.Path == "" || strings.HasPrefix(parsed.Path, "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("workflow path %q must be a relative path without query or fragment", value)
	}
	if strings.ContainsAny(parsed.Path, "\\\x00") || path.Clean(parsed.Path) != parsed.Path {
		return "", fmt.Errorf("workflow path %q is not safe", value)
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("workflow path %q is not safe", value)
		}
	}
	return parsed.Path, nil
}
