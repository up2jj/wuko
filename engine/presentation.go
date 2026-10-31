package engine

import (
	"slices"

	"github.com/up2jj/wuko/workflow"
)

// presentationOptions scopes a declaration's preference without changing its siblings.
func presentationOptions(options Options, declaration workflow.Step) Options {
	if declaration.Quiet != nil {
		options.quietOverride = declaration.Quiet
	}
	if options.quietOverride == nil && (declaration.Type == "print" || slices.Contains(options.presentation.Quiet, executionKind(declaration))) {
		quiet := true
		options.quietOverride = &quiet
	}
	options.quiet = options.quietOverride != nil && *options.quietOverride
	return options
}
