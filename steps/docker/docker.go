package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/muesli/cancelreader"
	"github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

const (
	defaultPullPolicy  = "if-missing"
	cleanupTimeout     = 5 * time.Second
	outputDrainTimeout = 5 * time.Second
	managedLabel       = "com.up2jj.wuko.managed"
	ownerHostLabel     = "com.up2jj.wuko.owner-host"
	ownerPIDLabel      = "com.up2jj.wuko.owner-pid"
	workflowLabel      = "com.up2jj.wuko.workflow"
	stepLabel          = "com.up2jj.wuko.step"
	ownershipLabel     = "com.up2jj.wuko.ownership"
)

// Config describes one Docker operation.
type Config struct {
	Operation        string               `yaml:"operation,omitempty"`
	Image            string               `yaml:"image"`
	Command          string               `yaml:"command,omitempty"`
	Args             []string             `yaml:"args,omitempty"`
	WorkingDirectory string               `yaml:"working_directory,omitempty"`
	Mounts           []Mount              `yaml:"mounts,omitempty"`
	Env              workflow.Environment `yaml:"env,omitempty"`
	Network          string               `yaml:"network,omitempty"`
	User             string               `yaml:"user,omitempty"`
	Platform         string               `yaml:"platform,omitempty"`
	Pull             any                  `yaml:"pull,omitempty"`
	TTY              bool                 `yaml:"tty,omitempty"`
	Stdin            *string              `yaml:"stdin,omitempty"`
	Auth             *Auth                `yaml:"auth,omitempty"`
	Source           string               `yaml:"source,omitempty"`
	Target           string               `yaml:"target,omitempty"`
	ExpectedDigest   string               `yaml:"expected_digest,omitempty"`
	Container        string               `yaml:"container,omitempty"`
	Name             string               `yaml:"name,omitempty"`
	Driver           string               `yaml:"driver,omitempty"`
	Internal         bool                 `yaml:"internal,omitempty"`
	Attachable       bool                 `yaml:"attachable,omitempty"`
	Options          map[string]string    `yaml:"options,omitempty"`
	Labels           map[string]string    `yaml:"labels,omitempty"`
	DriverOptions    map[string]string    `yaml:"driver_options,omitempty"`
	Context          string               `yaml:"context,omitempty"`
	Dockerfile       string               `yaml:"dockerfile,omitempty"`
	Tags             []string             `yaml:"tags,omitempty"`
	Platforms        []string             `yaml:"platforms,omitempty"`
	Output           string               `yaml:"output,omitempty"`
	BuildArgs        map[string]string    `yaml:"build_args,omitempty"`
	NoCache          bool                 `yaml:"no_cache,omitempty"`
	CacheFrom        []string             `yaml:"cache_from,omitempty"`
	CacheTo          []string             `yaml:"cache_to,omitempty"`
	Cleanup          *bool                `yaml:"cleanup,omitempty"`
}

// Auth contains inline credentials for one registry operation.
type Auth struct {
	Username      string `yaml:"username,omitempty"`
	Password      string `yaml:"password,omitempty"`
	ServerAddress string `yaml:"server_address,omitempty"`
	IdentityToken string `yaml:"identity_token,omitempty"`
	RegistryToken string `yaml:"registry_token,omitempty"`
}

// Mount describes a bind or named-volume mount into the container.
type Mount struct {
	Type     string `yaml:"type,omitempty"`
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only,omitempty"`
}

type Runner struct {
	config     Config
	present    map[string]bool
	newClient  func() (dockerClient, error)
	runCommand commandRunner
	waitHealth func(context.Context, time.Duration) error
}

type runInput struct {
	reader io.Reader
	cancel func() bool
	close  func() error
}

