package docker

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/step"
)

const healthPollInterval = time.Second

type managedRunner struct{ *Runner }

func (r *Runner) runEngineOperation(ctx context.Context, request step.Request) (result step.Result, runErr error) {
	dockerClient, err := r.newClient()
	if err != nil {
		return step.Result{}, fmt.Errorf("connecting to Docker: %w", err)
	}
	defer func() {
		if closeErr := dockerClient.Close(); closeErr != nil {
			if runErr == nil && managesResource(r.config) && result.Outputs != nil && result.Outputs["resource_type"] != nil {
				// Preserve successful managed-resource results so the engine registers cleanup.
				return
			}
			runErr = errors.Join(runErr, fmt.Errorf("closing Docker client: %w", closeErr))
		}
	}()

	switch operation(r.config) {
	case operationPull:
		return r.pullImage(ctx, request, dockerClient)
	case operationPush:
		return r.pushImage(ctx, request, dockerClient)
	case operationTag:
		return r.tagImage(ctx, dockerClient)
	case operationInspect:
		return r.inspectImage(ctx, dockerClient)
	case operationHealthWait:
		return r.waitForHealthyContainer(ctx, dockerClient)
	case operationCopyTo:
		return r.copyToContainer(ctx, request, dockerClient)
	case operationCopyFrom:
		return r.copyFromContainer(ctx, request, dockerClient)
	case operationLogin:
		return r.login(ctx, dockerClient)
	case operationNetworkCreate:
		return r.createNetwork(ctx, request, dockerClient)
	case operationVolumeCreate:
		return r.createVolume(ctx, request, dockerClient)
	case operationVerifyDigest:
		return r.verifyDigest(ctx, dockerClient)
	default:
		panic("validated Docker operation")
	}
}

type healthWaitError struct {
	message    string
	cause      error
	attributes []diagnostic.Attribute
}

func (err healthWaitError) Error() string { return err.message }

func (err healthWaitError) Unwrap() error { return err.cause }

func (err healthWaitError) DiagnosticAttributes() []diagnostic.Attribute {
	return slices.Clone(err.attributes)
}

func (r *Runner) waitForHealthyContainer(ctx context.Context, dockerClient dockerClient) (step.Result, error) {
	wait := r.waitHealth
	if wait == nil {
		wait = waitForHealthPoll
	}
	var last step.Result
	for {
		inspected, err := dockerClient.ContainerInspect(ctx, r.config.Container, client.ContainerInspectOptions{})
		if err != nil {
			if last.Outputs == nil {
				last = dockerHealthResult(r.config.Container, client.ContainerInspectResult{})
			}
			if ctx.Err() != nil {
				return last, healthFailure(last, fmt.Sprintf("waiting for Docker container %q health: %v", r.config.Container, ctx.Err()), ctx.Err())
			}
			return last, healthFailure(last, fmt.Sprintf("inspecting Docker container %q: %v", r.config.Container, err), err)
		}

		last = dockerHealthResult(r.config.Container, inspected)
		state := inspected.Container.State
		if state == nil {
			return last, healthFailure(last, fmt.Sprintf("Docker container %q has no state", r.config.Container), nil)
		}
		if !state.Running {
			return last, healthFailure(last, fmt.Sprintf("Docker container %q is not running (status %s, exit code %d)", r.config.Container, state.Status, state.ExitCode), nil)
		}
		if state.Health == nil || state.Health.Status == container.NoHealthcheck {
			return last, healthFailure(last, fmt.Sprintf("Docker container %q has no healthcheck", r.config.Container), nil)
		}
		switch state.Health.Status {
		case container.Healthy:
			return last, nil
		case container.Unhealthy:
			return last, healthFailure(last, fmt.Sprintf("Docker container %q is unhealthy (failing streak %d)", r.config.Container, state.Health.FailingStreak), nil)
		case container.Starting:
			if err := wait(ctx, healthPollInterval); err != nil {
				return last, healthFailure(last, fmt.Sprintf("waiting for Docker container %q health: %v", r.config.Container, err), err)
			}
		default:
			return last, healthFailure(last, fmt.Sprintf("Docker container %q reported unknown health status %q", r.config.Container, state.Health.Status), nil)
		}
	}
}

