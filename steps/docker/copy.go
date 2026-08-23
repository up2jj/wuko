package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/moby/go-archive"
	"github.com/moby/moby/client"

	"github.com/up2jj/wuko/step"
)

func (r *Runner) copyToContainer(ctx context.Context, request step.Request, dockerClient dockerClient) (result step.Result, runErr error) {
	sourcePath, err := resolveCopyHostPath(request.RunDir, r.config.Source)
	if err != nil {
		return step.Result{}, fmt.Errorf("resolving Docker copy_to source %q: %w", r.config.Source, err)
	}

	sourceInfo, err := archive.CopyInfoSourcePath(sourcePath, false)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting Docker copy_to source %q: %w", sourcePath, err)
	}
	sourceArchive, err := archive.TarResource(sourceInfo)
	if err != nil {
		return step.Result{}, fmt.Errorf("archiving Docker copy_to source %q: %w", sourcePath, err)
	}
	defer joinCopyCloseError(&runErr, sourceArchive, "source archive")

	destinationInfo, err := r.containerDestinationInfo(ctx, dockerClient)
	if err != nil {
		return step.Result{}, err
	}
	destinationPath, preparedArchive, err := archive.PrepareArchiveCopy(sourceArchive, sourceInfo, destinationInfo)
	if err != nil {
		return step.Result{}, fmt.Errorf("preparing Docker copy_to from %q to %q: %w", sourcePath, r.config.Target, err)
	}
	defer joinCopyCloseError(&runErr, preparedArchive, "prepared archive")

	_, err = dockerClient.CopyToContainer(ctx, r.config.Container, client.CopyToContainerOptions{
		DestinationPath: destinationPath,
		Content:         preparedArchive,
	})
	if err != nil {
		return step.Result{}, fmt.Errorf("copying host path %q to Docker container %q at %q: %w", sourcePath, r.config.Container, r.config.Target, err)
	}
	return dockerCopyResult(r.config), nil
}

func (r *Runner) containerDestinationInfo(ctx context.Context, dockerClient dockerClient) (archive.CopyInfo, error) {
	destination := archive.CopyInfo{Path: r.config.Target}
	stat, err := dockerClient.ContainerStatPath(ctx, r.config.Container, client.ContainerStatPathOptions{Path: destination.Path})
	if err != nil {
		return destination, nil
	}
	if stat.Stat.Mode&os.ModeSymlink != 0 {
		linkTarget := stat.Stat.LinkTarget
		if !filepath.IsAbs(linkTarget) {
			parent, _ := archive.SplitPathDirEntry(destination.Path)
			linkTarget = filepath.Join(parent, linkTarget)
		}
		destination.Path = linkTarget
		stat, err = dockerClient.ContainerStatPath(ctx, r.config.Container, client.ContainerStatPathOptions{Path: linkTarget})
	}
	if err == nil {
		if !stat.Stat.Mode.IsDir() && !stat.Stat.Mode.IsRegular() {
			return archive.CopyInfo{}, fmt.Errorf("Docker copy_to target %q in container %q must be a directory or regular file", r.config.Target, r.config.Container)
		}
		destination.Exists = true
		destination.IsDir = stat.Stat.Mode.IsDir()
	}
	return destination, nil
}

func (r *Runner) copyFromContainer(ctx context.Context, request step.Request, dockerClient dockerClient) (result step.Result, runErr error) {
	targetPath, err := resolveCopyHostPath(request.RunDir, r.config.Target)
	if err != nil {
		return step.Result{}, fmt.Errorf("resolving Docker copy_from target %q: %w", r.config.Target, err)
	}

	copied, err := dockerClient.CopyFromContainer(ctx, r.config.Container, client.CopyFromContainerOptions{SourcePath: r.config.Source})
	if err != nil {
		return step.Result{}, fmt.Errorf("copying %q from Docker container %q: %w", r.config.Source, r.config.Container, err)
	}
	defer joinCopyCloseError(&runErr, copied.Content, "container archive")

	sourceInfo := archive.CopyInfo{Path: r.config.Source, Exists: true, IsDir: copied.Stat.Mode.IsDir()}
	if err := archive.CopyTo(copied.Content, sourceInfo, targetPath); err != nil {
		return step.Result{}, fmt.Errorf("extracting Docker container %q path %q to host path %q: %w", r.config.Container, r.config.Source, targetPath, err)
	}
	return dockerCopyResult(r.config), nil
}

func resolveCopyHostPath(runDir, configured string) (string, error) {
	path := configured
	if !filepath.IsAbs(path) {
		path = filepath.Join(runDir, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return archive.PreserveTrailingDotOrSeparator(absolute, configured), nil
}

func dockerCopyResult(config Config) step.Result {
	return step.Result{Outputs: map[string]any{
		"container": config.Container,
		"source":    config.Source,
		"target":    config.Target,
	}}
}

func joinCopyCloseError(target *error, closer io.Closer, description string) {
	if err := closer.Close(); err != nil {
		*target = errors.Join(*target, fmt.Errorf("closing Docker copy %s: %w", description, err))
	}
}
