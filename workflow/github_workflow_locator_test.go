package workflow

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGitHubWorkflowLocatorForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		value     string
		canonical string
		tree      bool
	}{
		{name: "shorthand repository", value: "github:acme/automation@main", canonical: "github:acme/automation@main"},
		{name: "repository URL", value: "https://github.com/acme/automation", canonical: "github:acme/automation"},
		{name: "repository git URL", value: "https://github.com/acme/automation.git/", canonical: "github:acme/automation"},
		{name: "blob URL", value: "https://github.com/acme/automation/blob/main/workflows/build/wuko.yaml", canonical: "github:acme/automation@main:workflows/build/wuko.yaml"},
		{name: "tree URL", value: "https://github.com/acme/automation/tree/main/workflows", canonical: "github:acme/automation@main:workflows", tree: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			locator, recognized, err := ParseGitHubWorkflowLocator(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			if !recognized || locator.Canonical() != tt.canonical || locator.isTree() != tt.tree {
				t.Fatalf("locator = %#v, recognized = %v, canonical = %q", locator, recognized, locator.Canonical())
			}
		})
	}
}

func TestParseGitHubWorkflowLocatorRejectsUnsafeURLs(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"http://github.com/acme/automation",
		"https://user@github.com/acme/automation",
		"https://github.com:443/acme/automation",
		"https://github.com/acme/automation?ref=main",
		"https://github.com/acme/automation/blob/main/%2e%2e/workflow.yaml",
		"https://github.com/acme/automation/tree",
		"github:acme/automation@.:wuko.yaml",
		"github:acme/automation@ main:wuko.yaml",
		"github:acme/automation@:wuko.yaml",
		"github:acme/automation@..:wuko.yaml",
	} {
		t.Run(value, func(t *testing.T) {
			_, recognized, err := ParseGitHubWorkflowLocator(value)
			if !recognized || err == nil {
				t.Fatalf("recognized = %v, error = %v", recognized, err)
			}
		})
	}
}

func TestParseGitHubWorkflowLocatorLeavesOtherGitHubURLsToHTTPS(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"https://github.com/acme/automation/issues",
		"https://github.com/acme/automation/raw/main/wuko.yaml",
		"https://github.com/acme/automation/releases/download/v1.0.0/workflow.tar.gz",
		"https://example.com/acme/automation",
	} {
		t.Run(value, func(t *testing.T) {
			_, recognized, err := ParseGitHubWorkflowLocator(value)
			if recognized || err != nil {
				t.Fatalf("recognized = %v, error = %v", recognized, err)
			}
		})
	}
}

func TestDiscoverGitHubWorkflowsUsesOneTemporarySnapshot(t *testing.T) {
	t.Parallel()
	tarball := makeGitHubTarball(t, "acme-automation-0123456", []githubArchiveEntry{
		{name: "workflows/build/wuko.yaml", data: []byte("version: 1\nname: build\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n")},
		{name: "workflows/build/scripts/build.sh", data: []byte("#!/bin/sh\n"), mode: 0o755},
		{name: "workflows/deploy/wuko.yml", data: []byte("version: 1\nname: deploy\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n")},
		{name: "outside/wuko.yaml", data: []byte("version: 1\nname: outside\nsteps:\n  - id: run\n    type: shell\n")},
	})
	requests := 0
	client := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.EscapedPath() != "/repos/acme/automation/tarball/main" {
			return nil, fmt.Errorf("path = %q", request.URL.EscapedPath())
		}
		return testResponse(http.StatusOK, tarball), nil
	})
	loader := NewLoader(client)
	loader.githubStoredTokenFn = func(context.Context, map[string]string) string { return "" }
	sources, cleanup, err := loader.DiscoverGitHubWorkflows(t.Context(), "https://github.com/acme/automation/tree/main/workflows", LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || len(sources) != 2 {
		t.Fatalf("requests = %d, sources = %#v", requests, sources)
	}
	if sources[0].Locator != "github:acme/automation@main:workflows/build/wuko.yaml" || sources[1].Locator != "github:acme/automation@main:workflows/deploy/wuko.yml" {
		t.Fatalf("locators = %q, %q", sources[0].Locator, sources[1].Locator)
	}
	companion := filepath.Join(filepath.Dir(sources[0].Path), "scripts", "build.sh")
	if _, err := os.Stat(companion); err != nil {
		t.Fatalf("companion file is unavailable: %v", err)
	}
	root := filepath.Dir(filepath.Dir(sources[0].Path))
	cleanup()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("snapshot remains after cleanup: %v", err)
	}
}