func waitForHealthPoll(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func dockerHealthResult(reference string, inspected client.ContainerInspectResult) step.Result {
	outputs := map[string]any{
		"container": reference, "id": inspected.Container.ID,
		"container_status": "", "health_status": string(container.NoHealthcheck),
		"failing_streak": 0, "health_checks": []map[string]any{},
	}
	state := inspected.Container.State
	if state == nil {
		return step.Result{Outputs: outputs}
	}
	outputs["container_status"] = string(state.Status)
	if state.Health == nil {
		return step.Result{Outputs: outputs}
	}
	outputs["health_status"] = string(state.Health.Status)
	outputs["failing_streak"] = state.Health.FailingStreak
	checks := make([]map[string]any, 0, len(state.Health.Log))
	for _, check := range state.Health.Log {
		if check == nil {
			continue
		}
		checks = append(checks, map[string]any{
			"started_at": check.Start.Format(time.RFC3339Nano), "finished_at": check.End.Format(time.RFC3339Nano),
			"exit_code": check.ExitCode, "output": check.Output,
		})
	}
	outputs["health_checks"] = checks
	return step.Result{Outputs: outputs}
}

func healthFailure(result step.Result, message string, cause error) error {
	outputs := result.Outputs
	return healthWaitError{message: message, cause: cause, attributes: []diagnostic.Attribute{
		diagnostic.Attr("container", fmt.Sprint(outputs["container"])),
		diagnostic.Attr("container_id", fmt.Sprint(outputs["id"])),
		diagnostic.Attr("container_status", fmt.Sprint(outputs["container_status"])),
		diagnostic.Attr("health_status", fmt.Sprint(outputs["health_status"])),
		diagnostic.Attr("failing_streak", fmt.Sprint(outputs["failing_streak"])),
		diagnostic.Attr("health_checks", diagnostic.RedactedJSON(outputs["health_checks"])),
	}}
}

func (r *Runner) pullImage(ctx context.Context, request step.Request, dockerClient dockerClient) (step.Result, error) {
	platform, err := parsePlatform(r.config.Platform)
	if err != nil {
		return step.Result{}, err
	}
	auth, err := encodedAuth(r.config.Auth)
	if err != nil {
		return step.Result{}, err
	}
	options := client.ImagePullOptions{RegistryAuth: auth}
	if platform != nil {
		options.Platforms = []v1.Platform{*platform}
	}
	response, err := dockerClient.ImagePull(ctx, r.config.Image, options)
	if err != nil {
		return step.Result{}, fmt.Errorf("pulling Docker image %q: %w", r.config.Image, err)
	}
	defer response.Close()
	if err := displayDockerMessages(response.JSONMessages(ctx), writerOrDiscard(request.Stdout), nil); err != nil {
		return step.Result{}, fmt.Errorf("pulling Docker image %q: %w", r.config.Image, err)
	}
	return inspectImage(ctx, dockerClient, r.config.Image, platform)
}

func (r *Runner) pushImage(ctx context.Context, request step.Request, dockerClient dockerClient) (step.Result, error) {
	platform, err := parsePlatform(r.config.Platform)
	if err != nil {
		return step.Result{}, err
	}
	auth, err := encodedAuth(r.config.Auth)
	if err != nil {
		return step.Result{}, err
	}
	response, err := dockerClient.ImagePush(ctx, r.config.Image, client.ImagePushOptions{RegistryAuth: auth, Platform: platform})
	if err != nil {
		return step.Result{}, fmt.Errorf("pushing Docker image %q: %w", r.config.Image, err)
	}
	defer response.Close()
	var pushedDigest string
	var auxErr error
	aux := func(message jsonstream.Message) {
		if message.Aux == nil || auxErr != nil {
			return
		}
		var value map[string]any
		if err := json.Unmarshal(*message.Aux, &value); err != nil {
			auxErr = fmt.Errorf("decoding Docker push result: %w", err)
			return
		}
		if reported, ok := value["Digest"].(string); ok {
			pushedDigest = reported
		}
	}
	if err := displayDockerMessages(response.JSONMessages(ctx), writerOrDiscard(request.Stdout), aux); err != nil {
		return step.Result{}, fmt.Errorf("pushing Docker image %q: %w", r.config.Image, err)
	}
	if auxErr != nil {
		return step.Result{}, auxErr
	}
	if pushedDigest == "" {
		var options []client.ImageInspectOption
		if platform != nil {
			options = append(options, client.ImageInspectWithPlatform(platform))
		}
		inspected, inspectErr := dockerClient.ImageInspect(ctx, r.config.Image, options...)
		if inspectErr == nil && inspected.Descriptor != nil {
			pushedDigest = inspected.Descriptor.Digest.String()
		}
	}
	outputs := map[string]any{"image": r.config.Image}
	if pushedDigest != "" {
		outputs["digest"] = pushedDigest
	}
	return step.Result{Outputs: outputs}, nil
}

func (r *Runner) tagImage(ctx context.Context, dockerClient dockerClient) (step.Result, error) {
	_, err := dockerClient.ImageTag(ctx, client.ImageTagOptions{Source: r.config.Source, Target: r.config.Target})
	if err != nil {
		return step.Result{}, fmt.Errorf("tagging Docker image %q as %q: %w", r.config.Source, r.config.Target, err)
	}
	return step.Result{Outputs: map[string]any{"source": r.config.Source, "target": r.config.Target}}, nil
}

func (r *Runner) inspectImage(ctx context.Context, dockerClient dockerClient) (step.Result, error) {
	platform, err := parsePlatform(r.config.Platform)
	if err != nil {
		return step.Result{}, err
	}
	return inspectImage(ctx, dockerClient, r.config.Image, platform)
}

func inspectImage(ctx context.Context, dockerClient dockerClient, image string, platform *v1.Platform) (step.Result, error) {
	var options []client.ImageInspectOption
	if platform != nil {
		options = append(options, client.ImageInspectWithPlatform(platform))
	}
	inspected, err := dockerClient.ImageInspect(ctx, image, options...)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting Docker image %q: %w", image, err)
	}
	labels := map[string]string(nil)
	if inspected.Config != nil {
		labels = maps.Clone(inspected.Config.Labels)
	}
	if labels == nil {
		labels = map[string]string{}
	}
	return step.Result{Outputs: map[string]any{
		"image": image, "id": inspected.ID,
		"repo_tags": append([]string{}, inspected.RepoTags...), "repo_digests": append([]string{}, inspected.RepoDigests...),
		"created": inspected.Created, "size": inspected.Size,
		"platform": formatPlatform(inspected.Os, inspected.Architecture, inspected.Variant),
		"os":       inspected.Os, "architecture": inspected.Architecture, "variant": inspected.Variant,
		"labels": labels,
	}}, nil
}

