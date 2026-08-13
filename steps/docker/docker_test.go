package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"iter"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
	"github.com/muesli/cancelreader"
	"github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/up2jj/wuko/step"
)

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr string
	}{
		{name: "missing image", config: Config{}, wantErr: "image is required"},
		{name: "relative working directory", config: Config{Image: "alpine", WorkingDirectory: "work"}, wantErr: "working_directory must be an absolute container path"},
		{name: "relative mount target", config: Config{Image: "alpine", Mounts: []Mount{{Source: ".", Target: "work"}}}, wantErr: "target must be an absolute container path"},
		{name: "arguments without command", config: Config{Image: "alpine", Args: []string{"echo"}}, wantErr: "args require command"},
		{name: "invalid pull policy", config: Config{Image: "alpine", Pull: "sometimes"}, wantErr: "pull must be one of never, if-missing, or always"},
		{name: "invalid platform", config: Config{Image: "alpine", Platform: "linux"}, wantErr: "platform must have the form os/architecture[/variant]"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateConfig(test.config)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateConfig() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestRunCapturesOutputAndBuildsContainerConfig(t *testing.T) {
	runDir := t.TempDir()
	client := &fakeClient{output: multiplexedOutput("out\n", "err\n"), exitCode: 0}
	var liveStdout, liveStderr bytes.Buffer
	runner := &Runner{
		config: Config{
			Image:            "alpine:3.22",
			Command:          "sh",
			Args:             []string{"-c", "echo out; echo err >&2"},
			WorkingDirectory: "/workspace",
			Mounts:           []Mount{{Source: "src", Target: "/workspace", ReadOnly: true}},
			Env:              map[string]string{"STEP": "docker"},
		},
		newClient: func() (dockerClient, error) { return client, nil },
	}

	result, err := runner.Run(t.Context(), step.Request{
		StepID: "tests",
		RunDir: runDir,
		Env:    map[string]string{"BASE": "workflow"},
		Stdout: &liveStdout,
		Stderr: &liveStderr,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := result.Outputs["stdout"]; got != "out\n" {
		t.Fatalf("stdout = %#v, want %q", got, "out\n")
	}
	if got := result.Outputs["stderr"]; got != "err\n" {
		t.Fatalf("stderr = %#v, want %q", got, "err\n")
	}
	if got := result.Outputs["exit_code"]; got != 0 {
		t.Fatalf("exit_code = %#v, want 0", got)
	}
	if liveStdout.String() != "out\n" || liveStderr.String() != "err\n" {
		t.Fatalf("live output = %q/%q, want %q/%q", liveStdout.String(), liveStderr.String(), "out\n", "err\n")
	}
	if !client.started || !client.removed || !client.closed {
		t.Fatalf("container lifecycle = started:%v removed:%v client-closed:%v", client.started, client.removed, client.closed)
	}
	if !client.attachedBeforeStart {
		t.Fatal("container output was not attached before start")
	}
	if !client.attachOptions.Stream || client.attachOptions.Logs || !client.attachOptions.Stdout || !client.attachOptions.Stderr {
		t.Fatalf("attach options = %#v, want live stdout/stderr stream without log replay", client.attachOptions)
	}
	if !client.removedOptions.Force || !client.removedOptions.RemoveVolumes {
		t.Fatalf("cleanup options = %#v, want force and volume removal", client.removedOptions)
	}
	if got := client.created.Config.Labels[managedLabel]; got != "true" {
		t.Fatalf("managed label = %q, want true", got)
	}
	if got := client.created.Config.Labels[ownerPIDLabel]; got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("owner pid label = %q, want current pid", got)
	}
	ownerHost, err := clientHostIdentity()
	if err != nil {
		t.Fatalf("clientHostIdentity() error = %v", err)
	}
	if got := client.created.Config.Labels[ownerHostLabel]; got != ownerHost {
		t.Fatalf("owner host label = %q, want %q", got, ownerHost)
	}

	created := client.created.Config
	if created.Image != "alpine:3.22" || created.WorkingDir != "/workspace" || created.User != "" {
		t.Fatalf("container config = %#v", created)
	}
	if got, want := created.Cmd, []string{"sh", "-c", "echo out; echo err >&2"}; !equalStrings(got, want) {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
	if got := client.created.HostConfig.Mounts[0].Source; got != filepath.Join(runDir, "src") {
		t.Fatalf("mount source = %q, want %q", got, filepath.Join(runDir, "src"))
	}
	if got := client.created.HostConfig.Mounts[0].Target; got != "/workspace" {
		t.Fatalf("mount target = %q, want /workspace", got)
	}
}

func TestRunReturnsContainerExitCode(t *testing.T) {
	client := &fakeClient{output: multiplexedOutput("", "failed\n"), exitCode: 17}
	runner := &Runner{
		config:    Config{Image: "alpine", Command: "false"},
		newClient: func() (dockerClient, error) { return client, nil },
	}

	result, err := runner.Run(t.Context(), step.Request{StepID: "failed"})
	if err == nil || !strings.Contains(err.Error(), "exited with status 17") {
		t.Fatalf("Run() error = %v, want exit status error", err)
	}
	if got := result.Outputs["exit_code"]; got != 17 {
		t.Fatalf("exit_code = %#v, want 17", got)
	}
	if client.waitCondition != container.WaitConditionNextExit {
		t.Fatalf("wait condition = %q, want %q", client.waitCondition, container.WaitConditionNextExit)
	}
}

func TestRunStopsAndRemovesOnCancellation(t *testing.T) {
	client := &fakeClient{output: multiplexedOutput("", ""), waitBlocks: true}
	runner := &Runner{
		config:    Config{Image: "alpine", Command: "sleep"},
		newClient: func() (dockerClient, error) { return client, nil },
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := runner.Run(ctx, step.Request{StepID: "cancelled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if !client.removed {
		t.Fatal("container was not removed after cancellation")
	}
}

func TestRunJoinsCleanupErrorWithStepError(t *testing.T) {
	cleanupErr := errors.New("remove failed")
	client := &fakeClient{output: multiplexedOutput("", ""), exitCode: 7, removeErr: cleanupErr}
	runner := &Runner{
		config:    Config{Image: "alpine", Command: "false"},
		newClient: func() (dockerClient, error) { return client, nil },
	}

	_, err := runner.Run(t.Context(), step.Request{StepID: "failed"})
	if !errors.Is(err, cleanupErr) || !strings.Contains(err.Error(), "exited with status 7") {
		t.Fatalf("Run() error = %v, want step and cleanup errors", err)
	}
}

func TestWaitForOutputStopsOnCancellation(t *testing.T) {
	reader, writer := net.Pipe()
	defer writer.Close()
	attached := client.ContainerAttachResult{HijackedResponse: client.HijackedResponse{
		Conn: reader, Reader: bufio.NewReader(reader),
	}}
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, attached.Reader)
		done <- err
	}()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitForOutput(ctx, &attached, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForOutput() error = %v, want context.Canceled", err)
	}
}

func TestRecoverOrphansOnlyRemovesDeadOwners(t *testing.T) {
	const ownerHost = "client-a"
	client := &fakeClient{containers: []container.Summary{
		{ID: "live", Labels: map[string]string{managedLabel: "true", ownerHostLabel: ownerHost, ownerPIDLabel: strconv.Itoa(os.Getpid())}},
		{ID: "orphan", Labels: map[string]string{managedLabel: "true", ownerHostLabel: ownerHost, ownerPIDLabel: "999999999"}},
		{ID: "foreign-host", Labels: map[string]string{managedLabel: "true", ownerHostLabel: "client-b", ownerPIDLabel: "999999999"}},
		{ID: "legacy-unscoped", Labels: map[string]string{managedLabel: "true", ownerPIDLabel: "999999999"}},
		{ID: "unlabeled-owner", Labels: map[string]string{managedLabel: "true", ownerHostLabel: ownerHost}},
	}}

	if err := recoverOrphans(t.Context(), client, ownerHost); err != nil {
		t.Fatalf("recoverOrphans() error = %v", err)
	}
	if len(client.removedIDs) != 1 || client.removedIDs[0] != "orphan" {
		t.Fatalf("removed IDs = %#v, want [orphan]", client.removedIDs)
	}
	if !client.removedOptions.RemoveVolumes || !client.removedOptions.Force {
		t.Fatalf("recovery options = %#v, want force and volume removal", client.removedOptions)
	}
}

func TestInputSelectionAndCancellation(t *testing.T) {
	t.Run("non-interactive stdin is not forwarded", func(t *testing.T) {
		runner := &Runner{config: Config{Image: "alpine"}}
		input, err := runner.input(step.Request{Stdin: strings.NewReader("shared input")})
		if err != nil {
			t.Fatalf("input() error = %v", err)
		}
		if input != nil {
			t.Fatal("input() forwarded shared stdin for a non-interactive step")
		}
	})

	t.Run("explicit empty stdin is attached", func(t *testing.T) {
		empty := ""
		runner := &Runner{config: Config{Image: "alpine", Stdin: &empty}}
		input, err := runner.input(step.Request{})
		if err != nil {
			t.Fatalf("input() error = %v", err)
		}
		if input == nil {
			t.Fatal("input() did not attach explicitly configured stdin")
		}
		config, _, err := runner.containerConfig(step.Request{}, "client", true)
		if err != nil {
			t.Fatalf("containerConfig() error = %v", err)
		}
		if !config.StdinOnce {
			t.Fatal("finite non-TTY stdin did not enable StdinOnce")
		}
	})

	t.Run("interactive terminal read is cancelable", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe() error = %v", err)
		}
		defer reader.Close()
		defer writer.Close()

		runner := &Runner{config: Config{Image: "alpine", TTY: true}}
		input, err := runner.input(step.Request{Stdin: reader, Interactive: true})
		if err != nil {
			t.Fatalf("input() error = %v", err)
		}
		readDone := make(chan error, 1)
		go func() {
			var value [1]byte
			_, err := input.reader.Read(value[:])
			readDone <- err
		}()
		if !input.cancel() {
			t.Fatal("input cancellation was not supported")
		}
		if err := <-readDone; !errors.Is(err, cancelreader.ErrCanceled) {
			t.Fatalf("input read error = %v, want cancelreader.ErrCanceled", err)
		}
		if err := input.close(); err != nil {
			t.Fatalf("closing input: %v", err)
		}
		config, _, err := runner.containerConfig(step.Request{}, "client", true)
		if err != nil {
			t.Fatalf("containerConfig() error = %v", err)
		}
		if config.StdinOnce {
			t.Fatal("interactive TTY stdin unexpectedly enabled StdinOnce")
		}
	})
}

func TestEnsureImageInspectsRequestedPlatform(t *testing.T) {
	pull := &fakePullResponse{}
	client := &fakeClient{imageInspectErr: errdefs.ErrNotFound, pullResponse: pull}
	platform := &v1.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}

	if err := ensureImage(t.Context(), client, "example/image:tag", "if-missing", platform); err != nil {
		t.Fatalf("ensureImage() error = %v", err)
	}
	if client.imageInspectOptionCount != 1 {
		t.Fatalf("image inspect option count = %d, want 1", client.imageInspectOptionCount)
	}
	platformMatches := len(client.pullOptions.Platforms) == 1
	if platformMatches {
		pulledPlatform := client.pullOptions.Platforms[0]
		platformMatches = pulledPlatform.OS == platform.OS && pulledPlatform.Architecture == platform.Architecture && pulledPlatform.Variant == platform.Variant
	}
	if !client.pullCalled || !platformMatches {
		t.Fatalf("pull options = %#v, want platform %#v", client.pullOptions, *platform)
	}
	if !pull.waited || !pull.closed {
		t.Fatalf("pull lifecycle = waited:%v closed:%v, want both true", pull.waited, pull.closed)
	}
}

type fakeClient struct {
	created                 client.ContainerCreateOptions
	output                  []byte
	exitCode                int64
	waitBlocks              bool
	containers              []container.Summary
	removeErr               error
	imageInspectErr         error
	imageInspectOptionCount int
	pullResponse            *fakePullResponse
	pullCalled              bool
	pullOptions             client.ImagePullOptions
	waitCondition           container.WaitCondition
	attachOptions           client.ContainerAttachOptions
	attached                bool
	attachedBeforeStart     bool
	started                 bool
	stopped                 bool
	removed                 bool
	closed                  bool
	removedIDs              []string
	removedOptions          client.ContainerRemoveOptions
}

func (f *fakeClient) ImageInspect(_ context.Context, _ string, options ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	f.imageInspectOptionCount = len(options)
	return client.ImageInspectResult{}, f.imageInspectErr
}

func (f *fakeClient) ImagePull(_ context.Context, _ string, options client.ImagePullOptions) (client.ImagePullResponse, error) {
	if f.pullResponse == nil {
		return nil, errors.New("unexpected image pull")
	}
	f.pullCalled = true
	f.pullOptions = options
	return f.pullResponse, nil
}

func (f *fakeClient) ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error) {
	return client.ContainerListResult{Items: f.containers}, nil
}

