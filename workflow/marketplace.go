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

// ErrMarketplaceNotFound indicates that an HTTPS source does not expose a marketplace manifest.
var ErrMarketplaceNotFound = errors.New("marketplace manifest not found")

// MarketplaceManifest lists workflows published by a marketplace.
type MarketplaceManifest struct {
	Version   int                   `json:"version"`
	Workflows []MarketplaceWorkflow `json:"workflows"`
}

// MarketplaceWorkflow identifies a workflow relative to a marketplace root.
type MarketplaceWorkflow struct {
	Name        string `json:"name,omitempty"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

// DiscoverMarketplace fetches and validates the versioned manifest at baseURL.
// ErrMarketplaceNotFound means callers may fall back to direct workflow loading.
func (loader *Loader) DiscoverMarketplace(ctx context.Context, baseURL string) (MarketplaceManifest, error) {
	base, err := marketplaceBaseURL(baseURL)
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

// ValidateMarketplaceManifest validates the version and all workflow paths.
func ValidateMarketplaceManifest(manifest MarketplaceManifest) error {
	if manifest.Version != MarketplaceManifestVersion {
		return fmt.Errorf("unsupported version %d (want %d)", manifest.Version, MarketplaceManifestVersion)
	}
	seenPaths := make(map[string]struct{}, len(manifest.Workflows))
	seenNames := make(map[string]struct{}, len(manifest.Workflows))
	for index, item := range manifest.Workflows {
		if _, err := validateMarketplacePath(item.Path); err != nil {
			return fmt.Errorf("workflow %d: %w", index+1, err)
		}
		if _, exists := seenPaths[item.Path]; exists {
			return fmt.Errorf("workflow path %q is duplicated", item.Path)
		}
		seenPaths[item.Path] = struct{}{}
		if item.Name != "" {
			if !ValidWorkflowName(item.Name) {
				return fmt.Errorf("workflow %d: invalid name %q", index+1, item.Name)
			}
			if _, exists := seenNames[item.Name]; exists {
				return fmt.Errorf("workflow name %q is duplicated", item.Name)
			}
			seenNames[item.Name] = struct{}{}
		}
	}
	return nil
}

// ResolveMarketplaceWorkflow resolves an entry path against an HTTPS marketplace base URL.
func ResolveMarketplaceWorkflow(baseURL string, item MarketplaceWorkflow) (string, error) {
	base, err := marketplaceBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	if _, err := validateMarketplacePath(item.Path); err != nil {
		return "", err
	}
	entry, err := url.Parse(item.Path)
	if err != nil {
		return "", fmt.Errorf("parsing marketplace workflow path %q: %w", item.Path, err)
	}
	resolved := base.ResolveReference(entry)
	if resolved.Scheme != "https" || resolved.Host != base.Host {
		return "", fmt.Errorf("marketplace workflow path %q resolves outside the marketplace", item.Path)
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