type dockerClient interface {
	ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error)
	ImagePull(context.Context, string, client.ImagePullOptions) (client.ImagePullResponse, error)
	ImagePush(context.Context, string, client.ImagePushOptions) (client.ImagePushResponse, error)
	ImageTag(context.Context, client.ImageTagOptions) (client.ImageTagResult, error)
	RegistryLogin(context.Context, client.RegistryLoginOptions) (client.RegistryLoginResult, error)
	NetworkInspect(context.Context, string, client.NetworkInspectOptions) (client.NetworkInspectResult, error)
	NetworkCreate(context.Context, string, client.NetworkCreateOptions) (client.NetworkCreateResult, error)
	NetworkRemove(context.Context, string, client.NetworkRemoveOptions) (client.NetworkRemoveResult, error)
	VolumeInspect(context.Context, string, client.VolumeInspectOptions) (client.VolumeInspectResult, error)
	VolumeCreate(context.Context, client.VolumeCreateOptions) (client.VolumeCreateResult, error)
	VolumeRemove(context.Context, string, client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
	ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerCreate(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerAttach(context.Context, string, client.ContainerAttachOptions) (client.ContainerAttachResult, error)
	ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerWait(context.Context, string, client.ContainerWaitOptions) client.ContainerWaitResult
	ContainerStop(context.Context, string, client.ContainerStopOptions) (client.ContainerStopResult, error)
	ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ExecCreate(context.Context, string, client.ExecCreateOptions) (client.ExecCreateResult, error)
	ExecAttach(context.Context, string, client.ExecAttachOptions) (client.ExecAttachResult, error)
	ExecInspect(context.Context, string, client.ExecInspectOptions) (client.ExecInspectResult, error)
	Close() error
}

func Register(registry *step.Registry) error { return registry.Register("docker", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(raw))
	for field := range raw {
		present[field] = true
	}
	runner := &Runner{
		config:  config,
		present: present,
		newClient: func() (dockerClient, error) {
			return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		},
		runCommand: defaultCommandRunner,
		waitHealth: waitForHealthPoll,
	}
	if err := validateConfig(config, present); err != nil {
		return nil, err
	}
	if managesResource(config) {
		return &managedRunner{Runner: runner}, nil
	}
	return runner, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (result step.Result, runErr error) {
	if err := validateConfig(r.config, r.present); err != nil {
		return step.Result{}, err
	}
	if operation(r.config) == operationBuild {
		return r.runBuild(ctx, request)
	}
	if operation(r.config) != operationRun {
		return r.runEngineOperation(ctx, request)
	}
	input, err := r.input(request)
	if err != nil {
		return step.Result{}, err
	}
	if input != nil && input.close != nil {
		defer func() {
			if closeErr := input.close(); closeErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("closing Docker input: %w", closeErr))
			}
		}()
	}
	ownerHost, err := clientHostIdentity()
	if err != nil {
		return step.Result{}, err
	}

	dockerClient, err := r.newClient()
	if err != nil {
		return step.Result{}, fmt.Errorf("connecting to Docker: %w", err)
	}
	defer func() {
		if closeErr := dockerClient.Close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("closing Docker client: %w", closeErr))
		}
	}()
	if err := recoverOrphans(ctx, dockerClient, ownerHost); err != nil {
		return step.Result{}, err
	}

	platform, err := parsePlatform(r.config.Platform)
	if err != nil {
		return step.Result{}, err
	}
	pullPolicy, _ := r.config.Pull.(string)
	if err := ensureImage(ctx, dockerClient, r.config.Image, pullPolicy, platform); err != nil {
		return step.Result{}, err
	}

	containerConfig, hostConfig, err := r.containerConfig(request, ownerHost, input != nil)
	if err != nil {
		return step.Result{}, err
	}
	created, err := dockerClient.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     containerConfig,
		HostConfig: hostConfig,
		Platform:   platform,
	})
	if err != nil {
		return step.Result{}, fmt.Errorf("creating Docker container for step %q: %w", request.StepID, err)
	}
	containerID := created.ID
	started := false
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		var cleanupErrs []error
		if started && ctx.Err() != nil {
			if _, stopErr := dockerClient.ContainerStop(cleanupCtx, containerID, client.ContainerStopOptions{Timeout: intPointer(2)}); stopErr != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("stopping container: %w", stopErr))
			}
		}
		if _, removeErr := dockerClient.ContainerRemove(cleanupCtx, containerID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); removeErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("removing container: %w", removeErr))
		}
		if len(cleanupErrs) > 0 {
			cleanupErr := fmt.Errorf("cleaning up Docker container %q: %w", containerID, errors.Join(cleanupErrs...))
			runErr = errors.Join(runErr, cleanupErr)
		}
	}()

	attached, err := dockerClient.ContainerAttach(ctx, containerID, client.ContainerAttachOptions{
		Stream: true, Stdin: containerConfig.AttachStdin, Stdout: true, Stderr: true,
	})
	if err != nil {
		return step.Result{}, fmt.Errorf("attaching to Docker container: %w", err)
	}
	defer attached.Close()

	wait := dockerClient.ContainerWait(ctx, containerID, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := dockerClient.ContainerStart(ctx, containerID, client.ContainerStartOptions{}); err != nil {
		return step.Result{}, fmt.Errorf("starting Docker container: %w", err)
	}
	started = true

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	stdoutWriter := io.MultiWriter(writerOrDiscard(request.Stdout), stdout)
	stderrWriter := io.MultiWriter(writerOrDiscard(request.Stderr), stderr)
	copyDone := make(chan error, 1)
	go func() {
		var copyErr error
		if r.config.TTY {
			_, copyErr = io.Copy(stdoutWriter, attached.Reader)
		} else {
			_, copyErr = stdcopy.StdCopy(stdoutWriter, stderrWriter, attached.Reader)
		}
		copyDone <- copyErr
	}()
	var inputDone chan error
	if input != nil {
		inputDone = make(chan error, 1)
		go func() {
			_, inputErr := io.Copy(attached.Conn, input.reader)
			if closeErr := attached.CloseWrite(); inputErr == nil {
				inputErr = closeErr
			}
			inputDone <- inputErr
		}()
	}

	status, waitErr := waitForContainer(ctx, wait)
	if input != nil && input.cancel != nil {
		input.cancel()
	}
	if waitErr != nil {
		attached.Close()
		<-copyDone
		if inputDone != nil {
			<-inputDone
		}
		return step.Result{Outputs: dockerOutputs(stdout, stderr, -1)}, waitErr
	}
	copyErr := waitForOutput(ctx, &attached, copyDone)
	var inputErr error
	if inputDone != nil {
		attached.Close()
		inputErr = <-inputDone
	}
	if !expectedStreamError(copyErr) {
		return step.Result{Outputs: dockerOutputs(stdout, stderr, status)}, fmt.Errorf("reading Docker output: %w", copyErr)
	}
	if !expectedStreamError(inputErr) {
		return step.Result{Outputs: dockerOutputs(stdout, stderr, status)}, fmt.Errorf("writing Docker input: %w", inputErr)
	}

	outputs := dockerOutputs(stdout, stderr, status)
	if status != 0 {
		return step.Result{Outputs: outputs}, fmt.Errorf("Docker container for step %q exited with status %d", request.StepID, status)
	}
	return step.Result{Outputs: outputs}, nil
}

