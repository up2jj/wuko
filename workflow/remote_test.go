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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestLoadRemoteReportsPreparationFailureAsLoadFailure(t *testing.T) {
	// Not parallel: this test snapshots the process-global os.TempDir()/wuko-workflow-*
	// namespace, so a concurrently-materializing test would look like a leak.
	workflowData := []byte("version: 1\nname: remote\nsteps:\n  - id: action\n    uses: https://actions.example.test/missing\n")
	client := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "example.test" {
			return testResponse(http.StatusOK, workflowData), nil
		}
		return testResponse(http.StatusNotFound, nil), nil
	})
	before := remoteWorkflowTempDirs(t)
	var events []diagnostic.Event
	_, _, err := NewLoader(client).LoadRemote(t.Context(), "https://example.test/workflow.yaml", LoadOptions{
		Diagnostics: func(event diagnostic.Event) { events = append(events, event) },
	})
	if err == nil {
		t.Fatal("LoadRemote() error = nil")
	}
	var statuses []diagnostic.Status
	for _, event := range events {
		if event.Phase == diagnostic.PhaseLoad {
			statuses = append(statuses, event.Status)
		}
	}
	if got, want := fmt.Sprint(statuses), fmt.Sprint([]diagnostic.Status{diagnostic.StatusStarted, diagnostic.StatusFailed}); got != want {
		t.Fatalf("load statuses = %s, want %s", got, want)
	}
	assertNoNewRemoteWorkflowTempDirs(t, before)
}

func TestDecodeRemoteRemovesMaterializedWorkflowOnDecodeFailure(t *testing.T) {
	// Not parallel: this test snapshots the process-global os.TempDir()/wuko-workflow-*
	// namespace, so a concurrently-materializing test would look like a leak.
	client := testHTTPClient(func(*http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, []byte("version: [")), nil
	})
	before := remoteWorkflowTempDirs(t)
	_, _, err := NewLoader(client).DecodeRemote(t.Context(), "https://example.test/workflow.yaml", LoadOptions{})
	if err == nil {
		t.Fatal("DecodeRemote() error = nil")
	}
	assertNoNewRemoteWorkflowTempDirs(t, before)
}

func TestLoadRemoteArchivesWorkflowAndCompanionFiles(t *testing.T) {
	t.Parallel()
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

func TestLoadMarketplacePackageRejectsPackageVersionMismatch(t *testing.T) {
	t.Parallel()
	sourceDir := t.TempDir()
	workflowData := []byte("version: 1\npackage_version: 1.0.0\nname: release\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n")
	if err := os.WriteFile(filepath.Join(sourceDir, "wuko.yaml"), workflowData, 0o644); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "release.tar.gz")
	_, archiveDigest, err := BuildWorkflowPackage(sourceDir, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archiveData, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	client := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://example.test/repo/packages/release.tar.gz" {
			return nil, fmt.Errorf("unexpected URL %s", request.URL)
		}
		return testResponse(http.StatusOK, archiveData), nil
	})
	item := MarketplacePackage{
		Name: "release", PackageVersion: "2.0.0", Source: ".wuko/workflows/release", Path: "packages/release.tar.gz",
		Format: "tar.gz", Entry: "wuko.yaml", SourceSHA256: strings.Repeat("a", 64), SHA256: archiveDigest,
	}
	_, _, cleanup, err := NewLoader(client).LoadMarketplacePackage(t.Context(), "https://example.test/repo", item, LoadOptions{})
	cleanup()
	if err == nil || !strings.Contains(err.Error(), "package_version") {
		t.Fatalf("error = %v, want package version mismatch", err)
	}
}

func TestLoadRemoteArchiveResolvesTemplateFiles(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestLoadRemoteRejectsArchivesWhenRequested(t *testing.T) {
	t.Parallel()
	payload := makeZIP(t, map[string]archiveTestFile{
		"wuko.yaml": {data: []byte("version: 1\nname: remote\nsteps:\n  - id: run\n    type: shell\n"), mode: 0o644},
	})
	client := testHTTPClient(func(*http.Request) (*http.Response, error) { return testResponse(http.StatusOK, payload), nil })
	_, _, err := NewLoader(client).LoadRemote(t.Context(), "https://example.test/workflow.zip", LoadOptions{RejectRemoteArchives: true})
	if err == nil || !strings.Contains(err.Error(), "archives are not supported") {
		t.Fatalf("error = %v, want archive rejection", err)
	}
}

func TestLoadRemoteArchiveResolvesLocalActionFromRequiredFragment(t *testing.T) {
	t.Parallel()
	payload := makeZIP(t, map[string]archiveTestFile{
		"wuko.yaml": {
			data: []byte("version: 1\nname: remote\nsteps:\n  - require: fragments/build.yaml\n"),
			mode: 0o644,
		},
		"fragments/build.yaml": {
			data: []byte("- id: build\n  uses: ../actions/build\n  with: {target: linux}\n"),
			mode: 0o644,
		},
		"actions/build/action.yaml": {data: []byte(validAction), mode: 0o644},
	})
	client := testHTTPClient(func(*http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, payload), nil
	})

	definition, cleanup, err := NewLoader(client).LoadRemote(t.Context(), "https://example.test/workflow.zip", LoadOptions{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if definition.Steps[0].Action == nil {
		t.Fatal("local action was not resolved from remote workflow archive")
	}
	if got := definition.Steps[0].Action.Location.Source; got != "https://example.test/workflow.zip::actions/build/action.yaml" {
		t.Fatalf("action source = %q", got)
	}
}

func TestLoadRemoteRejectsInvalidWorkflowArchives(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func remoteWorkflowTempDirs(t *testing.T) map[string]struct{} {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(os.TempDir(), "wuko-workflow-*"))
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		result[path] = struct{}{}
	}
	return result
}

func assertNoNewRemoteWorkflowTempDirs(t *testing.T, before map[string]struct{}) {
	t.Helper()
	for path := range remoteWorkflowTempDirs(t) {
		if _, existed := before[path]; !existed {
			t.Errorf("remote workflow temporary directory remains: %s", path)
		}
	}
}