func (f *fakeClient) ContainerCreate(_ context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	f.created = options
	return client.ContainerCreateResult{ID: "container-id"}, nil
}

func (f *fakeClient) ContainerAttach(_ context.Context, _ string, options client.ContainerAttachOptions) (client.ContainerAttachResult, error) {
	f.attachOptions = options
	f.attached = true
	f.attachedBeforeStart = !f.started
	connA, connB := net.Pipe()
	_ = connB.Close()
	return client.ContainerAttachResult{HijackedResponse: client.HijackedResponse{
		Conn:   connA,
		Reader: bufio.NewReader(bytes.NewReader(f.output)),
	}}, nil
}

func (f *fakeClient) ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error) {
	if !f.attached {
		return client.ContainerStartResult{}, errors.New("container must be attached before start")
	}
	f.started = true
	return client.ContainerStartResult{}, nil
}

func (f *fakeClient) ContainerWait(ctx context.Context, _ string, options client.ContainerWaitOptions) client.ContainerWaitResult {
	result := make(chan container.WaitResponse, 1)
	errors := make(chan error, 1)
	f.waitCondition = options.Condition
	if f.waitBlocks {
		go func() {
			<-ctx.Done()
			errors <- ctx.Err()
		}()
	} else if options.Condition == container.WaitConditionNotRunning && !f.started {
		result <- container.WaitResponse{StatusCode: 0}
	} else {
		result <- container.WaitResponse{StatusCode: f.exitCode}
	}
	return client.ContainerWaitResult{Result: result, Error: errors}
}

