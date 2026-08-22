package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/up2jj/wuko/step"
)

func TestOperationValidation(t *testing.T) {
	validDigest := "sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		name    string
		raw     map[string]any
		wantErr string
	}{
		{name: "implicit run", raw: map[string]any{"image": "alpine"}},
		{name: "pull", raw: map[string]any{"operation": "pull", "image": "alpine"}},
		{name: "push auth", raw: map[string]any{"operation": "push", "image": "example/app", "auth": map[string]any{"username": "a", "password": "b"}}},
		{name: "verify", raw: map[string]any{"operation": "verify_digest", "image": "alpine", "expected_digest": validDigest}},
		{name: "templated verify", raw: map[string]any{"operation": "verify_digest", "image": "alpine", "expected_digest": "{{ .steps.push.digest }}"}},
		{name: "invalid digest", raw: map[string]any{"operation": "verify_digest", "image": "alpine", "expected_digest": "latest"}, wantErr: "valid OCI digest"},
		{name: "build", raw: map[string]any{"operation": "build", "tags": []any{"app:test"}, "output": "load", "pull": true}},
		{name: "templated build fields", raw: map[string]any{"operation": "build", "tags": []any{"app:test"}, "output": "{{ .vars.output }}", "platforms": []any{"{{ .vars.platform }}"}}},
		{name: "persistent volume", raw: map[string]any{"operation": "volume_create", "name": "data", "cleanup": false}},
		{name: "volume mount", raw: map[string]any{"image": "alpine", "mounts": []any{map[string]any{"type": "volume", "source": "data", "target": "/data"}}}},
		{name: "templated run enums", raw: map[string]any{"image": "alpine", "platform": "{{ .vars.platform }}", "pull": "{{ .vars.pull }}", "mounts": []any{map[string]any{"type": "{{ .vars.mount_type }}", "source": "data", "target": "{{ .vars.mount_target }}"}}}},
		{name: "run rejects boolean pull", raw: map[string]any{"image": "alpine", "pull": true}, wantErr: "policy string"},
		{name: "build rejects policy pull", raw: map[string]any{"operation": "build", "tags": []any{"app:test"}, "output": "load", "pull": "always"}, wantErr: "boolean for build"},
		{name: "operation fields isolated", raw: map[string]any{"operation": "inspect", "image": "alpine", "command": "true"}, wantErr: "command is not allowed for inspect"},
		{name: "login requires server", raw: map[string]any{"operation": "login", "auth": map[string]any{"username": "a", "password": "b"}}, wantErr: "server_address"},
		{name: "auth modes are exclusive", raw: map[string]any{"operation": "pull", "image": "alpine", "auth": map[string]any{"username": "a", "password": "b", "identity_token": "token"}}, wantErr: "exactly one"},
		{name: "invalid mount type", raw: map[string]any{"image": "alpine", "mounts": []any{map[string]any{"type": "tmpfs", "source": "data", "target": "/data"}}}, wantErr: "type must be bind or volume"},
		{name: "multi platform cannot load", raw: map[string]any{"operation": "build", "tags": []any{"app:test"}, "output": "load", "platforms": []any{"linux/amd64", "linux/arm64"}}, wantErr: "at most one platform"},
		{name: "reserved resource label", raw: map[string]any{"operation": "network_create", "name": "test", "labels": map[string]any{managedLabel: "false"}}, wantErr: "reserved by Wuko"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.raw)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("New() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("New() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestPullStreamsProgressAndReturnsInspection(t *testing.T) {
	stream := &fakePullResponse{messages: []jsonstream.Message{{ID: "layer", Status: "Downloaded"}}}
	inspected := imageInspection()
	client := &operationClient{fakeClient: &fakeClient{}, inspect: inspected, pull: stream}
	runner := &Runner{
		config:    Config{Operation: operationPull, Image: "registry.example/app:1", Platform: "linux/amd64", Auth: &Auth{Username: "alice", Password: "secret"}},
		newClient: func() (dockerClient, error) { return client, nil },
	}
	var progress bytes.Buffer

	result, err := runner.Run(t.Context(), step.Request{Stdout: &progress})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := result.Outputs["id"]; got != inspected.ID {
		t.Fatalf("id = %#v, want %q", got, inspected.ID)
	}
	if !strings.Contains(progress.String(), "layer: Downloaded") {
		t.Fatalf("progress = %q", progress.String())
	}
	decoded, err := authconfig.Decode(client.pullOptions.RegistryAuth)
	if err != nil {
		t.Fatalf("decoding auth: %v", err)
	}
	if decoded.Username != "alice" || decoded.Password != "secret" {
		t.Fatalf("auth = %#v", decoded)
	}
	if len(client.pullOptions.Platforms) != 1 || client.pullOptions.Platforms[0].Architecture != "amd64" {
		t.Fatalf("platforms = %#v", client.pullOptions.Platforms)
	}
}

func TestPullReturnsProgressError(t *testing.T) {
	stream := &fakePullResponse{messages: []jsonstream.Message{{Error: &jsonstream.Error{Message: "registry rejected request"}}}}
	client := &operationClient{fakeClient: &fakeClient{}, pull: stream}
	runner := &Runner{
		config:    Config{Operation: operationPull, Image: "registry.example/app:1"},
		newClient: func() (dockerClient, error) { return client, nil },
	}
	if _, err := runner.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "registry rejected request") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestPushTagAndLogin(t *testing.T) {
	raw := json.RawMessage(`{"Digest":"sha256:` + strings.Repeat("b", 64) + `"}`)
	client := &operationClient{
		fakeClient: &fakeClient{},
		push:       &fakePullResponse{messages: []jsonstream.Message{{Aux: &raw}}},
		login:      client.RegistryLoginResult{Auth: registry.AuthResponse{Status: "Login Succeeded"}},
	}

	push := &Runner{config: Config{Operation: operationPush, Image: "example/app:1"}, newClient: func() (dockerClient, error) { return client, nil }}
	result, err := push.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatalf("push Run() error = %v", err)
	}
	if result.Outputs["digest"] != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("push digest = %#v", result.Outputs["digest"])
	}

	tag := &Runner{config: Config{Operation: operationTag, Source: "example/app:1", Target: "example/app:stable"}, newClient: func() (dockerClient, error) { return client, nil }}
	if _, err := tag.Run(t.Context(), step.Request{}); err != nil {
		t.Fatalf("tag Run() error = %v", err)
	}
	if client.tagOptions.Source != "example/app:1" || client.tagOptions.Target != "example/app:stable" {
		t.Fatalf("tag options = %#v", client.tagOptions)
	}

	login := &Runner{config: Config{Operation: operationLogin, Auth: &Auth{ServerAddress: "registry.example", IdentityToken: "token"}}, newClient: func() (dockerClient, error) { return client, nil }}
	result, err = login.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatalf("login Run() error = %v", err)
	}
	if result.Outputs["status"] != "Login Succeeded" || client.loginOptions.IdentityToken != "token" {
		t.Fatalf("login result/options = %#v/%#v", result.Outputs, client.loginOptions)
	}
}

