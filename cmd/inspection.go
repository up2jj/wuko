package cmd

import (
	"context"
	"io"
	"path/filepath"

	"github.com/up2jj/wuko/workflow"
)

// loadInspectionFile shares file and stdin loading between validation and tree
// rendering. The input stream is separate from options.Stdin because a streamed
// workflow consumes stdin and must not offer it again to plugins or prompts.
func loadInspectionFile(ctx context.Context, loader *workflow.Loader, filename, baseDir string, input io.Reader, options workflow.LoadOptions) (*workflow.Definition, error) {
	if filename != "-" {
		if !filepath.IsAbs(filename) {
			filename = filepath.Join(baseDir, filename)
		}
		return loader.Load(ctx, filename, options)
	}
	definition, err := loader.DecodeStdinContext(ctx, input, baseDir, options)
	if err != nil {
		return nil, err
	}
	definition, err = definition.SelectTarget(options.Target)
	if err != nil {
		return nil, err
	}
	if err := loader.Prepare(ctx, definition, options); err != nil {
		return nil, err
	}
	return definition, nil
}