func (r *Runner) input(request step.Request) (*runInput, error) {
	if r.config.Stdin != nil {
		return &runInput{reader: strings.NewReader(*r.config.Stdin)}, nil
	}
	if !r.config.TTY || !request.Interactive || request.Stdin == nil {
		return nil, nil
	}
	if _, ok := request.Stdin.(cancelreader.File); !ok {
		return nil, fmt.Errorf("interactive Docker stdin must be a file-backed terminal")
	}
	reader, err := cancelreader.NewReader(request.Stdin)
	if err != nil {
		return nil, fmt.Errorf("preparing interactive Docker stdin: %w", err)
	}
	return &runInput{reader: reader, cancel: reader.Cancel, close: reader.Close}, nil
}

func (r *Runner) containerConfig(request step.Request, ownerHost string, attachStdin bool) (*container.Config, *container.HostConfig, error) {
	environment := maps.Clone(request.Env)
	maps.Copy(environment, r.config.Env)
	environment = step.ApplyAttemptEnvironment(environment, request)
	envKeys := slices.Sorted(maps.Keys(environment))
	env := make([]string, 0, len(envKeys))
	for _, key := range envKeys {
		env = append(env, key+"="+environment[key])
	}

	containerMounts := make([]mount.Mount, 0, len(r.config.Mounts))
	for _, configured := range r.config.Mounts {
		mountType := mount.TypeBind
		source := configured.Source
		if configured.Type == "volume" {
			mountType = mount.TypeVolume
		} else {
			if !filepath.IsAbs(source) {
				source = filepath.Join(request.RunDir, source)
			}
			absoluteSource, err := filepath.Abs(source)
			if err != nil {
				return nil, nil, fmt.Errorf("resolving Docker mount source %q: %w", configured.Source, err)
			}
			source = absoluteSource
		}
		containerMounts = append(containerMounts, mount.Mount{
			Type:     mountType,
			Source:   source,
			Target:   configured.Target,
			ReadOnly: configured.ReadOnly,
		})
	}

	command := append([]string(nil), r.config.Args...)
	if r.config.Command != "" {
		command = append([]string{r.config.Command}, command...)
	}
	config := &container.Config{
		Image:        r.config.Image,
		Cmd:          command,
		WorkingDir:   r.config.WorkingDirectory,
		User:         r.config.User,
		Env:          env,
		Tty:          r.config.TTY,
		AttachStdin:  attachStdin,
		OpenStdin:    attachStdin,
		StdinOnce:    attachStdin && !r.config.TTY,
		AttachStdout: true,
		AttachStderr: true,
		Labels: map[string]string{
			managedLabel:   "true",
			ownerHostLabel: ownerHost,
			ownerPIDLabel:  strconv.Itoa(os.Getpid()),
			workflowLabel:  request.WorkflowName,
			stepLabel:      request.StepID,
		},
	}
	return config, &container.HostConfig{
		NetworkMode: container.NetworkMode(r.config.Network),
		Mounts:      containerMounts,
	}, nil
}

