package workflow

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestDiscoverMarketplaceValidatesVersionedManifest(t *testing.T) {
	manifest := `{"version":1,"workflows":[{"name":"release","path":".wuko/workflows/release.yaml","description":"Ship it"}]}`
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
	if got.Version != MarketplaceManifestVersion || len(got.Workflows) != 1 || got.Workflows[0].Name != "release" {
		t.Fatalf("manifest = %#v", got)
	}
	resolved, err := ResolveMarketplaceWorkflow("https://example.test/repo", got.Workflows[0])
	if err != nil || resolved != "https://example.test/repo/.wuko/workflows/release.yaml" {
		t.Fatalf("resolved = %q, err = %v", resolved, err)
	}
	resolved, err = ResolveMarketplaceWorkflow("https://example.test/repo?token=secret", got.Workflows[0])
	if err != nil || resolved != "https://example.test/repo/.wuko/workflows/release.yaml?token=secret" {
		t.Fatalf("resolved with query = %q, err = %v", resolved, err)
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

func TestValidateMarketplaceManifestRejectsUnsupportedAndUnsafeEntries(t *testing.T) {
	tests := []MarketplaceManifest{
		{Version: 2},
		{Version: 1, Workflows: []MarketplaceWorkflow{{Name: "release", Path: "../release.yaml"}}},
		{Version: 1, Workflows: []MarketplaceWorkflow{{Name: "release", Path: "release.yaml?ref=main"}}},
		{Version: 1, Workflows: []MarketplaceWorkflow{{Name: "release", Path: "release.yaml"}, {Name: "release", Path: "other.yaml"}}},
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