type fakePullResponse struct {
	waited bool
	closed bool
}

func (f *fakePullResponse) Read([]byte) (int, error) { return 0, io.EOF }

func (f *fakePullResponse) Close() error {
	f.closed = true
	return nil
}

func (f *fakePullResponse) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(func(jsonstream.Message, error) bool) {}
}

func (f *fakePullResponse) Wait(context.Context) error {
	f.waited = true
	return nil
}

func (f *fakeClient) ContainerStop(context.Context, string, client.ContainerStopOptions) (client.ContainerStopResult, error) {
	f.stopped = true
	return client.ContainerStopResult{}, nil
}

func (f *fakeClient) ContainerRemove(_ context.Context, id string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	return f.removeContainer(id, options)
}

func (f *fakeClient) removeContainer(id string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	f.removed = true
	f.removedIDs = append(f.removedIDs, id)
	f.removedOptions = options
	return client.ContainerRemoveResult{}, f.removeErr
}

func (f *fakeClient) Close() error {
	f.closed = true
	return nil
}

func multiplexedOutput(stdout, stderr string) []byte {
	var output bytes.Buffer
	writeFrame := func(stream byte, value string) {
		header := make([]byte, 8)
		header[0] = stream
		binary.BigEndian.PutUint32(header[4:], uint32(len(value)))
		_, _ = output.Write(header)
		_, _ = output.WriteString(value)
	}
	if stdout != "" {
		writeFrame(1, stdout)
	}
	if stderr != "" {
		writeFrame(2, stderr)
	}
	return output.Bytes()
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
