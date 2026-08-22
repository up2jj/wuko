package workflow

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const validAction = `version: 1
name: remote-build
inputs:
  target:
    type: string
    required: true
outputs:
  result:
    value: steps.build.value
steps:
  - id: build
    type: capture
    with:
      value: "{{ .inputs.target }}"
`

func TestLoaderResolvesAndCachesHTTPSActions(t *testing.T) {
	var requests atomic.Int32
	client := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if request.URL.Path != "/v1/build" {
			t.Errorf("path = %q", request.URL.Path)
		}
		return testResponse(http.StatusOK, []byte(validAction)), nil
	})

	digest := sha256.Sum256([]byte(validAction))
	workflowPath := writeActionWorkflow(t, fmt.Sprintf(`version: 1
name: caller
vars: {release: default}
steps:
  - id: first
    uses: %s/{{ .vars.release }}/build?token=secret
    sha256: %x
    with: {target: linux}
  - id: second
    uses: %s/{{ .vars.release }}/build?token=secret
    sha256: %x
    with: {target: darwin}
`, "https://actions.example.test", digest, "https://actions.example.test", digest))

	definition, err := NewLoader(client).Load(t.Context(), workflowPath, LoadOptions{Vars: map[string]any{"release": "v1"}, RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if definition.Steps[0].Action == nil || definition.Steps[0].Action.Name != "remote-build" {
		t.Fatalf("action = %#v", definition.Steps[0].Action)
	}
	if got := definition.Steps[0].Action.Location; got.Source != "https://actions.example.test/v1/build" || got.Line != 1 {
		t.Fatalf("action location = %#v", got)
	}
	if got := definition.Steps[0].Action.Steps[0].Location; got.Source != "https://actions.example.test/v1/build" || got.Line == 0 {
		t.Fatalf("action step location = %#v", got)
	}
	if definition.Steps[0].Action != definition.Steps[1].Action {
		t.Fatal("identical action references did not share the load cache")
	}
	if strings.Contains(definition.Steps[0].Uses.URL, "{{") {
		t.Fatalf("uses was not rendered: %q", definition.Steps[0].Uses)
	}
}

func TestLoaderRendersActionSourceWithNamedTemplate(t *testing.T) {
	client := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v2/build" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		return testResponse(http.StatusOK, []byte(validAction)), nil
	})
	workflowPath := writeActionWorkflow(t, `version: 1
name: caller
templates:
  action_url: https://actions.example.test/v2/build
steps:
  - id: build
    uses: '{{ template "action_url" . }}'
    with: {target: linux}
`)
	if _, err := NewLoader(client).Load(t.Context(), workflowPath, LoadOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestLoaderResolvesActionInsideConcurrentGroup(t *testing.T) {
	client := testHTTPClient(func(*http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, []byte(validAction)), nil
	})
	workflowPath := writeActionWorkflow(t, `version: 1
name: caller
steps:
  - concurrent:
      max_concurrency: 2
      steps:
        - id: remote
          uses: https://actions.example.test/build
          with: {target: linux}
        - id: local
          type: shell
`)
	definition, err := NewLoader(client).Load(t.Context(), workflowPath, LoadOptions{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	children := definition.Steps[0].Concurrent.Steps
	if children[0].Action == nil || children[0].Action.Name != "remote-build" {
		t.Fatalf("resolved action = %#v", children[0].Action)
	}
}

func TestLoaderResolvesStaticActionsInsideFanoutControls(t *testing.T) {
	client := testHTTPClient(func(*http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, []byte(validAction)), nil
	})
	workflowPath := writeActionWorkflow(t, `version: 1
name: caller
vars: {targets: [linux]}
steps:
  - id: batch_action
    batch:
      items: vars.targets
      size: 1
      steps:
        - id: remote
          uses: https://actions.example.test/build
          with: {target: {expr: 'batch.items[0]'}}
  - id: foreach_action
    foreach:
      items: vars.targets
      steps:
        - id: remote
          uses: https://actions.example.test/build
          with: {target: {expr: foreach.item}}
  - id: matrix_action
    matrix:
      axes: {target: [linux]}
      steps:
        - id: remote
          uses: https://actions.example.test/build
          with: {target: {expr: matrix.target}}
`)
	definition, err := NewLoader(client).Load(t.Context(), workflowPath, LoadOptions{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if definition.Steps[0].Batch.Steps[0].Action == nil || definition.Steps[1].Foreach.Steps[0].Action == nil || definition.Steps[2].Matrix.Steps[0].Action == nil {
		t.Fatalf("actions were not resolved: %#v", definition.Steps)
	}

	workflowPath = writeActionWorkflow(t, `version: 1
name: invalid
vars: {targets: [linux]}
steps:
  - id: dynamic
    batch:
      items: vars.targets
      size: 1
      steps:
        - id: remote
          uses: https://actions.example.test/{{ .batch.items }}
`)
	if _, err := NewLoader(client).Load(t.Context(), workflowPath, LoadOptions{RunDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "batch") {
		t.Fatalf("dynamic action source error = %v", err)
	}
}

func TestLoaderRejectsChecksumAndNestedAction(t *testing.T) {
	client := testHTTPClient(func(*http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, []byte(validAction)), nil
	})

	t.Run("checksum mismatch", func(t *testing.T) {
		path := writeActionWorkflow(t, fmt.Sprintf("version: 1\nname: caller\nsteps:\n  - id: remote\n    uses: https://actions.example.test/action\n    sha256: %064d\n", 0))
		_, err := NewLoader(client).Load(t.Context(), path, LoadOptions{})
		if err == nil || !strings.Contains(err.Error(), "checksum") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("nested action", func(t *testing.T) {
		nested := strings.ReplaceAll(validAction, "type: capture", "uses: https://example.test/action")
		nestedClient := testHTTPClient(func(*http.Request) (*http.Response, error) { return testResponse(http.StatusOK, []byte(nested)), nil })
		path := writeActionWorkflow(t, "version: 1\nname: caller\nsteps:\n  - id: remote\n    uses: https://actions.example.test/action\n")
		_, err := NewLoader(nestedClient).Load(t.Context(), path, LoadOptions{})
		if err == nil || !strings.Contains(err.Error(), "nested remote actions") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestLoaderSupportsZIPAndTarGzipPackages(t *testing.T) {
	files := map[string]archiveTestFile{
		"action.yml":        {data: []byte(validAction), mode: 0o644},
		"scripts/build.lua": {data: []byte(`wuko.output("ok", true)`), mode: 0o755},
	}
	for name, payload := range map[string][]byte{
		"zip":    makeZIP(t, files),
		"tar.gz": makeTarGzip(t, files),
	} {
		t.Run(name, func(t *testing.T) {
			client := testHTTPClient(func(*http.Request) (*http.Response, error) { return testResponse(http.StatusOK, payload), nil })
			path := writeActionWorkflow(t, "version: 1\nname: caller\nsteps:\n  - id: remote\n    uses: https://actions.example.test/package\n")
			definition, err := NewLoader(client).Load(t.Context(), path, LoadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			dir, cleanup, err := definition.Steps[0].Action.Materialize()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, "scripts", "build.lua")); err != nil {
				t.Fatal(err)
			}
			cleanup()
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("temporary action directory remains: %v", err)
			}
		})
	}
}

func TestLoaderResolvesTemplateFileFromActionPackage(t *testing.T) {
	manifest := `version: 1
name: templated-action
templates:
  message:
    file: templates/message.tmpl
steps:
  - id: render
    type: capture
    with:
      value: '{{ template "message" . }}'
`
	payload := makeZIP(t, map[string]archiveTestFile{
		"action.yml":             {data: []byte(manifest), mode: 0o644},
		"templates/message.tmpl": {data: []byte("Hello {{ .inputs.name }}"), mode: 0o644},
	})
	client := testHTTPClient(func(*http.Request) (*http.Response, error) { return testResponse(http.StatusOK, payload), nil })
	path := writeActionWorkflow(t, "version: 1\nname: caller\nsteps:\n  - id: remote\n    uses: https://actions.example.test/package\n")
	definition, err := NewLoader(client).Load(t.Context(), path, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := definition.Steps[0].Action.Templates["message"].Body; got != "Hello {{ .inputs.name }}" {
		t.Fatalf("template body = %q", got)
	}
}

func TestLoaderRejectsTemplateFileInStandaloneAction(t *testing.T) {
	manifest := `version: 1
name: standalone
templates:
  message:
    file: templates/message.tmpl
steps:
  - id: run
    type: capture
`
	client := testHTTPClient(func(*http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, []byte(manifest)), nil
	})
	path := writeActionWorkflow(t, "version: 1\nname: caller\nsteps:\n  - id: remote\n    uses: https://actions.example.test/action\n")
	_, err := NewLoader(client).Load(t.Context(), path, LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "requires a packaged action") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoaderRunsAndCachesCommandActionSource(t *testing.T) {
	workflowDir := t.TempDir()
	counter := filepath.Join(workflowDir, "command-ran")
	workflowPath := filepath.Join(workflowDir, "workflow.yaml")
	data := `version: 1
name: command-source
vars: {release: default}
steps:
  - id: first
    uses:
      command: sh
      args:
        - -c
        - |
          test "$1" = "v1"
          test "$TOKEN" = "secret"
          test ! -e "$2" || exit 9
          : > "$2"
          printf '%s' "$ACTION_MANIFEST"
        - wuko
        - "{{ .vars.release }}"
        - "{{ .workflow.dir }}/command-ran"
    with: {target: linux}
  - id: second
    uses:
      command: sh
      args:
        - -c
        - |
          test "$1" = "v1"
          test "$TOKEN" = "secret"
          test ! -e "$2" || exit 9
          : > "$2"
          printf '%s' "$ACTION_MANIFEST"
        - wuko
        - "{{ .vars.release }}"
        - "{{ .workflow.dir }}/command-ran"
    with: {target: darwin}
`
	if err := os.WriteFile(workflowPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := NewLoader(nil).Load(t.Context(), workflowPath, LoadOptions{
		Vars:   map[string]any{"release": "v1"},
		Env:    map[string]string{"TOKEN": "secret", "ACTION_MANIFEST": validAction},
		RunDir: workflowDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(counter); err != nil {
		t.Fatalf("command did not run: %v", err)
	}
	if definition.Steps[0].Action != definition.Steps[1].Action {
		t.Fatal("identical command sources did not share the load cache")
	}
	if definition.Steps[0].Uses.Command != "sh" || definition.Steps[0].Uses.Args[3] != "v1" {
		t.Fatalf("resolved source = %#v", definition.Steps[0].Uses)
	}
}

func TestLoaderRunsCommandActionSourceFromScopedWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(root, "workflow.yaml")
	data := `version: 1
name: scoped-command-source
vars: {project: project}
steps:
  - working_directory: "{{ .vars.project }}"
    steps:
      - id: action
        uses:
          command: sh
          args:
            - -c
            - |
              test "$PWD" = "$1"
              printf '%s' "$ACTION_MANIFEST"
            - wuko
            - "{{ .run.dir }}"
        with: {target: linux}
`
	if err := os.WriteFile(workflowPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := NewLoader(nil).Load(t.Context(), workflowPath, LoadOptions{
		Env: map[string]string{"ACTION_MANIFEST": validAction}, RunDir: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	actionStep := definition.Steps[0].Steps[0]
	if actionStep.Action == nil || actionStep.Uses.Args[3] != project {
		t.Fatalf("resolved scoped action = %#v", actionStep)
	}
}

func TestLoaderRejectsCommandActionSourceWithRuntimeWorkingDirectory(t *testing.T) {
	workflowPath := writeActionWorkflow(t, `version: 1
name: dynamic-command-source
steps:
  - id: select
    type: shell
  - working_directory: "{{ .steps.select.dir }}"
    steps:
      - id: action
        uses:
          command: sh
          args: [-c, "printf '%s' \"$ACTION_MANIFEST\""]
`)
	_, err := NewLoader(nil).Load(t.Context(), workflowPath, LoadOptions{RunDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "resolved while loading") {
		t.Fatalf("runtime working_directory error = %v", err)
	}
}

func TestLoaderAcceptsArchiveFromCommandSource(t *testing.T) {
	payload := makeZIP(t, map[string]archiveTestFile{"action.yml": {data: []byte(validAction), mode: 0o644}})
	archivePath := filepath.Join(t.TempDir(), "action.zip")
	if err := os.WriteFile(archivePath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	workflowPath := writeActionWorkflow(t, "version: 1\nname: command-archive\nsteps:\n  - id: remote\n    uses:\n      command: cat\n      args: [\""+archivePath+"\"]\n    with: {target: linux}\n")
	definition, err := NewLoader(nil).Load(t.Context(), workflowPath, LoadOptions{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Steps[0].Action.Files) == 0 {
		t.Fatal("command archive was not decoded as a package")
	}
}

func TestLoaderReportsCommandFailureWithoutArguments(t *testing.T) {
	workflowPath := writeActionWorkflow(t, `version: 1
name: command-failure
steps:
  - id: remote
    uses:
      command: sh
      args: [-c, "printf 'access denied' >&2; exit 7", secret-argument]
`)
	_, err := NewLoader(nil).Load(t.Context(), workflowPath, LoadOptions{RunDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "status 7") || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "secret-argument") {
		t.Fatalf("error exposes command arguments: %v", err)
	}
}

func TestLoaderRejectsUnsafeArchive(t *testing.T) {
	payload := makeZIP(t, map[string]archiveTestFile{
		"action.yml": {data: []byte(validAction), mode: 0o644},
		"../escape":  {data: []byte("bad"), mode: 0o644},
	})
	client := testHTTPClient(func(*http.Request) (*http.Response, error) { return testResponse(http.StatusOK, payload), nil })
	path := writeActionWorkflow(t, "version: 1\nname: caller\nsteps:\n  - id: remote\n    uses: https://actions.example.test/package\n")
	_, err := NewLoader(client).Load(t.Context(), path, LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoaderEnforcesRemotePolicyAndManifestLimits(t *testing.T) {
	tests := []struct {
		name       string
		uses       string
		sha        string
		status     int
		body       []byte
		transport  roundTripFunc
		want       string
		notContain string
	}{
		{name: "HTTP", uses: "http://actions.example.test/action", want: "must use HTTPS"},
		{name: "userinfo", uses: "https://user:password@actions.example.test/action", want: "user information"},
		{name: "malformed checksum", uses: "https://actions.example.test/action", sha: "abc", want: "64-character"},
		{name: "non-success status", uses: "https://actions.example.test/action", status: http.StatusNotFound, want: "404 Not Found"},
		{name: "oversized direct manifest", uses: "https://actions.example.test/action", status: http.StatusOK, body: bytes.Repeat([]byte("x"), maxManifestSize+1), want: "manifest exceeds"},
		{name: "multiple documents", uses: "https://actions.example.test/action", status: http.StatusOK, body: []byte(validAction + "---\n{}\n"), want: "multiple YAML documents"},
		{
			name: "query redaction", uses: "https://actions.example.test/action?token=supersecret",
			transport: func(*http.Request) (*http.Response, error) {
				return nil, fmt.Errorf(`Get "https://actions.example.test/action?token=supersecret": boom`)
			},
			want: "fetching action", notContain: "supersecret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := tt.transport
			if transport == nil {
				transport = func(*http.Request) (*http.Response, error) {
					status := tt.status
					if status == 0 {
						status = http.StatusOK
					}
					body := tt.body
					if body == nil {
						body = []byte(validAction)
					}
					return testResponse(status, body), nil
				}
			}
			shaLine := ""
			if tt.sha != "" {
				shaLine = "    sha256: " + tt.sha + "\n"
			}
			filename := writeActionWorkflow(t, "version: 1\nname: caller\nsteps:\n  - id: remote\n    uses: "+tt.uses+"\n"+shaLine)
			_, err := NewLoader(testHTTPClient(transport)).Load(t.Context(), filename, LoadOptions{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if tt.notContain != "" && strings.Contains(err.Error(), tt.notContain) {
				t.Fatalf("error leaks %q: %v", tt.notContain, err)
			}
		})
	}
}

func TestLoaderHonorsCancellationAndRejectsInsecureRedirects(t *testing.T) {
	client := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	filename := writeActionWorkflow(t, "version: 1\nname: caller\nsteps:\n  - id: remote\n    uses: https://actions.example.test/action\n")
	_, err := NewLoader(client).Load(ctx, filename, LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("error = %v", err)
	}

	loader := NewLoader(client)
	request, err := http.NewRequest(http.MethodGet, "http://actions.example.test/action", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.client.CheckRedirect(request, nil); err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("redirect error = %v", err)
	}
	request, err = http.NewRequest(http.MethodGet, "https://user:secret@actions.example.test/action", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.client.CheckRedirect(request, nil); err == nil || !strings.Contains(err.Error(), "user information") {
		t.Fatalf("redirect error = %v", err)
	}
}

func TestLoaderRejectsAmbiguousArchiveManifest(t *testing.T) {
	payload := makeZIP(t, map[string]archiveTestFile{
		"action.yml":  {data: []byte(validAction), mode: 0o644},
		"action.yaml": {data: []byte(validAction), mode: 0o644},
	})
	client := testHTTPClient(func(*http.Request) (*http.Response, error) { return testResponse(http.StatusOK, payload), nil })
	filename := writeActionWorkflow(t, "version: 1\nname: caller\nsteps:\n  - id: remote\n    uses: https://actions.example.test/package\n")
	_, err := NewLoader(client).Load(t.Context(), filename, LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "multiple action manifests") {
		t.Fatalf("error = %v", err)
	}
}

func TestDiscoverDoesNotFetchRemoteActions(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := "version: 1\nname: offline\nsteps:\n  - id: remote\n    uses: https://unreachable.invalid/action\n"
	if err := os.WriteFile(filepath.Join(dir, "offline.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(root, "", ""); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testHTTPClient(function roundTripFunc) *http.Client {
	return &http.Client{Transport: function}
}

func testResponse(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}
}

func writeActionWorkflow(t *testing.T, data string) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := os.WriteFile(filename, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return filename
}

type archiveTestFile struct {
	data []byte
	mode os.FileMode
}

func makeZIP(t *testing.T, files map[string]archiveTestFile) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, file := range files {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(file.mode)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(file.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func makeTarGzip(t *testing.T, files map[string]archiveTestFile) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)
	for name, file := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: int64(file.mode), Size: int64(len(file.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(file.data); err != nil {
			t.Fatal(err)
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
