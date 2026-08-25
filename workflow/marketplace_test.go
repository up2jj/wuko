package workflow

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestDiscoverMarketplaceValidatesPackageManifest(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":1,"packages":[{"name":"release","package_version":"1.0.0","source":".wuko/workflows/release","path":"packages/release.tar.gz","format":"tar.gz","entry":"wuko.yaml","description":"Ship it","source_sha256":"%s","sha256":"%s"}]}`, strings.Repeat("a", 64), strings.Repeat("b", 64))
	loader := NewLoader(testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://example.test/repo/manifest.json" {
			return nil, fmt.Errorf("unexpected URL %s", request.URL)
		}
		return testResponse(http.StatusOK, []byte(manifest)), nil
	}))
	got, err := loader.DiscoverMarketplace(t.Context(), "https://example.test/repo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != MarketplaceManifestVersion || len(got.Packages) != 1 || got.Packages[0].Name != "release" || got.Packages[0].PackageVersion != "1.0.0" {
		t.Fatalf("manifest = %#v", got)
	}
	resolved, err := ResolveMarketplacePackage("https://example.test/repo", got.Packages[0])
	if err != nil || resolved != "https://example.test/repo/packages/release.tar.gz" {
		t.Fatalf("resolved = %q, err = %v", resolved, err)
	}
	resolved, err = ResolveMarketplacePackage("https://example.test/repo?token=secret", got.Packages[0])
	if err != nil || resolved != "https://example.test/repo/packages/release.tar.gz?token=secret" {
		t.Fatalf("resolved with query = %q, err = %v", resolved, err)
	}
}

func TestDiscoverMarketplaceNormalizesGitHubRepositoryURL(t *testing.T) {
	item := validMarketplacePackage("release", "packages/release.tar.gz")
	manifest := fmt.Sprintf(`{"version":1,"packages":[{"name":"%s","source":"%s","path":"%s","format":"%s","entry":"%s","source_sha256":"%s","sha256":"%s"}]}`, item.Name, item.Source, item.Path, item.Format, item.Entry, item.SourceSHA256, item.SHA256)
	loader := NewLoader(testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://raw.githubusercontent.com/acme/workflows/HEAD/manifest.json" {
			return nil, fmt.Errorf("unexpected URL %s", request.URL)
		}
		return testResponse(http.StatusOK, []byte(manifest)), nil
	}))
	got, err := loader.DiscoverMarketplace(t.Context(), "https://github.com/acme/workflows/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packages) != 1 || got.Packages[0].Name != "release" {
		t.Fatalf("manifest = %#v", got)
	}
	resolved, err := ResolveMarketplacePackage("https://github.com/acme/workflows", got.Packages[0])
	if err != nil || resolved != "https://raw.githubusercontent.com/acme/workflows/HEAD/packages/release.tar.gz" {
		t.Fatalf("resolved = %q, err = %v", resolved, err)
	}
}

func TestDiscoverMarketplaceFallsBackWhenManifestIsMissing(t *testing.T) {
	loader := NewLoader(testHTTPClient(func(*http.Request) (*http.Response, error) {
		return testResponse(http.StatusNotFound, nil), nil
	}))
	_, err := loader.DiscoverMarketplace(t.Context(), "https://example.test/repo/")
	if !errors.Is(err, ErrMarketplaceNotFound) {
		t.Fatalf("error = %v, want ErrMarketplaceNotFound", err)
	}
}

func TestValidateMarketplaceManifestRejectsInvalidPackages(t *testing.T) {
	valid := validMarketplacePackage("release", "packages/release.tar.gz")
	tests := []MarketplaceManifest{
		{Version: 2},
		{Version: 1, Packages: []MarketplacePackage{{Name: "release", Source: "../release", Path: valid.Path, Format: valid.Format, Entry: valid.Entry, SourceSHA256: valid.SourceSHA256, SHA256: valid.SHA256}}},
		{Version: 1, Packages: []MarketplacePackage{{Name: "release", Source: valid.Source, Path: valid.Path + "?ref=main", Format: valid.Format, Entry: valid.Entry, SourceSHA256: valid.SourceSHA256, SHA256: valid.SHA256}}},
		{Version: 1, Packages: []MarketplacePackage{{Name: "release", Source: valid.Source, Path: valid.Path, Format: "zip", Entry: valid.Entry, SourceSHA256: valid.SourceSHA256, SHA256: valid.SHA256}}},
		{Version: 1, Packages: []MarketplacePackage{valid, valid}},
	}
	for index, manifest := range tests {
		if err := ValidateMarketplaceManifest(manifest); err == nil {
			t.Fatalf("test %d: expected validation error", index)
		}
	}
}

func TestMarketplaceRepositoryName(t *testing.T) {
	tests := map[string]string{
		"https://example.test/acme/release.git": "release",
		"https://example.test/acme/release/":    "release",
		"https://example.test/":                 "example-test",
	}
	for source, want := range tests {
		got, err := MarketplaceRepositoryName(source)
		if err != nil || got != want {
			t.Fatalf("MarketplaceRepositoryName(%q) = %q, %v; want %q", source, got, err, want)
		}
	}
}

func validMarketplacePackage(name, path string) MarketplacePackage {
	return MarketplacePackage{
		Name: name, PackageVersion: "1.0.0", Source: ".wuko/workflows/" + name, Path: path, Format: "tar.gz", Entry: "wuko.yaml",
		SourceSHA256: strings.Repeat("a", 64), SHA256: strings.Repeat("b", 64),
	}
}
