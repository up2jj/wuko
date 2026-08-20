package workflow

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/diagnostic"
)

func TestGitHubWorkflowURL(t *testing.T) {
	tests := []struct {
		locator string
		wantURL string
	}{
		{locator: "github:acme/workflows", wantURL: "https://api.github.com/repos/acme/workflows/contents/wuko.yaml"},
		{locator: "github:acme/workflows@v1.2.3", wantURL: "https://api.github.com/repos/acme/workflows/contents/wuko.yaml?ref=v1.2.3"},
		{locator: "github:acme/workflows:ci/release file.yaml", wantURL: "https://api.github.com/repos/acme/workflows/contents/ci/release%20file.yaml"},
		{locator: "github:acme/workflows@feature/release:ci/release.yaml", wantURL: "https://api.github.com/repos/acme/workflows/contents/ci/release.yaml?ref=feature%2Frelease"},
	}
	for _, tt := range tests {
		t.Run(tt.locator, func(t *testing.T) {
			got, err := githubWorkflowURL(tt.locator)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tt.wantURL {
				t.Fatalf("URL = %q, want %q", got, tt.wantURL)
			}
		})
	}
}

func TestGitHubWorkflowURLRejectsMalformedLocators(t *testing.T) {
	for _, locator := range []string{
		"github:", "github:owner", "github:owner/repo:", "github:owner/repo@",
		"github:owner/repo:../workflow.yaml", "github:owner/repo:/workflow.yaml",
		"github:owner/repo@ref@other:workflow.yaml", "github:owner/repo\\name",
	} {
		t.Run(locator, func(t *testing.T) {
			if _, err := githubWorkflowURL(locator); err == nil {
				t.Fatal("expected malformed locator error")
			}
		})
	}
}

func TestLoadRemoteYAMLAndCleansUp(t *testing.T) {
	workflowData := []byte("version: 1\nname: remote\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n")
	client := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://example.test/workflow.yaml?token=secret" {
			return nil, fmt.Errorf("unexpected URL %s", request.URL)
		}
		return testResponse(http.StatusOK, workflowData), nil
	})

	var events []diagnostic.Event
	definition, cleanup, err := NewLoader(client).LoadRemote(t.Context(), "https://example.test/workflow.yaml?token=secret", LoadOptions{
		RunDir: t.TempDir(), Diagnostics: func(event diagnostic.Event) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if definition.Name != "remote" {
		t.Fatalf("name = %q, want remote", definition.Name)
	}
	if definition.Location.Source != "https://example.test/workflow.yaml" {
		t.Fatalf("logical source = %q", definition.Location.Source)
	}
	for _, event := range events {
		if strings.Contains(event.Location.Source, "wuko-workflow-") || strings.Contains(event.Location.Source, "token=secret") {
			t.Fatalf("diagnostic source = %q", event.Location.Source)
		}
	}
	if _, err := os.Stat(definition.Path); err != nil {
		t.Fatalf("materialized workflow is unavailable: %v", err)
	}
	path := definition.Path
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("materialized workflow remains after cleanup: %v", err)
	}
}

