package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	slashpath "path"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
)

// The Docker executor exposes the container filesystem through the archive endpoints, so
// steps that read and write files run against the container rather than the host. Only
// paths covered by a bind mount, or already absolute inside the image, have a container
// counterpart; anything else is rejected rather than silently redirected to the host.

func (session *dockerExecutorSession) Stat(ctx context.Context, path string) (executor.FileInfo, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	target, err := session.prepareFilePath(ctx, path)
	if err != nil {
		return executor.FileInfo{}, err
	}
	result, err := session.client.ContainerStatPath(ctx, session.containerID, client.ContainerStatPathOptions{Path: target})
	if err != nil {
		return executor.FileInfo{}, containerPathError("inspecting", path, err)
	}
	return executor.FileInfo{Size: result.Stat.Size, Mode: result.Stat.Mode, ModTime: result.Stat.Mtime}, nil
}

func (session *dockerExecutorSession) ReadFile(ctx context.Context, path string, maximum int64) ([]byte, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	target, err := session.prepareFilePath(ctx, path)
	if err != nil {
		return nil, err
	}
	copied, err := session.client.CopyFromContainer(ctx, session.containerID, client.CopyFromContainerOptions{SourcePath: target})
	if err != nil {
		return nil, containerPathError("reading", path, err)
	}
	defer copied.Content.Close()
	reader := tar.NewReader(copied.Content)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("reading %s in Docker container %s: archive is empty", path, session.containerID)
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s in Docker container %s: %w", path, session.containerID, err)
		}
		if header.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("reading %s in Docker container %s: not a regular file", path, session.containerID)
		}
		var content io.Reader = reader
		if maximum > 0 {
			limit := maximum
			if maximum < math.MaxInt64 {
				limit++
			}
			content = io.LimitReader(reader, limit)
		}
		data, err := io.ReadAll(content)
		if err != nil {
			return nil, fmt.Errorf("reading %s in Docker container %s: %w", path, session.containerID, err)
		}
		return data, nil
	}
}

func (session *dockerExecutorSession) WriteFile(ctx context.Context, path string, data []byte, options executor.WriteOptions) error {
	session.mu.Lock()
	target, err := session.prepareFilePath(ctx, path)
	if err != nil {
		session.mu.Unlock()
		return err
	}
	shell, ok := serviceShell(session.config)
	if !ok {
		session.mu.Unlock()
		return fmt.Errorf("writing %s in Docker container requires a shell-backed init", path)
	}
	session.mu.Unlock()

	temporary := slashpath.Join(slashpath.Dir(target), ".wuko-write-"+rand.Text())
	replace := "false"
	if options.Replace {
		replace = "true"
	}
	result, err := session.Run(ctx, process.Options{
		Command: shell,
		Args: []string{"-c", installFileScript, "wuko-file", temporary, target,
			fmt.Sprintf("%04o", options.Mode.Perm()), replace},
		Dir: "/", Stdin: bytes.NewReader(data),
		StdoutPolicy: process.OutputDiscard, StderrPolicy: process.OutputCapture, CaptureLimit: 64 << 10,
	})
	if !options.Replace && result.ExitCode == fileExistsExitCode {
		return &fs.PathError{Op: "write", Path: path, Err: fs.ErrExist}
	}
	if err != nil {
		detail := strings.TrimSpace(result.Stderr)
		if detail != "" {
			return fmt.Errorf("writing %s in Docker container: %s: %w", path, detail, err)
		}
		return fmt.Errorf("writing %s in Docker container: %w", path, err)
	}
	return nil
}

const fileExistsExitCode = 73

const installFileScript = `set -eu
temporary=$1
target=$2
mode=$3
replace=$4
trap 'rm -f "$temporary"' 0 1 2 15
umask 077
cat > "$temporary"
chmod "$mode" "$temporary"
if [ "$replace" = true ]; then
  mv -fT "$temporary" "$target"
else
  if ! ln -T "$temporary" "$target"; then
    if [ -e "$target" ] || [ -L "$target" ]; then
      exit 73
    fi
    exit 1
  fi
  rm "$temporary"
fi`

func (session *dockerExecutorSession) MkdirAll(ctx context.Context, path string, mode fs.FileMode) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	target, err := session.prepareFilePath(ctx, path)
	if err != nil {
		return err
	}
	// Docker extracts an archive into an existing directory, so walk up to the deepest
	// component that already exists and let one archive create everything below it.
	var missing []string
	existing := target
	for {
		stat, err := session.client.ContainerStatPath(ctx, session.containerID, client.ContainerStatPathOptions{Path: existing})
		if err == nil {
			if !stat.Stat.Mode.IsDir() {
				return fmt.Errorf("creating %s in Docker container %s: %s exists and is not a directory", path, session.containerID, existing)
			}
			break
		}
		if !errdefs.IsNotFound(err) {
			return containerPathError("creating", path, err)
		}
		parent := slashpath.Dir(existing)
		if parent == existing {
			return fmt.Errorf("creating %s in Docker container %s: no existing parent directory", path, session.containerID)
		}
		missing = append(missing, slashpath.Base(existing))
		existing = parent
	}
	if len(missing) == 0 {
		return nil
	}
	entries := make([]archiveEntry, 0, len(missing))
	name := ""
	for index := len(missing) - 1; index >= 0; index-- {
		name = slashpath.Join(name, missing[index])
		entries = append(entries, archiveEntry{name: name, mode: mode, directory: true})
	}
	archive, err := buildArchive(entries...)
	if err != nil {
		return fmt.Errorf("archiving %s for Docker container %s: %w", path, session.containerID, err)
	}
	if _, err := session.client.CopyToContainer(ctx, session.containerID, client.CopyToContainerOptions{
		DestinationPath: existing,
		Content:         bytes.NewReader(archive),
		CopyUIDGID:      true,
	}); err != nil {
		return containerPathError("creating", path, err)
	}
	return nil
}

// prepareFilePath starts the container if it is not running yet and resolves one host path
// to its container counterpart. Callers hold session.mu.
func (session *dockerExecutorSession) prepareFilePath(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if err := session.startLocked(ctx); err != nil {
		return "", err
	}
	target, ok := session.mapPath(path)
	if !ok {
		return "", fmt.Errorf("path %q is not covered by a bind mount, so it has no location inside the container", path)
	}
	return target, nil
}

// containerPathError keeps a missing path reportable as fs.ErrNotExist, which is how the
// FileSystem contract distinguishes absence from failure.
func containerPathError(operation, path string, err error) error {
	if errdefs.IsNotFound(err) {
		return &fs.PathError{Op: operation, Path: path, Err: fs.ErrNotExist}
	}
	return fmt.Errorf("%s %s in Docker container: %w", operation, path, err)
}

type archiveEntry struct {
	name      string
	mode      fs.FileMode
	data      []byte
	directory bool
}

func buildArchive(entries ...archiveEntry) ([]byte, error) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: int64(entry.mode.Perm()), Size: int64(len(entry.data)), Typeflag: tar.TypeReg}
		if entry.directory {
			header.Name += "/"
			header.Size = 0
			header.Typeflag = tar.TypeDir
		}
		if err := writer.WriteHeader(header); err != nil {
			return nil, err
		}
		if !entry.directory {
			if _, err := writer.Write(entry.data); err != nil {
				return nil, err
			}
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

var _ executor.FileSystem = (*dockerExecutorSession)(nil)