func normalizePullPolicy(policy string) string {
	if policy == "" {
		return defaultPullPolicy
	}
	if policy == "missing" {
		return "if-missing"
	}
	return policy
}

func ensureImage(ctx context.Context, dockerClient dockerClient, image, policy string, platform *v1.Platform) error {
	policy = normalizePullPolicy(policy)
	if policy == "never" {
		return nil
	}
	if policy == "if-missing" {
		var options []client.ImageInspectOption
		if platform != nil {
			options = append(options, client.ImageInspectWithPlatform(platform))
		}
		if _, err := dockerClient.ImageInspect(ctx, image, options...); err == nil {
			return nil
		} else if !errdefs.IsNotFound(err) {
			return fmt.Errorf("inspecting Docker image %q: %w", image, err)
		}
	}
	options := client.ImagePullOptions{}
	if platform != nil {
		options.Platforms = []v1.Platform{*platform}
	}
	pull, err := dockerClient.ImagePull(ctx, image, options)
	if err != nil {
		return fmt.Errorf("pulling Docker image %q: %w", image, err)
	}
	defer pull.Close()
	if err := pull.Wait(ctx); err != nil {
		return fmt.Errorf("pulling Docker image %q: %w", image, err)
	}
	return nil
}

func recoverOrphans(ctx context.Context, dockerClient dockerClient, ownerHost string) error {
	containers, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", managedLabel+"=true"),
	})
	if err != nil {
		return fmt.Errorf("listing Wuko Docker containers: %w", err)
	}

	var cleanupErrs []error
	for _, candidate := range containers.Items {
		if candidate.Labels[ownerHostLabel] != ownerHost {
			continue
		}
		owner, ok := candidate.Labels[ownerPIDLabel]
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(owner)
		if err != nil || pid <= 0 || processAlive(pid) {
			continue
		}
		if _, err := dockerClient.ContainerRemove(ctx, candidate.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("removing orphan container %s: %w", candidate.ID, err))
		}
	}
	if len(cleanupErrs) > 0 {
		return fmt.Errorf("recovering orphaned Wuko containers: %w", errors.Join(cleanupErrs...))
	}
	return nil
}

func clientHostIdentity() (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("identifying Docker client host: %w", err)
	}
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return "", fmt.Errorf("identifying Docker client host: hostname is empty")
	}
	return hostname, nil
}

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func parsePlatform(value string) (*v1.Platform, error) {
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("platform must have the form os/architecture[/variant]")
	}
	platform := &v1.Platform{OS: parts[0], Architecture: parts[1]}
	if len(parts) == 3 {
		if parts[2] == "" {
			return nil, fmt.Errorf("platform variant cannot be empty")
		}
		platform.Variant = parts[2]
	}
	return platform, nil
}

func waitForContainer(ctx context.Context, wait client.ContainerWaitResult) (int, error) {
	select {
	case err := <-wait.Error:
		if err != nil {
			return -1, fmt.Errorf("waiting for Docker container: %w", err)
		}
		return -1, fmt.Errorf("waiting for Docker container: wait ended without a result")
	case <-ctx.Done():
		return -1, ctx.Err()
	case status := <-wait.Result:
		return int(status.StatusCode), nil
	}
}

func waitForOutput(ctx context.Context, attached *client.ContainerAttachResult, done <-chan error) error {
	timer := time.NewTimer(outputDrainTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		attached.Close()
		<-done
		return ctx.Err()
	case <-timer.C:
		attached.Close()
		<-done
		return fmt.Errorf("timed out draining Docker output after %s", outputDrainTimeout)
	}
}

func dockerOutputs(stdout, stderr *bytes.Buffer, exitCode int) map[string]any {
	return map[string]any{"stdout": stdout.String(), "stderr": stderr.String(), "exit_code": exitCode}
}

func writerOrDiscard(writer io.Writer) io.Writer {
	if writer == nil {
		return io.Discard
	}
	return writer
}

func expectedStreamError(err error) bool {
	return err == nil || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, cancelreader.ErrCanceled)
}

func intPointer(value int) *int { return &value }