func (r *Runner) login(ctx context.Context, dockerClient dockerClient) (step.Result, error) {
	auth := r.config.Auth
	response, err := dockerClient.RegistryLogin(ctx, client.RegistryLoginOptions{
		Username: auth.Username, Password: auth.Password, ServerAddress: auth.ServerAddress,
		IdentityToken: auth.IdentityToken, RegistryToken: auth.RegistryToken,
	})
	if err != nil {
		return step.Result{}, fmt.Errorf("authenticating with Docker registry %q: %w", auth.ServerAddress, err)
	}
	return step.Result{Outputs: map[string]any{"server": auth.ServerAddress, "status": response.Auth.Status}}, nil
}

func (r *Runner) createNetwork(ctx context.Context, request step.Request, dockerClient dockerClient) (step.Result, error) {
	if _, err := dockerClient.NetworkInspect(ctx, r.config.Name, client.NetworkInspectOptions{}); err == nil {
		return step.Result{}, fmt.Errorf("Docker network %q already exists", r.config.Name)
	} else if !errdefs.IsNotFound(err) {
		return step.Result{}, fmt.Errorf("checking Docker network %q: %w", r.config.Name, err)
	}
	labels, err := resourceLabels(request, r.config.Labels, cleanupEnabled(r.config))
	if err != nil {
		return step.Result{}, err
	}
	created, err := dockerClient.NetworkCreate(ctx, r.config.Name, client.NetworkCreateOptions{
		Driver: r.config.Driver, Internal: r.config.Internal, Attachable: r.config.Attachable,
		Options: maps.Clone(r.config.Options), Labels: labels,
	})
	if err != nil {
		return step.Result{}, fmt.Errorf("creating Docker network %q: %w", r.config.Name, err)
	}
	if err := ctx.Err(); err != nil {
		cleanupCtx, cancel := detachedCleanupContext(ctx)
		defer cancel()
		_, cleanupErr := dockerClient.NetworkRemove(cleanupCtx, created.ID, client.NetworkRemoveOptions{})
		return step.Result{}, errors.Join(err, cleanupErr)
	}
	return step.Result{Outputs: map[string]any{
		"resource_type": "network", "id": created.ID, "name": r.config.Name, "warnings": slices.Clone(created.Warning),
	}}, nil
}