func TestLoadRemoteGitHubBlobURLPreservesURLDiagnostics(t *testing.T) {
	t.Parallel()
	const raw = "https://github.com/acme/automation/blob/main/workflows/build/wuko.yaml"
	tarball := makeGitHubTarball(t, "acme-automation-0123456", []githubArchiveEntry{
		{name: "workflows/build/wuko.yaml", data: []byte("version: 1\nname: build\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n")},
		{name: "workflows/build/config/settings.json", data: []byte("{}")},
	})
	client := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, tarball), nil
	})
	loader := NewLoader(client)
	loader.githubStoredTokenFn = func(context.Context, map[string]string) string { return "" }
	definition, cleanup, err := loader.LoadRemote(t.Context(), raw, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if definition.Location.Source != raw {
		t.Fatalf("source = %q, want %q", definition.Location.Source, raw)
	}
	if _, err := os.Stat(filepath.Join(definition.Dir, "config", "settings.json")); err != nil {
		t.Fatalf("companion file is unavailable: %v", err)
	}
}

func TestDiscoverGitHubWorkflowsRejectsBlobURL(t *testing.T) {
	t.Parallel()
	_, _, err := NewLoader(nil).DiscoverGitHubWorkflows(t.Context(), "https://github.com/acme/automation/blob/main/workflows/build/wuko.yaml", LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "omit --pick") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRemoteGitHubRootWorkflowUsesContentsAPI(t *testing.T) {
	t.Parallel()
	workflowData := []byte("version: 1\nname: root\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n")
	for _, raw := range []string{"github:acme/automation@main", "github:acme/automation@main:release.yaml", "https://github.com/acme/automation"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			client := testHTTPClient(func(request *http.Request) (*http.Response, error) {
				if !strings.HasPrefix(request.URL.EscapedPath(), "/repos/acme/automation/contents/") {
					return nil, fmt.Errorf("path = %q, want the contents API", request.URL.EscapedPath())
				}
				if got := request.Header.Get("Accept"); got != "application/vnd.github.raw+json" {
					return nil, fmt.Errorf("Accept = %q", got)
				}
				return testResponse(http.StatusOK, workflowData), nil
			})
			loader := NewLoader(client)
			loader.githubStoredTokenFn = func(context.Context, map[string]string) string { return "" }
			definition, cleanup, err := loader.LoadRemote(t.Context(), raw, LoadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if definition.Name != "root" {
				t.Fatalf("name = %q, want root", definition.Name)
			}
		})
	}
}

func TestLoadRemoteGitHubReportsExhaustedRateLimit(t *testing.T) {
	t.Parallel()
	for _, options := range []LoadOptions{{}, {RejectRemoteArchives: true}} {
		client := testHTTPClient(func(*http.Request) (*http.Response, error) {
			response := testResponse(http.StatusForbidden, []byte(`{"message":"API rate limit exceeded"}`))
			response.Header.Set("X-RateLimit-Remaining", "0")
			return response, nil
		})
		loader := NewLoader(client)
		loader.githubStoredTokenFn = func(context.Context, map[string]string) string { return "" }
		_, _, err := loader.LoadRemote(t.Context(), "github:acme/automation@main:workflows/build/wuko.yaml", options)
		if err == nil || !strings.Contains(err.Error(), "rate limit is exhausted") {
			t.Fatalf("RejectRemoteArchives = %v: error = %v", options.RejectRemoteArchives, err)
		}
	}
}

func TestDiscoverGitHubWorkflowsSkipsNestedAndKeepsInvalidManifests(t *testing.T) {
	t.Parallel()
	valid := func(name string) []byte {
		return []byte("version: 1\nname: " + name + "\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n")
	}
	tarball := makeGitHubTarball(t, "acme-automation-0123456", []githubArchiveEntry{
		{name: "wuko.yaml", data: valid("root")},
		{name: "workflows/build/wuko.yaml", data: valid("build")},
		{name: "workflows/build/testdata/wuko.yaml", data: valid("fixture")},
		{name: "workflows/broken/wuko.yaml", data: []byte("version: 1\nname: broken\nsteps: nope\n")},
	})
	client := testHTTPClient(func(*http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, tarball), nil
	})
	loader := NewLoader(client)
	loader.githubStoredTokenFn = func(context.Context, map[string]string) string { return "" }
	sources, cleanup, err := loader.DiscoverGitHubWorkflows(t.Context(), "github:acme/automation", LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got := make(map[string]bool, len(sources))
	for _, source := range sources {
		got[source.Locator] = source.Invokable
	}
	want := map[string]bool{
		"github:acme/automation:wuko.yaml":                  true,
		"github:acme/automation:workflows/build/wuko.yaml":  true,
		"github:acme/automation:workflows/broken/wuko.yaml": false,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("sources = %v, want %v", got, want)
	}
}
