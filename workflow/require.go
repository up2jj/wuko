package workflow

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type stepFragment struct {
	Steps []Step `yaml:"steps"`
}

// expandWorkflowRequiredSteps expands the require entries of a decoded workflow. A virtual
// workflow has no file of its own on disk - one read from standard input, say - so it is resolved
// from the directory in source, reported against the logical display name, and left off the cycle
// stack because no fragment can require a file that does not exist.
func expandWorkflowRequiredSteps(steps []Step, source, display string, virtual bool) ([]Step, error) {
	if virtual {
		return expandRequiredStepsInSource(steps, source, display, nil)
	}
	return expandRequiredSteps(steps, source, nil)
}

// expandRequiredSteps replaces require entries with the steps from their local YAML files.
// Paths are relative to the file containing the require entry, including for nested fragments.
func expandRequiredSteps(steps []Step, source string, stack []string) ([]Step, error) {
	canonicalSource, err := canonicalFilePath(source)
	if err != nil {
		return nil, err
	}
	for i, ancestor := range stack {
		if ancestor == canonicalSource {
			cycle := append(append([]string(nil), stack[i:]...), canonicalSource)
			for i := range cycle {
				cycle[i] = filepath.Base(cycle[i])
			}
			return nil, fmt.Errorf("required steps form a cycle: %s", strings.Join(cycle, " -> "))
		}
	}
	stack = append(stack, canonicalSource)
	return expandRequiredStepsInSource(steps, source, source, stack)
}

// expandRequiredStepsInSource resolves relative require paths from source and names source in its
// errors as display, which differs only for a workflow whose source path is virtual.
func expandRequiredStepsInSource(steps []Step, source, display string, stack []string) ([]Step, error) {
	expanded := make([]Step, 0, len(steps))
	for i, workflowStep := range steps {
		if workflowStep.IsCancelOn() {
			if err := cancelOnContainsForbidden(workflowStep.CancelOn.Monitors); err != nil {
				return nil, fmt.Errorf("cancel_on monitors at step %d in %s: %w", i+1, display, err)
			}
			if err := cancelOnContainsForbidden(workflowStep.CancelOn.Steps); err != nil {
				return nil, fmt.Errorf("cancel_on body at step %d in %s: %w", i+1, display, err)
			}
		}
		if len(workflowStep.childSequenceRefs()) > 0 {
			err := workflowStep.transformChildSequences(func(role ChildRole, children []Step) ([]Step, error) {
				expandedChildren, err := expandRequiredStepsInSource(children, source, display, stack)
				if err != nil {
					return nil, fmt.Errorf("%s at step %d in %s: %w", requiredChildContext(workflowStep, role), i+1, display, err)
				}
				return expandedChildren, nil
			})
			if err != nil {
				return nil, err
			}
			expanded = append(expanded, workflowStep)
			continue
		}
		if workflowStep.Require == nil {
			expanded = append(expanded, workflowStep)
			continue
		}
		if err := validateRequireEntry(workflowStep); err != nil {
			return nil, fmt.Errorf("step %d in %s: %w", i+1, display, err)
		}

		requiredPath := *workflowStep.Require
		if strings.TrimSpace(requiredPath) == "" {
			return nil, fmt.Errorf("step %d in %s: require must be a non-empty local file path", i+1, display)
		}
		if filepath.IsAbs(requiredPath) {
			return nil, fmt.Errorf("step %d in %s: require path %q must be relative", i+1, display, requiredPath)
		}
		requiredPath = filepath.Join(filepath.Dir(source), filepath.FromSlash(requiredPath))
		required, err := loadStepFragment(requiredPath)
		if err != nil {
			return nil, fmt.Errorf("step %d in %s requires %q: %w", i+1, display, *workflowStep.Require, err)
		}
		required, err = expandRequiredSteps(required, requiredPath, stack)
		if err != nil {
			return nil, fmt.Errorf("step %d in %s requires %q: %w", i+1, display, *workflowStep.Require, err)
		}
		expanded = append(expanded, required...)
	}
	return expanded, nil
}

func requiredChildContext(workflowStep Step, role ChildRole) string {
	switch {
	case workflowStep.IsExecutorBlock() && role == ChildFinally:
		return "executor block finally"
	case role == ChildDefer:
		return "step defer"
	case workflowStep.IsExecutorBlock():
		return "executor block"
	case workflowStep.IsEnvironmentBlock():
		return "env block"
	case workflowStep.IsWorkingDirectoryBlock():
		return "working_directory block"
	case workflowStep.IsWorktreeBlock():
		return "worktree block"
	case workflowStep.IsConditionalBlock():
		return "conditional block"
	case workflowStep.IsTryCatch() && role == ChildTry:
		return "try block"
	case workflowStep.IsTryCatch() && role == ChildCatch:
		return "catch block"
	case workflowStep.IsCancelOn() && role == ChildMonitors:
		return "cancel_on monitors"
	case workflowStep.IsCancelOn():
		return "cancel_on body"
	case workflowStep.IsObserve():
		return "observe body"
	case workflowStep.Concurrent != nil:
		return "concurrent group"
	case workflowStep.Batch != nil:
		return "batch group"
	case workflowStep.Foreach != nil:
		return "foreach group"
	case workflowStep.Matrix != nil:
		return "matrix group"
	case workflowStep.Once != nil:
		return "once block"
	case workflowStep.Attempt != nil:
		return "attempt block"
	default:
		panic("step has no child sequence")
	}
}

func validateRequireEntry(workflowStep Step) error {
	if workflowStep.ID != "" || workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.Worktree != nil || workflowStep.IsConditionalBlock() || workflowStep.IsEnvironmentBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Once != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.If != "" || workflowStep.Attempt != nil || workflowStep.With != nil {
		return fmt.Errorf("require cannot be combined with other step fields")
	}
	return nil
}

func canonicalFilePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving path %s: %w", path, err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolving path %s: %w", path, err)
	}
	return canonical, nil
}

func loadStepFragment(path string) ([]Step, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading required steps %s: %w", path, err)
	}

	var document yaml.Node
	if err := decodeOneYAML(data, path, &document, false); err != nil {
		return nil, err
	}
	if len(document.Content) != 1 {
		return nil, fmt.Errorf("decoding required steps %s: document must not be empty", path)
	}

	switch document.Content[0].Kind {
	case yaml.SequenceNode:
		var steps []Step
		if err := decodeOneYAML(data, path, &steps, true); err != nil {
			return nil, err
		}
		if len(steps) == 0 {
			return nil, fmt.Errorf("decoding required steps %s: at least one step is required", path)
		}
		annotateFragmentLocations(data, steps, path)
		return steps, nil
	case yaml.MappingNode:
		var fragment stepFragment
		if err := decodeOneYAML(data, path, &fragment, true); err != nil {
			return nil, err
		}
		if len(fragment.Steps) == 0 {
			return nil, fmt.Errorf("decoding required steps %s: at least one step is required", path)
		}
		annotateFragmentLocations(data, fragment.Steps, path)
		return fragment.Steps, nil
	default:
		return nil, fmt.Errorf("decoding required steps %s: document must be a step list or an object containing steps", path)
	}
}

func decodeOneYAML(data []byte, path string, target any, knownFields bool) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(knownFields)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decoding required steps %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decoding required steps %s: multiple YAML documents are not supported", path)
		}
		return fmt.Errorf("decoding required steps %s: %w", path, err)
	}
	return nil
}
