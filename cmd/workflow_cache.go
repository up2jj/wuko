package cmd

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/workflow"
)

// workflowInvocationCache memoizes the two costs a child-workflow invocation would otherwise pay
// again on every call: filesystem discovery, which reads and YAML-parses every discoverable
// workflow file, and semantic validation, which recurses into the child's own run_workflow steps
// and so compounds with nesting depth. Without it a foreach of N iterations over a call chain of
// depth d performs both N*2^d times.
//
// The cache lives for one command execution and is shared by every invoker built during it, so a
// workflow file created by a running workflow is not visible to a later invocation in the same
// run. It is safe for parallel branches.
type workflowInvocationCache struct {
	mu        sync.Mutex
	sources   map[string][]workflow.Source
	validated map[string]struct{}
}

// discover returns the effective workflow sources for one discovery root. The returned slice is
// shared with later callers and must not be modified.
func (cache *workflowInvocationCache) discover(cwd, homeDir, configDir string) ([]workflow.Source, error) {
	if cache == nil {
		return workflow.Discover(cwd, homeDir, configDir)
	}
	key := strings.Join([]string{cwd, homeDir, configDir}, "\x00")
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if sources, cached := cache.sources[key]; cached {
		return sources, nil
	}
	sources, err := workflow.Discover(cwd, homeDir, configDir)
	if err != nil {
		return nil, err
	}
	if cache.sources == nil {
		cache.sources = make(map[string][]workflow.Source)
	}
	cache.sources[key] = sources
	return sources, nil
}

func (cache *workflowInvocationCache) find(cwd, homeDir, configDir, name string) (workflow.Source, error) {
	sources, err := cache.discover(cwd, homeDir, configDir)
	if err != nil {
		return workflow.Source{}, err
	}
	return workflow.SelectSource(sources, name)
}

func (cache *workflowInvocationCache) alreadyValidated(key string) bool {
	if cache == nil || key == "" {
		return false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	_, validated := cache.validated[key]
	return validated
}

func (cache *workflowInvocationCache) recordValidated(key string) {
	if cache == nil || key == "" {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.validated == nil {
		cache.validated = make(map[string]struct{})
	}
	cache.validated[key] = struct{}{}
}

// validationKey identifies one child-workflow call shape. Two calls resolving to the same file,
// target and variable values validate identically, so the second can reuse the first's verdict.
// An empty key means the shape cannot be compared and must be validated again.
func validationKey(source workflow.Source, invocation engine.WorkflowInvocation) string {
	vars, err := json.Marshal(invocation.Vars())
	if err != nil {
		return ""
	}
	return strings.Join([]string{source.Path, invocation.Target(), string(vars)}, "\x00")
}