func TestPushUsesInspectedDescriptorWhenStreamOmitsDigest(t *testing.T) {
	inspected := imageInspection()
	client := &operationClient{
		fakeClient: &fakeClient{},
		inspect:    inspected,
		push:       &fakePullResponse{},
	}
	runner := &Runner{
		config:    Config{Operation: operationPush, Image: "example/app:1"},
		newClient: func() (dockerClient, error) { return client, nil },
	}

	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Outputs["digest"] != inspected.Descriptor.Digest.String() {
		t.Fatalf("digest = %#v, want %q", result.Outputs["digest"], inspected.Descriptor.Digest)
	}
}

func TestInspectAndVerifyDigest(t *testing.T) {
	inspected := imageInspection()
	client := &operationClient{fakeClient: &fakeClient{}, inspect: inspected}
	inspect := &Runner{config: Config{Operation: operationInspect, Image: "example/app:1"}, newClient: func() (dockerClient, error) { return client, nil }}
	result, err := inspect.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatalf("inspect Run() error = %v", err)
	}
	if result.Outputs["platform"] != "linux/amd64" || result.Outputs["size"] != int64(42) {
		t.Fatalf("inspect outputs = %#v", result.Outputs)
	}

	expected := inspected.Descriptor.Digest.String()
	verify := &Runner{config: Config{Operation: operationVerifyDigest, Image: "example/app:1", ExpectedDigest: expected}, newClient: func() (dockerClient, error) { return client, nil }}
	result, err = verify.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatalf("verify Run() error = %v", err)
	}
	if result.Outputs["verified"] != true || result.Outputs["actual_digest"] != expected {
		t.Fatalf("verify outputs = %#v", result.Outputs)
	}

	repositoryDigest := "sha256:" + strings.Repeat("d", 64)
	client.inspect.Descriptor = nil
	verify.config.ExpectedDigest = repositoryDigest
	result, err = verify.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatalf("repository digest Run() error = %v", err)
	}
	if result.Outputs["actual_digest"] != repositoryDigest {
		t.Fatalf("repository digest outputs = %#v", result.Outputs)
	}

	verify.config.ExpectedDigest = "sha256:" + strings.Repeat("f", 64)
	if _, err := verify.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("verify mismatch error = %v", err)
	}
}

