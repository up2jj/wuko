package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/up2jj/wuko/step"
)

type commandRunner func(context.Context, string, []string, io.Writer, io.Writer) error

func defaultCommandRunner(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error {
	executable, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func (r *Runner) runBuild(ctx context.Context, request step.Request) (step.Result, error) {
	runCommand := r.runCommand
	if runCommand == nil {
		runCommand = defaultCommandRunner
	}
	var versionOutput bytes.Buffer
	if err := runCommand(ctx, "docker", []string{"buildx", "version"}, io.Discard, &versionOutput); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return step.Result{}, ctxErr
		}
		var executableError *exec.Error
		if errors.As(err, &executableError) {
			return step.Result{}, fmt.Errorf("Docker executable is unavailable: %w", err)
		}
		detail := strings.TrimSpace(versionOutput.String())
		if detail == "" {
			detail = err.Error()
		}
		return step.Result{}, fmt.Errorf("Docker Buildx is unavailable: %s", detail)
	}

	contextPath, dockerfilePath, err := r.buildPaths(request.RunDir)
	if err != nil {
		return step.Result{}, err
	}
	metadata, err := os.CreateTemp("", "wuko-buildx-metadata-*.json")
	if err != nil {
		return step.Result{}, fmt.Errorf("creating Docker Buildx metadata file: %w", err)
	}
	metadataPath := metadata.Name()
	if err := metadata.Close(); err != nil {
		_ = os.Remove(metadataPath)
		return step.Result{}, fmt.Errorf("closing Docker Buildx metadata file: %w", err)
	}
	defer os.Remove(metadataPath)

	args := []string{"buildx", "build", "--metadata-file", metadataPath, "--file", dockerfilePath}
	if r.config.Output == "load" {
		args = append(args, "--load")
	} else {
		args = append(args, "--push")
	}
	for _, tag := range r.config.Tags {
		args = append(args, "--tag", tag)
	}
	if len(r.config.Platforms) > 0 {
		args = append(args, "--platform", strings.Join(r.config.Platforms, ","))
	}
	if r.config.Target != "" {
		args = append(args, "--target", r.config.Target)
	}
	if pull, _ := r.config.Pull.(bool); pull {
		args = append(args, "--pull")
	}
	if r.config.NoCache {
		args = append(args, "--no-cache")
	}
	for _, key := range slices.Sorted(maps.Keys(r.config.BuildArgs)) {
		args = append(args, "--build-arg", key+"="+r.config.BuildArgs[key])
	}
	for _, descriptor := range r.config.CacheFrom {
		args = append(args, "--cache-from", descriptor)
	}
	for _, descriptor := range r.config.CacheTo {
		args = append(args, "--cache-to", descriptor)
	}
	args = append(args, contextPath)

	if err := runCommand(ctx, "docker", args, writerOrDiscard(request.Stdout), writerOrDiscard(request.Stderr)); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return step.Result{}, ctxErr
		}
		return step.Result{}, fmt.Errorf("Docker Buildx build failed: %w", err)
	}
	digest, err := buildDigest(metadataPath)
	if err != nil {
		return step.Result{}, err
	}
	outputs := map[string]any{
		"tags": slices.Clone(r.config.Tags), "platforms": slices.Clone(r.config.Platforms), "output": r.config.Output,
	}
	if digest != "" {
		outputs["digest"] = digest
	}
	return step.Result{Outputs: outputs}, nil
}

func (r *Runner) buildPaths(runDir string) (string, string, error) {
	if runDir == "" {
		runDir = "."
	}
	contextPath := r.config.Context
	if contextPath == "" {
		contextPath = "."
	}
	if !filepath.IsAbs(contextPath) {
		contextPath = filepath.Join(runDir, contextPath)
	}
	contextPath, err := filepath.Abs(contextPath)
	if err != nil {
		return "", "", fmt.Errorf("resolving Docker build context: %w", err)
	}
	info, err := os.Stat(contextPath)
	if err != nil {
		return "", "", fmt.Errorf("inspecting Docker build context %s: %w", contextPath, err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("Docker build context %s is not a directory", contextPath)
	}

	dockerfilePath := r.config.Dockerfile
	if dockerfilePath == "" {
		dockerfilePath = "Dockerfile"
	}
	if !filepath.IsAbs(dockerfilePath) {
		dockerfilePath = filepath.Join(contextPath, dockerfilePath)
	}
	dockerfilePath, err = filepath.Abs(dockerfilePath)
	if err != nil {
		return "", "", fmt.Errorf("resolving Dockerfile: %w", err)
	}
	info, err = os.Stat(dockerfilePath)
	if err != nil {
		return "", "", fmt.Errorf("inspecting Dockerfile %s: %w", dockerfilePath, err)
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("Dockerfile %s is not a regular file", dockerfilePath)
	}
	return contextPath, dockerfilePath, nil
}

func buildDigest(metadataPath string) (string, error) {
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return "", fmt.Errorf("reading Docker Buildx metadata: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", nil
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		return "", fmt.Errorf("decoding Docker Buildx metadata: %w", err)
	}
	if value, ok := metadata["containerimage.digest"].(string); ok {
		return value, nil
	}
	if descriptor, ok := metadata["containerimage.descriptor"].(map[string]any); ok {
		if value, ok := descriptor["digest"].(string); ok {
			return value, nil
		}
	}
	return "", nil
}
