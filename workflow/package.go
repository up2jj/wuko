package workflow

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	// WorkflowPackageMarkerName identifies an installed workflow package directory.
	WorkflowPackageMarkerName = ".wuko-package.json"
)

// ErrWorkflowPackageManifestNotFound indicates that a directory is not a workflow package root.
var ErrWorkflowPackageManifestNotFound = errors.New("workflow package manifest not found")

type packageFile struct {
	name string
	data []byte
	mode os.FileMode
}

// WorkflowPackageManifestPath returns the canonical workflow manifest path in a package.
func WorkflowPackageManifestPath(directory string) (string, error) {
	for _, name := range []string{"wuko.yaml", "wuko.yml"} {
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("workflow package manifest %s is not a regular file", candidate)
			}
			for _, other := range []string{"wuko.yaml", "wuko.yml"} {
				if other == name {
					continue
				}
				if _, otherErr := os.Stat(filepath.Join(directory, other)); otherErr == nil {
					return "", fmt.Errorf("workflow package contains both wuko.yaml and wuko.yml")
				} else if !os.IsNotExist(otherErr) {
					return "", fmt.Errorf("checking workflow package manifest %s: %w", filepath.Join(directory, other), otherErr)
				}
			}
			return candidate, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("checking workflow package manifest %s: %w", candidate, err)
		}
	}
	return "", fmt.Errorf("%w: %s must contain wuko.yaml or wuko.yml", ErrWorkflowPackageManifestNotFound, directory)
}

// WorkflowPackageDigest returns the canonical digest of all regular files in a package.
func WorkflowPackageDigest(directory string) (string, error) {
	files, err := collectPackageFiles(directory)
	if err != nil {
		return "", err
	}
	return digestPackageFiles(files), nil
}

// BuildWorkflowPackage creates a deterministic tar.gz archive and returns the source-tree and
// archive digests. The archive is replaced atomically.
func BuildWorkflowPackage(sourceDir, outputPath string) (string, string, error) {
	files, err := collectPackageFiles(sourceDir)
	if err != nil {
		return "", "", err
	}
	sourceDigest := digestPackageFiles(files)
	payload, err := encodePackageArchive(files)
	if err != nil {
		return "", "", fmt.Errorf("encoding workflow package %s: %w", sourceDir, err)
	}
	if len(payload) > maxArchiveSize {
		return "", "", fmt.Errorf("workflow package archive exceeds %d-byte download limit", maxArchiveSize)
	}
	archiveDigest := digestBytes(payload)
	if err := writePackageBytesAtomically(outputPath, payload); err != nil {
		return "", "", err
	}
	return sourceDigest, archiveDigest, nil
}

// CopyWorkflowPackage copies a validated materialized package directory to targetDir.
func CopyWorkflowPackage(sourceDir, targetDir string) error {
	files, err := collectPackageFiles(sourceDir)
	if err != nil {
		return err
	}
	for _, file := range files {
		target := filepath.Join(targetDir, filepath.FromSlash(file.name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("creating package directory for %s: %w", file.name, err)
		}
		mode := file.mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(target, file.data, mode); err != nil {
			return fmt.Errorf("writing package file %s: %w", file.name, err)
		}
	}
	return nil
}

func collectPackageFiles(directory string) ([]packageFile, error) {
	if _, err := WorkflowPackageManifestPath(directory); err != nil {
		return nil, err
	}
	var files []packageFile
	var total int64
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == directory {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("workflow package contains symlink %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("reading workflow package file %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("workflow package file %s is not regular", path)
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return fmt.Errorf("relating workflow package file %s: %w", path, err)
		}
		name := filepath.ToSlash(relative)
		if name == "wuko.yml" {
			name = "wuko.yaml"
		}
		if err := validateArchivePath(name); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading workflow package file %s: %w", path, err)
		}
		total += int64(len(data))
		if total > maxExtracted {
			return fmt.Errorf("workflow package exceeds %d-byte extracted limit", maxExtracted)
		}
		if name == "wuko.yaml" && len(data) > maxManifestSize {
			return fmt.Errorf("workflow package manifest exceeds %d-byte limit", maxManifestSize)
		}
		files = append(files, packageFile{name: name, data: data, mode: info.Mode()})
		if len(files) > maxEntries {
			return fmt.Errorf("workflow package exceeds %d-entry limit", maxEntries)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(files, func(a, b packageFile) int { return strings.Compare(a.name, b.name) })
	for i := 1; i < len(files); i++ {
		if files[i-1].name == files[i].name {
			return nil, fmt.Errorf("workflow package contains duplicate path %q", files[i].name)
		}
	}
	return files, nil
}

func digestPackageFiles(files []packageFile) string {
	hash := sha256.New()
	var buffer [8]byte
	for _, file := range files {
		binary.BigEndian.PutUint64(buffer[:], uint64(len(file.name)))
		_, _ = hash.Write(buffer[:])
		_, _ = io.WriteString(hash, file.name)
		binary.BigEndian.PutUint64(buffer[:], uint64(file.mode.Perm()))
		_, _ = hash.Write(buffer[:])
		binary.BigEndian.PutUint64(buffer[:], uint64(len(file.data)))
		_, _ = hash.Write(buffer[:])
		_, _ = hash.Write(file.data)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func encodePackageArchive(files []packageFile) ([]byte, error) {
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	gzipWriter.Header.ModTime = time.Unix(0, 0)
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, file := range files {
		header := &tar.Header{
			Name:    file.name,
			Mode:    int64(file.mode.Perm()),
			Size:    int64(len(file.data)),
			ModTime: time.Unix(0, 0),
			Format:  tar.FormatPAX,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := tarWriter.Write(file.data); err != nil {
			return nil, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writePackageBytesAtomically(filename string, data []byte) error {
	directory := filepath.Dir(filename)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("creating package output directory %s: %w", directory, err)
	}
	file, err := os.CreateTemp(directory, "."+filepath.Base(filename)+"-*")
	if err != nil {
		return fmt.Errorf("creating temporary package archive: %w", err)
	}
	temporary := file.Name()
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o644); err != nil {
		return fmt.Errorf("setting package archive permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("writing package archive: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("syncing package archive: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing package archive: %w", err)
	}
	if err := os.Rename(temporary, filename); err != nil {
		return fmt.Errorf("replacing package archive %s: %w", filename, err)
	}
	removeTemporary = false
	directoryFile, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("opening package output directory: %w", err)
	}
	defer directoryFile.Close()
	if err := directoryFile.Sync(); err != nil {
		return fmt.Errorf("syncing package output directory: %w", err)
	}
	return nil
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