func TestLoadRemoteArchivesWorkflowAndCompanionFiles(t *testing.T) {
	workflowData := []byte("version: 1\nname: remote\nsteps:\n  - id: run\n    type: lua\n    with:\n      file: companion.lua\n")
	for name, payload := range map[string][]byte{
		"zip": makeZIP(t, map[string]archiveTestFile{
			"wuko.yaml":     {data: workflowData, mode: 0o644},
			"companion.lua": {data: []byte("wuko.output(\"ok\", true)"), mode: 0o644},
		}),
		"tar.gz": makeTarGzip(t, map[string]archiveTestFile{
			"wuko.yml":      {data: workflowData, mode: 0o644},
			"companion.lua": {data: []byte("wuko.output(\"ok\", true)"), mode: 0o644},
		}),
	} {
		t.Run(name, func(t *testing.T) {
			client := testHTTPClient(func(*http.Request) (*http.Response, error) { return testResponse(http.StatusOK, payload), nil })
			definition, cleanup, err := NewLoader(client).LoadRemote(t.Context(), "https://example.test/workflow", LoadOptions{RunDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if _, err := os.Stat(filepath.Join(definition.Dir, "companion.lua")); err != nil {
				t.Fatalf("companion file was not materialized: %v", err)
			}
		})
	}
}

func TestLoadRemoteArchiveResolvesTemplateFiles(t *testing.T) {
	workflowData := []byte(`version: 1
name: remote-templates
templates:
  message:
    file: templates/message.tmpl
steps:
  - id: run
    type: shell
    with:
      command: echo
      args: ['{{ template "message" . }}']
`)
	payload := makeZIP(t, map[string]archiveTestFile{
		"wuko.yaml":              {data: workflowData, mode: 0o644},
		"templates/message.tmpl": {data: []byte("Hello {{ .vars.name }}"), mode: 0o644},
	})
	client := testHTTPClient(func(*http.Request) (*http.Response, error) { return testResponse(http.StatusOK, payload), nil })
	definition, cleanup, err := NewLoader(client).LoadRemote(t.Context(), "https://example.test/workflow.zip", LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got := definition.Templates["message"].Body; got != "Hello {{ .vars.name }}" {
		t.Fatalf("template body = %q", got)
	}
}

func TestLoadRemoteRejectsTemplateFileOutsidePackage(t *testing.T) {
	payload := makeZIP(t, map[string]archiveTestFile{
		"wuko.yaml": {
			data: []byte("version: 1\nname: remote\ntemplates:\n  escape:\n    file: ../escape.tmpl\nsteps:\n  - id: run\n    type: shell\n"),
			mode: 0o644,
		},
	})
	client := testHTTPClient(func(*http.Request) (*http.Response, error) { return testResponse(http.StatusOK, payload), nil })
	_, _, err := NewLoader(client).LoadRemote(t.Context(), "https://example.test/workflow.zip", LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "escapes the workflow package") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRemoteArchiveExpandsRequiredStepFiles(t *testing.T) {
	payload := makeZIP(t, map[string]archiveTestFile{
		"wuko.yaml": {
			data: []byte("version: 1\nname: remote-split\nsteps:\n  - require: steps/build.yaml\n"),
			mode: 0o644,
		},
		"steps/build.yaml": {
			data: []byte("- id: build\n  type: shell\n  with: {command: build}\n"),
			mode: 0o644,
		},
	})
	client := testHTTPClient(func(*http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, payload), nil
	})

	definition, cleanup, err := NewLoader(client).LoadRemote(t.Context(), "https://example.test/workflow.zip", LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(definition.Steps) != 1 || definition.Steps[0].ID != "build" {
		t.Fatalf("steps = %#v, want expanded build step", definition.Steps)
	}
}

func TestLoadRemoteRejectsInvalidWorkflowArchives(t *testing.T) {
	tests := map[string][]byte{
		"missing manifest": makeZIP(t, map[string]archiveTestFile{
			"workflow.yaml": {data: []byte("bad"), mode: 0o644},
		}),
		"multiple manifests": makeZIP(t, map[string]archiveTestFile{
			"wuko.yaml": {data: []byte("one"), mode: 0o644}, "wuko.yml": {data: []byte("two"), mode: 0o644},
		}),
		"nested manifest": makeZIP(t, map[string]archiveTestFile{
			"nested/wuko.yaml": {data: []byte("nested"), mode: 0o644},
		}),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			client := testHTTPClient(func(*http.Request) (*http.Response, error) { return testResponse(http.StatusOK, payload), nil })
			_, _, err := NewLoader(client).LoadRemote(t.Context(), "https://example.test/workflow.zip", LoadOptions{})
			if err == nil {
				t.Fatal("expected archive validation error")
			}
			if !strings.Contains(err.Error(), "workflow") && !strings.Contains(err.Error(), "manifest") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadRemoteGitHubUsesContentsAPIAndRawHeader(t *testing.T) {
	workflowData := []byte("version: 1\nname: github\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n")
	client := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.EscapedPath() != "/repos/acme/workflows/contents/ci/release%20file.yaml" {
			return nil, fmt.Errorf("path = %q", request.URL.EscapedPath())
		}
		if request.URL.Query().Get("ref") != "feature/release" {
			return nil, fmt.Errorf("ref = %q", request.URL.Query().Get("ref"))
		}
		if got := request.Header.Get("Accept"); got != "application/vnd.github.raw+json" {
			return nil, fmt.Errorf("Accept = %q", got)
		}
		return testResponse(http.StatusOK, workflowData), nil
	})
	definition, cleanup, err := NewLoader(client).LoadRemote(t.Context(), "github:acme/workflows@feature/release:ci/release file.yaml", LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if definition.Name != "github" {
		t.Fatalf("name = %q, want github", definition.Name)
	}
}

func TestLoadRemoteRejectsInsecureURLAndReportsGitHubFailure(t *testing.T) {
	loader := NewLoader(testHTTPClient(func(*http.Request) (*http.Response, error) {
		return testResponse(http.StatusNotFound, nil), nil
	}))
	if _, _, err := loader.LoadRemote(t.Context(), "http://example.test/workflow", LoadOptions{}); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("insecure URL error = %v", err)
	}
	if _, _, err := loader.LoadRemote(t.Context(), "github:acme/missing", LoadOptions{}); err == nil || !strings.Contains(err.Error(), "404 Not Found") {
		t.Fatalf("GitHub failure = %v", err)
	}
}