func (r *Runner) createVolume(ctx context.Context, request step.Request, dockerClient dockerClient) (step.Result, error) {
	if _, err := dockerClient.VolumeInspect(ctx, r.config.Name, client.VolumeInspectOptions{}); err == nil {
		return step.Result{}, fmt.Errorf("Docker volume %q already exists", r.config.Name)
	} else if !errdefs.IsNotFound(err) {
		return step.Result{}, fmt.Errorf("checking Docker volume %q: %w", r.config.Name, err)
	}
	labels, err := resourceLabels(request, r.config.Labels, cleanupEnabled(r.config))
	if err != nil {
		return step.Result{}, err
	}
	created, err := dockerClient.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name: r.config.Name, Driver: r.config.Driver, DriverOpts: maps.Clone(r.config.DriverOptions), Labels: labels,
	})
	if err != nil {
		return step.Result{}, fmt.Errorf("creating Docker volume %q: %w", r.config.Name, err)
	}
	volume := created.Volume
	ownership := labels[ownershipLabel]
	if volume.Labels[ownershipLabel] != ownership {
		return step.Result{}, fmt.Errorf("Docker volume %q does not have the expected Wuko ownership label", r.config.Name)
	}
	if err := ctx.Err(); err != nil {
		cleanupCtx, cancel := detachedCleanupContext(ctx)
		defer cancel()
		_, cleanupErr := dockerClient.VolumeRemove(cleanupCtx, volume.Name, client.VolumeRemoveOptions{})
		return step.Result{}, errors.Join(err, cleanupErr)
	}
	return step.Result{Outputs: map[string]any{
		"resource_type": "volume", "name": volume.Name, "driver": volume.Driver,
		"mountpoint": volume.Mountpoint, "scope": volume.Scope, "ownership_id": ownership,
	}}, nil
}

func (r *Runner) verifyDigest(ctx context.Context, dockerClient dockerClient) (step.Result, error) {
	platform, err := parsePlatform(r.config.Platform)
	if err != nil {
		return step.Result{}, err
	}
	var options []client.ImageInspectOption
	if platform != nil {
		options = append(options, client.ImageInspectWithPlatform(platform))
	}
	inspected, err := dockerClient.ImageInspect(ctx, r.config.Image, options...)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting Docker image %q: %w", r.config.Image, err)
	}
	expected, err := digest.Parse(r.config.ExpectedDigest)
	if err != nil {
		return step.Result{}, fmt.Errorf("expected_digest must be a valid OCI digest: %w", err)
	}
	candidates := make([]digest.Digest, 0, len(inspected.RepoDigests)+1)
	if inspected.Descriptor != nil {
		candidates = append(candidates, inspected.Descriptor.Digest)
	}
	for _, repositoryDigest := range inspected.RepoDigests {
		separator := strings.LastIndexByte(repositoryDigest, '@')
		if separator < 0 {
			continue
		}
		candidate, err := digest.Parse(repositoryDigest[separator+1:])
		if err == nil {
			candidates = append(candidates, candidate)
		}
	}
	for _, candidate := range candidates {
		if candidate == expected {
			return step.Result{Outputs: map[string]any{
				"image": r.config.Image, "expected_digest": expected.String(),
				"actual_digest": candidate.String(), "verified": true,
			}}, nil
		}
	}
	actual := make([]string, len(candidates))
	for i, candidate := range candidates {
		actual[i] = candidate.String()
	}
	return step.Result{}, fmt.Errorf("Docker image %q digest mismatch: expected %s, found %s", r.config.Image, expected, strings.Join(actual, ", "))
}