func TestManagedResourcesCreateAndCleanup(t *testing.T) {
	tests := []struct {
		name       string
		raw        map[string]any
		configure  func(*operationClient)
		wantRemove func(*operationClient) string
	}{
		{
			name: "network", raw: map[string]any{"operation": "network_create", "name": "workflow-net", "driver": "bridge"},
			configure:  func(client *operationClient) { client.network = clientpkgNetworkResult("network-id") },
			wantRemove: func(client *operationClient) string { return client.removedNetwork },
		},
		{
			name: "volume", raw: map[string]any{"operation": "volume_create", "name": "workflow-data", "driver": "local"},
			configure: func(operationClient *operationClient) {
				operationClient.volume = client.VolumeCreateResult{Volume: volume.Volume{Name: "workflow-data", Driver: "local", Scope: "local", Mountpoint: "/var/lib/docker/volumes/workflow-data"}}
			},
			wantRemove: func(client *operationClient) string { return client.removedVolume },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			built, err := New(test.raw)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			managed, ok := built.(*managedRunner)
			if !ok {
				t.Fatalf("runner type = %T, want managed runner", built)
			}
			client := &operationClient{fakeClient: &fakeClient{}}
			test.configure(client)
			managed.newClient = func() (dockerClient, error) { return client, nil }
			result, err := managed.Run(t.Context(), step.Request{WorkflowName: "release", StepID: test.name})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if err := managed.Cleanup(result); err != nil {
				t.Fatalf("Cleanup() error = %v", err)
			}
			if got := test.wantRemove(client); got == "" {
				t.Fatal("managed resource was not removed")
			}
			labels := client.resourceLabels()
			if labels[managedLabel] != "true" || labels[workflowLabel] != "release" || labels[stepLabel] != test.name {
				t.Fatalf("labels = %#v", labels)
			}
		})
	}
}

func TestManagedResourceCleanupCanBeDisabled(t *testing.T) {
	built, err := New(map[string]any{"operation": "volume_create", "name": "persistent", "cleanup": false})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := built.(step.Cleaner); ok {
		t.Fatalf("runner type %T unexpectedly implements step.Cleaner", built)
	}
	runner := built.(*Runner)
	client := &operationClient{
		fakeClient: &fakeClient{},
		volume:     client.VolumeCreateResult{Volume: volume.Volume{Name: "persistent"}},
	}
	runner.newClient = func() (dockerClient, error) { return client, nil }

	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Outputs["name"] != "persistent" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if client.volumeOptions.Labels[managedLabel] != "false" {
		t.Fatalf("labels = %#v", client.volumeOptions.Labels)
	}
}

