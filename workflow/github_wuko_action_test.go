package workflow

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGitHubHostedWukoActionSourceSchema(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		value      string
		wantGitHub string
		wantToken  string
		wantPath   string
		wantError  string
	}{
		{name: "repository directory", value: "github: acme/actions@main:build\n", wantGitHub: "acme/actions@main:build"},
		{name: "default branch", value: "github: acme/actions:build\n", wantGitHub: "acme/actions:build"},
		{name: "branch with slash", value: "github: acme/actions@feature/release:build\n", wantGitHub: "acme/actions@feature/release:build"},
		{name: "tag", value: "github: acme/actions@v2.1.0:build\n", wantGitHub: "acme/actions@v2.1.0:build"},
		{name: "commit", value: "github: acme/actions@0123456789abcdef0123456789abcdef01234567:build\n", wantGitHub: "acme/actions@0123456789abcdef0123456789abcdef01234567:build"},
		{name: "token", value: "github: acme/actions:build\ntoken: '{{ .env.GH_TOKEN }}'\n", wantGitHub: "acme/actions:build", wantToken: "{{ .env.GH_TOKEN }}"},
		{name: "missing directory", value: "github: acme/actions@main\n", wantError: "expected owner/repo[@ref]:directory"},
		{name: "manifest path", value: "github: acme/actions@main:build/action.yaml\n", wantError: "action directory"},
		{name: "unsafe directory", value: "github: acme/actions@main:../build\n", wantError: "relative safe path"},
		{name: "traversing ref", value: "github: acme/actions@../../main:build\n", wantError: "ref is invalid"},
		{name: "absolute ref", value: "github: acme/actions@/main:build\n", wantError: "ref is invalid"},
		{name: "empty token", value: "github: acme/actions:build\ntoken: ''\n", wantError: "token must not be empty"},
		{name: "token without GitHub", value: "command: fetch\ntoken: secret\n", wantError: "token requires github"},
		{name: "mixed command", value: "github: acme/actions:build\ncommand: fetch\n", wantError: "cannot be combined"},
		{name: "unknown field", value: "github: acme/actions:build\nenterprise: github.example.com\n", wantError: "field enterprise not found"},
		{name: "sha256 in uses", value: "github: acme/actions:build\nsha256: digest\n", wantError: "field sha256 not found"},
		{name: "scalar prefixed locator", value: "github:acme/actions@main:build\n", wantGitHub: "acme/actions@main:build"},
		{name: "scalar bare locator", value: "acme/actions@main:build\n", wantGitHub: "acme/actions@main:build"},
		{name: "scalar default branch locator", value: "acme/actions:build\n", wantGitHub: "acme/actions:build"},
		{name: "scalar branch with slash", value: "acme/actions@feature/release:build\n", wantGitHub: "acme/actions@feature/release:build"},
		{name: "scalar templated locator", value: "'{{ .env.ORG }}/actions@main:build'\n", wantGitHub: "{{ .env.ORG }}/actions@main:build"},
		{name: "scalar invalid locator", value: "acme/actions@main:../build\n", wantError: "relative safe path"},
		{name: "scalar empty prefixed locator", value: "'github:'\n", wantError: "expected owner/repo[@ref]:directory"},
		{name: "scalar local path", value: "actions/build\n", wantPath: "actions/build"},
		{name: "scalar nested local path", value: "./actions/build:staging\n", wantPath: "./actions/build:staging"},
		{name: "scalar deep local path", value: ".wuko/actions/build\n", wantPath: ".wuko/actions/build"},
		{name: "scalar templated local path", value: "'{{ .env.DIR }}/actions/build'\n", wantPath: "{{ .env.DIR }}/actions/build"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var source ActionSource
			err := yaml.Unmarshal([]byte(tt.value), &source)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if source.GitHub != tt.wantGitHub || source.Token != tt.wantToken || source.Path != tt.wantPath {
				t.Fatalf("source = %#v", source)
			}
			if source.GitHub != "" && !strings.Contains(source.GitHub, "{{") {
				if _, err := parseGitHubWukoActionLocator(source.GitHub); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestGitHubCredentialsPrecedenceAndStoredLookup(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	stored := func(context.Context, map[string]string) string {
		calls.Add(1)
		return "stored-token"
	}
	credentials := newGitHubCredentials(map[string]string{"GH_TOKEN": "gh-token", "GITHUB_TOKEN": "github-token"}, stored)
	if got := credentials.token(t.Context(), "explicit-token"); got != "explicit-token" {
		t.Fatalf("explicit token = %q", got)
	}
	if got := credentials.token(t.Context(), ""); got != "gh-token" {
		t.Fatalf("environment token = %q", got)
	}
	if calls.Load() != 0 {
		t.Fatalf("stored lookup calls = %d", calls.Load())
	}

	credentials = newGitHubCredentials(map[string]string{"GITHUB_TOKEN": "github-token"}, stored)
	if got := credentials.token(t.Context(), ""); got != "github-token" {
		t.Fatalf("GITHUB_TOKEN = %q", got)
	}

	credentials = newGitHubCredentials(nil, stored)
	if got := credentials.token(t.Context(), ""); got != "stored-token" {
		t.Fatalf("stored token = %q", got)
	}
	if got := credentials.token(t.Context(), ""); got != "stored-token" {
		t.Fatalf("cached stored token = %q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("stored lookup calls = %d, want 1", calls.Load())
	}
}

func TestLoaderFetchesGitHubHostedWukoActionDirectory(t *testing.T) {
	t.Parallel()
	manifest := `version: 1
name: github-build
inputs:
  target: {type: string, required: true}
outputs:
  artifact: {value: steps.build.stdout}
templates:
  message: {file: templates/message.tmpl}
steps:
  - id: build
    type: shell
    with: {command: true}
`
	binary := []byte{0x00, 0x01, 0xfe, 0xff}
	tarball := makeGitHubTarball(t, "acme-private-actions-0123456", []githubArchiveEntry{
		{name: "README.md", data: []byte("# unrelated")},
		{name: "actions", typeflag: tar.TypeDir},
		{name: "actions/build", typeflag: tar.TypeDir},
		{name: "actions/build/action.yaml", data: []byte(manifest)},
		{name: "actions/build/scripts/build.sh", data: []byte("#!/bin/sh\nexit 0\n"), mode: 0o755},
		{name: "actions/build/templates/message.tmpl", data: []byte("building {{ .inputs.target }}")},
		{name: "actions/build/bin/helper", data: binary, mode: 0o755},
		{name: "actions/other/action.yaml", data: []byte("# a sibling action that must not be packaged")},
	})
	var requests atomic.Int32
	client := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if got := request.Header.Get("Authorization"); got != "Bearer private-token" {
			return nil, fmt.Errorf("authorization = %q", got)
		}
		if request.Header.Get("X-GitHub-Api-Version") != githubAPIVersion {
			return nil, fmt.Errorf("missing API version")
		}
		if request.URL.EscapedPath() != "/repos/acme/private-actions/tarball/main" {
			return nil, fmt.Errorf("unexpected request %s", request.URL)
		}
		return testResponse(http.StatusOK, tarball), nil
	})
	loader := NewLoader(client)
	loader.githubStoredTokenFn = func(context.Context, map[string]string) string {
		t.Fatal("stored token lookup should not run")
		return ""
	}
	workflowPath := writeActionWorkflow(t, `version: 1
name: caller
env: {PRIVATE_TOKEN: private-token}
steps:
  - id: first
    uses:
      github: acme/private-actions@main:actions/build
      token: "{{ .env.PRIVATE_TOKEN }}"
    with: {target: linux}
  - id: second
    uses:
      github: acme/private-actions@main:actions/build
      token: "{{ .env.PRIVATE_TOKEN }}"
    with: {target: darwin}
`)
	definition, err := loader.Load(t.Context(), workflowPath, LoadOptions{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	first := definition.Steps[0]
	if first.Action == nil || first.Action.Name != "github-build" {
		t.Fatalf("action = %#v", first.Action)
	}
	if first.Action != definition.Steps[1].Action {
		t.Fatal("identical GitHub-hosted Wuko actions did not share the load cache")
	}
	if first.Uses.Token != "" || first.Uses.Display() != "github:acme/private-actions@main:actions/build" {
		t.Fatalf("resolved source = %#v", first.Uses)
	}
	if !bytes.Equal(first.Action.Files["bin/helper"].Data, binary) || first.Action.Files["bin/helper"].Mode.Perm() != 0o755 {
		t.Fatalf("binary file = %#v", first.Action.Files["bin/helper"])
	}
	if first.Action.Files["scripts/build.sh"].Mode.Perm() != 0o755 {
		t.Fatalf("script mode = %v", first.Action.Files["scripts/build.sh"].Mode)
	}
	if got := first.Action.Templates["message"].Body; got != "building {{ .inputs.target }}" {
		t.Fatalf("template = %q", got)
	}
	for name := range first.Action.Files {
		if strings.Contains(name, "README") || strings.Contains(name, "other") {
			t.Fatalf("packaged a file from outside the action directory: %q", name)
		}
	}
	// One request per file exhausts the 60-request anonymous hourly budget, so the whole
	// directory must arrive in a single repository archive.
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestLoaderGitHubHostedWukoActionUsesDefaultBranchAndAnonymousAccess(t *testing.T) {
	t.Parallel()
	tarball := makeGitHubTarball(t, "acme-public-actions-0123456", []githubArchiveEntry{
		{name: "build/action.yml", data: []byte(validAction)},
	})
	client := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "" {
			return nil, fmt.Errorf("unexpected authorization")
		}
		// An omitted ref must not cost a separate default-branch lookup.
		if request.URL.EscapedPath() != "/repos/acme/public-actions/tarball" {
			return nil, fmt.Errorf("unexpected request %s", request.URL)
		}
		return testResponse(http.StatusOK, tarball), nil
	})
	loader := NewLoader(client)
	loader.githubStoredTokenFn = func(context.Context, map[string]string) string { return "" }
	workflowPath := writeActionWorkflow(t, "version: 1\nname: caller\nsteps:\n  - id: build\n    uses:\n      github: acme/public-actions:build\n    with: {target: linux}\n")
	if _, err := loader.Load(t.Context(), workflowPath, LoadOptions{RunDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func TestLoaderGitHubHostedWukoActionAcceptsScalarLocator(t *testing.T) {
	t.Parallel()
	tarball := makeGitHubTarball(t, "acme-public-actions-0123456", []githubArchiveEntry{
		{name: "build/action.yml", data: []byte(validAction)},
	})
	client := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.EscapedPath() != "/repos/acme/public-actions/tarball/main" {
			return nil, fmt.Errorf("unexpected request %s", request.URL)
		}
		return testResponse(http.StatusOK, tarball), nil
	})
	loader := NewLoader(client)
	loader.githubStoredTokenFn = func(context.Context, map[string]string) string { return "" }
	workflowPath := writeActionWorkflow(t, `version: 1
name: caller
steps:
  - id: bare
    uses: acme/public-actions@main:build
    with: {target: linux}
  - id: prefixed
    uses: github:acme/public-actions@main:build
    with: {target: darwin}
`)
	definition, err := loader.Load(t.Context(), workflowPath, LoadOptions{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	bare, prefixed := definition.Steps[0], definition.Steps[1]
	if bare.Uses.Path != "" || bare.Uses.Display() != "github:acme/public-actions@main:build" {
		t.Fatalf("bare source = %#v", bare.Uses)
	}
	if bare.Action == nil || bare.Action != prefixed.Action {
		t.Fatal("both scalar spellings did not resolve to one cached action")
	}
}

func TestLoaderGitHubHostedWukoActionRejectsSHA256AndRedactsToken(t *testing.T) {
	t.Parallel()
	loader := NewLoader(testHTTPClient(func(*http.Request) (*http.Response, error) {
		return testResponse(http.StatusNotFound, nil), nil
	}))
	loader.githubStoredTokenFn = func(context.Context, map[string]string) string { return "" }
	withSHA := writeActionWorkflow(t, "version: 1\nname: caller\nsteps:\n  - id: build\n    uses:\n      github: acme/private:build\n      token: private-secret-token\n    sha256: "+strings.Repeat("a", 64)+"\n")
	if _, err := loader.Load(t.Context(), withSHA, LoadOptions{RunDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "sha256 is not supported") {
		t.Fatalf("sha256 error = %v", err)
	}

	withoutSHA := writeActionWorkflow(t, "version: 1\nname: caller\nsteps:\n  - id: build\n    uses:\n      github: acme/private:build\n      token: private-secret-token\n")
	_, err := loader.Load(t.Context(), withoutSHA, LoadOptions{RunDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "provide uses.token") {
		t.Fatalf("access error = %v", err)
	}
	if strings.Contains(err.Error(), "private-secret-token") {
		t.Fatalf("error exposes token: %v", err)
	}
}

func TestLoaderGitHubHostedWukoActionRejectsRenderedEmptyExplicitToken(t *testing.T) {
	t.Parallel()
	loader := NewLoader(testHTTPClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("GitHub API request should not be sent")
		return nil, nil
	}))
	loader.githubStoredTokenFn = func(context.Context, map[string]string) string {
		t.Fatal("stored token lookup should not run")
		return ""
	}
	workflowPath := writeActionWorkflow(t, `version: 1
name: caller
steps:
  - id: build
    uses:
      github: acme/private:build
      token: '{{ "" }}'
`)
	_, err := loader.Load(t.Context(), workflowPath, LoadOptions{RunDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "rendered GitHub-hosted Wuko action token is empty") {
		t.Fatalf("empty token error = %v", err)
	}
}

func TestGitHubAuthorizationIsRemovedOnCrossHostRedirect(t *testing.T) {
	t.Parallel()
	loader := NewLoader(nil)
	request, err := http.NewRequest(http.MethodGet, "https://objects.githubusercontent.com/archive", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer secret")
	if err := loader.client.CheckRedirect(request, nil); err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Authorization") != "" {
		t.Fatal("authorization survived cross-host redirect")
	}
}

func TestGitHubHostedWukoActionKeepsSlashesInRef(t *testing.T) {
	t.Parallel()
	tarball := makeGitHubTarball(t, "acme-actions-0123456", []githubArchiveEntry{
		{name: "build/action.yml", data: []byte(validAction)},
	})
	client := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.EscapedPath() != "/repos/acme/actions/tarball/feature/release" {
			return nil, fmt.Errorf("unexpected request %s", request.URL.EscapedPath())
		}
		return testResponse(http.StatusOK, tarball), nil
	})
	loader := NewLoader(client)
	loader.githubStoredTokenFn = func(context.Context, map[string]string) string { return "" }
	locator, err := parseGitHubWukoActionLocator("acme/actions@feature/release:build")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.fetchGitHubWukoAction(t.Context(), locator, ""); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubHostedWukoActionRejectsUnsafeRepositoryArchives(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		entries []githubArchiveEntry
		want    string
	}{
		{
			name:    "missing directory",
			entries: []githubArchiveEntry{{name: "other/action.yml", data: []byte(validAction)}},
			want:    "does not exist",
		},
		{
			name:    "path is a file",
			entries: []githubArchiveEntry{{name: "build", data: []byte(validAction)}},
			want:    "must identify a directory",
		},
		{
			name: "symlink",
			entries: []githubArchiveEntry{
				{name: "build/action.yml", data: []byte(validAction)},
				{name: "build/link", typeflag: tar.TypeSymlink, link: "../../../etc/passwd"},
			},
			want: "symlink",
		},
		{
			name: "traversing path",
			entries: []githubArchiveEntry{
				{name: "build/action.yml", data: []byte(validAction)},
				{name: "../escape", data: []byte("x")},
			},
			want: "unsafe path",
		},
		{
			name: "duplicate path",
			entries: []githubArchiveEntry{
				{name: "build/action.yml", data: []byte(validAction)},
				{name: "build/action.yml", data: []byte(validAction)},
			},
			want: "duplicate path",
		},
		{
			name:    "empty directory",
			entries: []githubArchiveEntry{{name: "build", typeflag: tar.TypeDir}},
			want:    "contains no files",
		},
		{
			name:    "second root",
			entries: []githubArchiveEntry{{name: "build/action.yml", data: []byte(validAction), root: "other-root"}},
			want:    "more than one root directory",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tarball := makeGitHubTarball(t, "acme-actions-0123456", append([]githubArchiveEntry{{name: "keep/action.yml", data: []byte(validAction)}}, tt.entries...))
			client := testHTTPClient(func(*http.Request) (*http.Response, error) {
				return testResponse(http.StatusOK, tarball), nil
			})
			loader := NewLoader(client)
			loader.githubStoredTokenFn = func(context.Context, map[string]string) string { return "" }
			locator := githubWukoActionLocator{owner: "acme", repository: "actions", ref: "main", directory: "build"}
			_, err := loader.fetchGitHubWukoAction(t.Context(), locator, "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestGitHubHostedWukoActionReportsExhaustedRateLimit(t *testing.T) {
	t.Parallel()
	client := testHTTPClient(func(*http.Request) (*http.Response, error) {
		response := testResponse(http.StatusForbidden, []byte(`{"message":"API rate limit exceeded"}`))
		response.Header.Set("X-RateLimit-Remaining", "0")
		return response, nil
	})
	loader := NewLoader(client)
	loader.githubStoredTokenFn = func(context.Context, map[string]string) string { return "" }
	locator := githubWukoActionLocator{owner: "acme", repository: "actions", ref: "main", directory: "build"}
	_, err := loader.fetchGitHubWukoAction(t.Context(), locator, "")
	if err == nil || !strings.Contains(err.Error(), "rate limit is exhausted") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "was not found") {
		t.Fatalf("rate limit reported as an access failure: %v", err)
	}
}

func TestGitHubHostedWukoActionRejectsOversizedRepositoryArchive(t *testing.T) {
	t.Parallel()
	// A file outside the action directory is skipped rather than read, so only the bound on the
	// decompressed stream stops a small archive from expanding without limit.
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)
	oversized := int64(maxGitHubRepositoryArchive) + 1
	if err := writer.WriteHeader(&tar.Header{Name: "acme-actions-0123456/blob", Mode: 0o644, Size: oversized, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for written := int64(0); written < oversized; written += int64(len(chunk)) {
		if _, err := writer.Write(chunk[:min(int64(len(chunk)), oversized-written)]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	client := testHTTPClient(func(*http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, buffer.Bytes()), nil
	})
	loader := NewLoader(client)
	loader.githubStoredTokenFn = func(context.Context, map[string]string) string { return "" }
	locator := githubWukoActionLocator{owner: "acme", repository: "actions", ref: "main", directory: "build"}
	_, err := loader.fetchGitHubWukoAction(t.Context(), locator, "")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v", err)
	}
}

type githubArchiveEntry struct {
	name     string
	root     string
	data     []byte
	mode     os.FileMode
	typeflag byte
	link     string
}

// makeGitHubTarball builds the shape GitHub's repository tarball has: every path lives under one
// owner-repository-commit root directory.
func makeGitHubTarball(t *testing.T, root string, entries []githubArchiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)
	if err := writer.WriteHeader(&tar.Header{Name: root + "/", Mode: 0o755, Typeflag: tar.TypeDir, Format: tar.FormatPAX}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		prefix := root
		if entry.root != "" {
			prefix = entry.root
		}
		header := &tar.Header{Name: prefix + "/" + entry.name, Mode: int64(mode), Typeflag: typeflag, Linkname: entry.link, Format: tar.FormatPAX}
		if typeflag == tar.TypeReg {
			header.Size = int64(len(entry.data))
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := writer.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
