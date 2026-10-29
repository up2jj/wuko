// Package text provides steps that fix and check text files, the policies a pre-commit
// framework applies to the files a commit or push touches.
package text

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/step"
)

// requiredFiles reads the `files` field, which every text step needs.
func requiredFiles(raw map[string]any) (step.PathList, error) {
	files, err := step.ParsePathList(raw, "files")
	if err == nil && !files.Set() {
		err = fmt.Errorf("files is required")
	}
	return files, err
}

// fileListReferences reports the path-list expressions to workflow validation.
func fileListReferences(raw map[string]any) (step.References, error) {
	return step.References{Expressions: []step.ExpressionReference{
		step.PathListReference(raw, "files"), step.PathListReference(raw, "size_exempt"),
	}}, nil
}

// regularFile resolves path against the run directory and reports whether it is a regular
// file. Symbolic links and directories have no text of their own and are skipped.
func regularFile(ctx context.Context, filesystem executor.FileSystem, request step.Request, path string) (string, executor.FileInfo, bool, error) {
	location := path
	if !filepath.IsAbs(location) {
		location = filepath.Join(request.RunDir, filepath.FromSlash(path))
	}
	info, err := filesystem.Stat(ctx, location)
	if err != nil {
		return "", executor.FileInfo{}, false, fmt.Errorf("inspecting %s: %w", path, err)
	}
	return location, info, info.Mode.IsRegular(), nil
}