func TestManagedResourcesRejectCollisionsAndIgnoreMissingCleanup(t *testing.T) {
	built, err := New(map[string]any{"operation": "network_create", "name": "existing"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	managed := built.(*managedRunner)
	client := &operationClient{fakeClient: &fakeClient{}, networkExists: true}
	managed.newClient = func() (dockerClient, error) { return client, nil }
	if _, err := managed.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("collision error = %v", err)
	}
	if client.networkName != "" {
		t.Fatal("network was created despite collision")
	}

	client.networkExists = false
	client.network = clientpkgNetworkResult("network-id")
	result, err := managed.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	client.networkRemoveErr = errdefs.ErrNotFound
	if err := managed.Cleanup(result); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
}

func TestManagedResourceCloseErrorStillRegistersUsableResult(t *testing.T) {
	built, err := New(map[string]any{"operation": "network_create", "name": "managed"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	managed := built.(*managedRunner)
	client := &operationClient{
		fakeClient: &fakeClient{closeErr: errors.New("close failed")},
		network:    clientpkgNetworkResult("network-id"),
	}
	managed.newClient = func() (dockerClient, error) { return client, nil }
	result, err := managed.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	client.closeErr = nil
	if err := managed.Cleanup(result); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
}

func TestManagedCreationCleansUpAfterCancellation(t *testing.T) {
	built, err := New(map[string]any{"operation": "volume_create", "name": "transient"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	managed := built.(*managedRunner)
	client := &operationClient{
		fakeClient: &fakeClient{},
		volume:     client.VolumeCreateResult{Volume: volume.Volume{Name: "transient"}},
	}
	managed.newClient = func() (dockerClient, error) { return client, nil }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := managed.Run(ctx, step.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if client.removedVolume != "transient" {
		t.Fatalf("removed volume = %q", client.removedVolume)
	}
}

func TestVolumeCreationAndCleanupRejectChangedOwnership(t *testing.T) {
	built, err := New(map[string]any{"operation": "volume_create", "name": "shared"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	managed := built.(*managedRunner)
	client := &operationClient{
		fakeClient: &fakeClient{},
		volume: client.VolumeCreateResult{Volume: volume.Volume{
			Name:   "shared",
			Labels: map[string]string{ownershipLabel: "foreign"},
		}},
	}
	managed.newClient = func() (dockerClient, error) { return client, nil }

	if _, err := managed.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "ownership label") {
		t.Fatalf("Run() error = %v", err)
	}
	if client.removedVolume != "" {
		t.Fatalf("foreign volume %q was removed", client.removedVolume)
	}

	client.volumeExists = false
	client.volume.Volume.Labels = nil
	result, err := managed.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	client.volume.Volume.Labels[ownershipLabel] = "replacement"
	if err := managed.Cleanup(result); err == nil || !strings.Contains(err.Error(), "ownership label changed") {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if client.removedVolume != "" {
		t.Fatalf("replacement volume %q was removed", client.removedVolume)
	}
}

func TestVolumeMountDoesNotResolveSourceAsHostPath(t *testing.T) {
	runner := &Runner{config: Config{Image: "alpine", Mounts: []Mount{{Type: "volume", Source: "workflow-data", Target: "/data"}}}}
	_, host, err := runner.containerConfig(step.Request{RunDir: t.TempDir()}, "host", false)
	if err != nil {
		t.Fatalf("containerConfig() error = %v", err)
	}
	if host.Mounts[0].Type != "volume" || host.Mounts[0].Source != "workflow-data" {
		t.Fatalf("mount = %#v", host.Mounts[0])
	}
}

func TestBuildxBuildArgumentsAndMetadata(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buildArgs []string
	calls := 0
	runCommand := func(_ context.Context, name string, args []string, _, _ io.Writer) error {
		calls++
		if name != "docker" {
			t.Fatalf("command = %q", name)
		}
		if calls == 1 {
			if !slices.Equal(args, []string{"buildx", "version"}) {
				t.Fatalf("version args = %#v", args)
			}
			return nil
		}
		buildArgs = slices.Clone(args)
		for i, arg := range args {
			if arg == "--metadata-file" {
				return os.WriteFile(args[i+1], []byte(`{"containerimage.digest":"sha256:`+strings.Repeat("c", 64)+`"}`), 0o600)
			}
		}
		return errors.New("metadata argument missing")
	}
	runner := &Runner{
		config: Config{
			Operation: operationBuild, Tags: []string{"example/app:1"}, Platforms: []string{"linux/amd64"},
			Output: "push", Target: "production", Pull: true, NoCache: true,
			BuildArgs: map[string]string{"VERSION": "1"}, CacheFrom: []string{"type=registry,ref=cache"},
			CacheTo: []string{"type=registry,ref=cache,mode=max"},
		},
		runCommand: runCommand,
	}
	result, err := runner.Run(t.Context(), step.Request{RunDir: runDir})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Outputs["digest"] != "sha256:"+strings.Repeat("c", 64) {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	for _, pair := range [][]string{
		{"--push"}, {"--tag", "example/app:1"}, {"--platform", "linux/amd64"}, {"--target", "production"},
		{"--pull"}, {"--no-cache"}, {"--build-arg", "VERSION=1"}, {"--cache-from", "type=registry,ref=cache"},
		{"--cache-to", "type=registry,ref=cache,mode=max"},
	} {
		if !containsArgs(buildArgs, pair) {
			t.Fatalf("build args %#v do not contain %#v", buildArgs, pair)
		}
	}
	if buildArgs[len(buildArgs)-1] != runDir {
		t.Fatalf("build context = %q, want %q", buildArgs[len(buildArgs)-1], runDir)
	}
}

func TestBuildxAvailabilityAndCancellation(t *testing.T) {
	runner := &Runner{
		config: Config{Operation: operationBuild, Tags: []string{"app:test"}, Output: "load"},
		runCommand: func(context.Context, string, []string, io.Writer, io.Writer) error {
			return errors.New("unknown command: buildx")
		},
	}
	if _, err := runner.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "Buildx is unavailable") {
		t.Fatalf("availability error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	runner.runCommand = func(ctx context.Context, _ string, _ []string, _, _ io.Writer) error { return ctx.Err() }
	if _, err := runner.Run(ctx, step.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

type operationClient struct {
	*fakeClient
	inspect          client.ImageInspectResult
	pull             *fakePullResponse
	push             *fakePullResponse
	pullOptions      client.ImagePullOptions
	pushOptions      client.ImagePushOptions
	tagOptions       client.ImageTagOptions
	loginOptions     client.RegistryLoginOptions
	login            client.RegistryLoginResult
	networkName      string
	networkOptions   client.NetworkCreateOptions
	network          client.NetworkCreateResult
	networkExists    bool
	removedNetwork   string
	networkRemoveErr error
	volumeOptions    client.VolumeCreateOptions
	volume           client.VolumeCreateResult
	volumeExists     bool
	removedVolume    string
	volumeRemoveErr  error
}

func (f *operationClient) ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	return f.inspect, nil
}

func (f *operationClient) ImagePull(_ context.Context, _ string, options client.ImagePullOptions) (client.ImagePullResponse, error) {
	f.pullOptions = options
	return f.pull, nil
}

func (f *operationClient) ImagePush(_ context.Context, _ string, options client.ImagePushOptions) (client.ImagePushResponse, error) {
	f.pushOptions = options
	return f.push, nil
}

func (f *operationClient) ImageTag(_ context.Context, options client.ImageTagOptions) (client.ImageTagResult, error) {
	f.tagOptions = options
	return client.ImageTagResult{}, nil
}

func (f *operationClient) RegistryLogin(_ context.Context, options client.RegistryLoginOptions) (client.RegistryLoginResult, error) {
	f.loginOptions = options
	return f.login, nil
}

func (f *operationClient) NetworkInspect(context.Context, string, client.NetworkInspectOptions) (client.NetworkInspectResult, error) {
	if f.networkExists {
		return client.NetworkInspectResult{}, nil
	}
	return client.NetworkInspectResult{}, errdefs.ErrNotFound
}

func (f *operationClient) NetworkCreate(_ context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
	f.networkName, f.networkOptions = name, options
	return f.network, nil
}

func (f *operationClient) NetworkRemove(_ context.Context, name string, _ client.NetworkRemoveOptions) (client.NetworkRemoveResult, error) {
	f.removedNetwork = name
	return client.NetworkRemoveResult{}, f.networkRemoveErr
}

func (f *operationClient) VolumeInspect(context.Context, string, client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	if f.volumeExists {
		return client.VolumeInspectResult{Volume: f.volume.Volume}, nil
	}
	return client.VolumeInspectResult{}, errdefs.ErrNotFound
}

func (f *operationClient) VolumeCreate(_ context.Context, options client.VolumeCreateOptions) (client.VolumeCreateResult, error) {
	f.volumeOptions = options
	if f.volume.Volume.Labels == nil {
		f.volume.Volume.Labels = maps.Clone(options.Labels)
	}
	f.volumeExists = true
	return f.volume, nil
}

func (f *operationClient) VolumeRemove(_ context.Context, name string, _ client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	f.removedVolume = name
	return client.VolumeRemoveResult{}, f.volumeRemoveErr
}

func (f *operationClient) resourceLabels() map[string]string {
	if f.networkOptions.Labels != nil {
		return f.networkOptions.Labels
	}
	return f.volumeOptions.Labels
}

func imageInspection() client.ImageInspectResult {
	result := client.ImageInspectResult{}
	result.ID = "sha256:" + strings.Repeat("1", 64)
	result.RepoTags = []string{"example/app:1"}
	result.RepoDigests = []string{"example/app@sha256:" + strings.Repeat("d", 64)}
	result.Created = "2026-08-22T10:00:00Z"
	result.Size = 42
	result.Os = "linux"
	result.Architecture = "amd64"
	result.Config = &v1.DockerOCIImageConfig{}
	result.Config.Labels = map[string]string{"org.example": "test"}
	result.Descriptor = &ocispec.Descriptor{Digest: digest.Digest("sha256:" + strings.Repeat("e", 64))}
	return result
}

func clientpkgNetworkResult(id string) client.NetworkCreateResult {
	return client.NetworkCreateResult{ID: id}
}

func containsArgs(args, sequence []string) bool {
	if len(sequence) > len(args) {
		return false
	}
	for start := 0; start <= len(args)-len(sequence); start++ {
		if slices.Equal(args[start:start+len(sequence)], sequence) {
			return true
		}
	}
	return false
}
