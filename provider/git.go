package provider

import "context"

type gitProvider struct{}

// NewGit returns the schema for Wuko's read-only Git execution context. The provider is inactive
// during ordinary runs; wuko git hook run supplies its invocation-specific value directly.
func NewGit() Provider { return gitProvider{} }

func (gitProvider) Name() string { return "git" }

func (gitProvider) Schema() Schema {
	return Object(map[string]Schema{
		"repository": Object(map[string]Schema{
			"root": Scalar(), "git_dir": Scalar(), "common_dir": Scalar(),
		}),
		"hook": Object(map[string]Schema{
			"name": Scalar(), "args": Scalar(), "stdin": Scalar(), "payload": OpenObject(),
		}),
	})
}

func (gitProvider) Load(context.Context, map[string]string) (map[string]any, bool, error) {
	return nil, false, nil
}