func encodedAuth(auth *Auth) (string, error) {
	if auth == nil {
		return "", nil
	}
	encoded, err := authconfig.Encode(registry.AuthConfig{
		Username: auth.Username, Password: auth.Password, ServerAddress: auth.ServerAddress,
		IdentityToken: auth.IdentityToken, RegistryToken: auth.RegistryToken,
	})
	if err != nil {
		return "", fmt.Errorf("encoding Docker registry authentication: %w", err)
	}
	return encoded, nil
}

func resourceLabels(request step.Request, configured map[string]string, managed bool) (map[string]string, error) {
	ownerHost, err := clientHostIdentity()
	if err != nil {
		return nil, err
	}
	labels := maps.Clone(configured)
	if labels == nil {
		labels = make(map[string]string, 5)
	}
	labels[managedLabel] = strconv.FormatBool(managed)
	labels[ownerHostLabel] = ownerHost
	labels[ownerPIDLabel] = strconv.Itoa(os.Getpid())
	labels[workflowLabel] = request.WorkflowName
	labels[stepLabel] = request.StepID
	labels[ownershipLabel] = rand.Text()
	return labels, nil
}

func (r *managedRunner) Cleanup(result step.Result) (cleanupErr error) {
	resourceType, _ := result.Outputs["resource_type"].(string)
	identifier, _ := result.Outputs["id"].(string)
	if resourceType == "volume" {
		identifier, _ = result.Outputs["name"].(string)
	} else if identifier == "" {
		identifier, _ = result.Outputs["name"].(string)
	}
	if identifier == "" {
		return fmt.Errorf("managed Docker %s identifier is missing", resourceType)
	}
	dockerClient, err := r.newClient()
	if err != nil {
		return fmt.Errorf("connecting to Docker for cleanup: %w", err)
	}
	defer func() {
		if closeErr := dockerClient.Close(); closeErr != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("closing Docker cleanup client: %w", closeErr))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	switch resourceType {
	case "network":
		_, err = dockerClient.NetworkRemove(ctx, identifier, client.NetworkRemoveOptions{})
	case "volume":
		ownership, _ := result.Outputs["ownership_id"].(string)
		if ownership == "" {
			return fmt.Errorf("managed Docker volume ownership identifier is missing")
		}
		inspected, inspectErr := dockerClient.VolumeInspect(ctx, identifier, client.VolumeInspectOptions{})
		if errdefs.IsNotFound(inspectErr) {
			return nil
		}
		if inspectErr != nil {
			return fmt.Errorf("inspecting managed Docker volume %q: %w", identifier, inspectErr)
		}
		if inspected.Volume.Labels[ownershipLabel] != ownership {
			return fmt.Errorf("refusing to remove Docker volume %q because its Wuko ownership label changed", identifier)
		}
		_, err = dockerClient.VolumeRemove(ctx, identifier, client.VolumeRemoveOptions{})
	default:
		return fmt.Errorf("managed Docker resource type %q is invalid", resourceType)
	}
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("removing managed Docker %s %q: %w", resourceType, identifier, err)
	}
	return nil
}

func formatPlatform(osName, architecture, variant string) string {
	if osName == "" || architecture == "" {
		return ""
	}
	value := osName + "/" + architecture
	if variant != "" {
		value += "/" + variant
	}
	return value
}

func displayDockerMessages(messages iter.Seq2[jsonstream.Message, error], out io.Writer, aux func(jsonstream.Message)) error {
	for message, err := range messages {
		if err != nil {
			return err
		}
		if message.Error != nil {
			return message.Error
		}
		if message.Aux != nil {
			if aux != nil {
				aux(message)
			}
			continue
		}
		line := message.Stream
		if line == "" && message.ID != "" {
			line = message.ID + ": " + message.Status
		} else if line == "" {
			line = message.Status
		}
		if line == "" {
			continue
		}
		if _, err := io.WriteString(out, line); err != nil {
			return err
		}
		if !strings.HasSuffix(line, "\n") {
			if _, err := io.WriteString(out, "\n"); err != nil {
				return err
			}
		}
	}
	return nil
}

var _ step.Cleaner = (*managedRunner)(nil)
