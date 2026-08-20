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
	return expandRequiredStepsInSource(steps, source, stack)
}

func expandRequiredStepsInSource(steps []Step, source string, stack []string) ([]Step, error) {
	expanded := make([]Step, 0, len(steps))
	for i, workflowStep := range steps {
		if workflowStep.Concurrent != nil {
			children, err := expandRequiredStepsInSource(workflowStep.Concurrent.Steps, source, stack)
			if err != nil {
				return nil, fmt.Errorf("concurrent group at step %d in %s: %w", i+1, source, err)
			}
			workflowStep.Concurrent.Steps = children
			expanded = append(expanded, workflowStep)
			continue
		}
		if workflowStep.Require == nil {
			expanded = append(expanded, workflowStep)
			continue
		}
		if err := validateRequireEntry(workflowStep); err != nil {
			return nil, fmt.Errorf("step %d in %s: %w", i+1, source, err)
		}

		requiredPath := *workflowStep.Require
		if strings.TrimSpace(requiredPath) == "" {
			return nil, fmt.Errorf("step %d in %s: require must be a non-empty local file path", i+1, source)
		}
		if filepath.IsAbs(requiredPath) {
			return nil, fmt.Errorf("step %d in %s: require path %q must be relative", i+1, source, requiredPath)
		}
		requiredPath = filepath.Join(filepath.Dir(source), filepath.FromSlash(requiredPath))
		required, err := loadStepFragment(requiredPath)
		if err != nil {
			return nil, fmt.Errorf("step %d in %s requires %q: %w", i+1, source, *workflowStep.Require, err)
		}
		required, err = expandRequiredSteps(required, requiredPath, stack)
		if err != nil {
			return nil, fmt.Errorf("step %d in %s requires %q: %w", i+1, source, *workflowStep.Require, err)
		}
		expanded = append(expanded, required...)
	}
	return expanded, nil
}

func validateRequireEntry(workflowStep Step) error {
	if workflowStep.ID != "" || workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Concurrent != nil || workflowStep.SHA256 != "" || workflowStep.If != "" || workflowStep.Timeout != nil || workflowStep.Retry != nil || workflowStep.With != nil {
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
