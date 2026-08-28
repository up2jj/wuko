package edit

import (
	"context"
	"fmt"
	"sort"

	"github.com/expr-lang/expr"
	theoryjsonpath "github.com/theory/jsonpath"
	"github.com/theory/jsonpath/spec"
	"github.com/up2jj/wuko/step"
)

type mutation struct {
	operation   string
	match       *spec.LocatedNode
	replacement any
	output      any
	name        string
	position    string
	changed     bool
	created     bool
}

func (r *Runner) planMutations(ctx context.Context, request step.Request, original any) ([]mutation, error) {
	matches := r.path.SelectLocated(original)
	created := false
	if len(matches) == 0 && r.config.Missing == "create" {
		path, err := createPath(r.path, original)
		if err != nil {
			return nil, err
		}
		matches = []*spec.LocatedNode{{Path: path}}
		created = true
	}
	if len(matches) == 0 {
		if r.config.Missing == "error" {
			return nil, fmt.Errorf("path returned no matches")
		}
		return nil, nil
	}
	var err error
	matches, err = uniqueNonOverlapping(matches)
	if err != nil {
		return nil, err
	}
	if r.config.Result == "one" && len(matches) != 1 {
		return nil, fmt.Errorf("path returned %d matches, want exactly one", len(matches))
	}
	if r.config.Operation == "delete" && len(matches[0].Path) == 0 {
		return nil, fmt.Errorf("operation delete cannot target the document root")
	}

	mutations := make([]mutation, len(matches))
	base := r.baseEnvironment(request)
	destinations := make(map[string]struct{}, len(matches))
	for i, match := range matches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m := mutation{operation: r.config.Operation, match: match, name: r.config.Name, position: r.config.Position}
		m.created = created
		if oneOf(r.config.Operation, "set", "append", "insert", "merge") {
			replacement, err := r.replacement(base, match, i)
			if err != nil {
				return nil, err
			}
			m.replacement = replacement
			m.output = clone(replacement)
		}
		switch r.config.Operation {
		case "set":
			m.changed = m.created || !sameValue(match.Node, m.replacement)
		case "delete":
			m.changed = true
		case "append":
			if _, ok := match.Node.([]any); !ok {
				return nil, fmt.Errorf("appending to %s: selected value is %T, want array", match.Path, match.Node)
			}
			m.changed = true
		case "insert":
			if err := validateArrayElement(original, match.Path); err != nil {
				return nil, fmt.Errorf("inserting at %s: %w", match.Path, err)
			}
			m.changed = true
		case "merge":
			current, ok := match.Node.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("merging %s: selected value is %T, want object", match.Path, match.Node)
			}
			patch, ok := m.replacement.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("merging %s: replacement is %T, want object", match.Path, m.replacement)
			}
			m.replacement = deepMerge(current, patch)
			m.output = clone(m.replacement)
			m.changed = !sameValue(current, m.replacement)
		case "rename":
			if len(match.Path) == 0 {
				return nil, fmt.Errorf("operation rename cannot target the document root")
			}
			if _, ok := match.Path[len(match.Path)-1].(spec.Name); !ok {
				return nil, fmt.Errorf("renaming %s: selected value is not an object member", match.Path)
			}
			parent, err := valueAt(original, match.Path[:len(match.Path)-1])
			if err != nil {
				return nil, err
			}
			object, ok := parent.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("renaming %s: parent is %T, want object", match.Path, parent)
			}
			oldName := string(match.Path[len(match.Path)-1].(spec.Name))
			if _, exists := object[r.config.Name]; exists && oldName != r.config.Name {
				return nil, fmt.Errorf("renaming %s: destination key %q already exists", match.Path, r.config.Name)
			}
			destination := appendPath(match.Path[:len(match.Path)-1], spec.Name(r.config.Name)).Pointer()
			if _, exists := destinations[destination]; exists {
				return nil, fmt.Errorf("renaming %s: destination key %q is selected more than once", match.Path, r.config.Name)
			}
			destinations[destination] = struct{}{}
			m.output = clone(match.Node)
			m.changed = string(match.Path[len(match.Path)-1].(spec.Name)) != r.config.Name
		}
		mutations[i] = m
	}
	return mutations, nil
}

func (r *Runner) replacement(base expressionEnvironment, match *spec.LocatedNode, index int) (any, error) {
	if r.hasValue {
		return clone(r.config.Value), nil
	}
	environment := base
	environment.Current = exprValue(match.Node)
	environment.Path = match.Path.String()
	environment.Index = index
	replacement, err := expr.Run(r.replaceExpr, environment)
	if err != nil {
		return nil, fmt.Errorf("evaluating expr for %s: %w", match.Path, err)
	}
	if err := validateJSON(replacement); err != nil {
		return nil, fmt.Errorf("replacement for %s is not JSON-compatible: %w", match.Path, err)
	}
	return clone(replacement), nil
}

func createPath(path *theoryjsonpath.Path, original any) (spec.NormalizedPath, error) {
	query := path.Query()
	if query.Singular() == nil {
		return nil, fmt.Errorf("missing create requires a singular path")
	}
	selectors := make(spec.NormalizedPath, 0, len(query.Segments()))
	for _, segment := range query.Segments() {
		selector := segment.Selectors()[0]
		switch selector := selector.(type) {
		case spec.Name:
			selectors = append(selectors, selector)
		case spec.Index:
			selectors = append(selectors, selector)
		default:
			return nil, fmt.Errorf("missing create requires a path made of names and indexes")
		}
	}
	if len(selectors) == 0 {
		return nil, fmt.Errorf("missing create cannot target the document root")
	}
	if _, ok := selectors[len(selectors)-1].(spec.Name); !ok {
		return nil, fmt.Errorf("missing create can only create object members")
	}
	current := original
	for i, selector := range selectors {
		switch selector := selector.(type) {
		case spec.Name:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("creating %s: parent is %T, want object", spec.Normalized(selectors[:i]...), current)
			}
			child, exists := object[string(selector)]
			if !exists {
				for _, remaining := range selectors[i:] {
					if _, ok := remaining.(spec.Name); !ok {
						return nil, fmt.Errorf("creating %s: array indexes must already exist", spec.Normalized(selectors...))
					}
				}
				return selectors, nil
			}
			current = child
		case spec.Index:
			array, ok := current.([]any)
			if !ok || int(selector) < 0 || int(selector) >= len(array) {
				return nil, fmt.Errorf("creating %s: array index %d does not exist", spec.Normalized(selectors...), selector)
			}
			current = array[int(selector)]
		}
	}
	return nil, fmt.Errorf("path already exists")
}

func applyMutations(root any, mutations []mutation) (any, error) {
	ordered := append([]mutation(nil), mutations...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return compareMutationPaths(ordered[i].match.Path, ordered[j].match.Path) > 0
	})
	var err error
	for _, mutation := range ordered {
		if !mutation.changed {
			continue
		}
		switch mutation.operation {
		case "set":
			if mutation.created {
				root, err = createMember(root, mutation.match.Path, clone(mutation.replacement))
			} else {
				root, err = assign(root, mutation.match.Path, clone(mutation.replacement))
			}
		case "delete":
			root, err = removeAt(root, mutation.match.Path)
		case "append":
			var current any
			current, err = valueAt(root, mutation.match.Path)
			if err == nil {
				array := current.([]any)
				root, err = assign(root, mutation.match.Path, append(array, clone(mutation.replacement)))
			}
		case "insert":
			root, err = insertAt(root, mutation.match.Path, mutation.position, clone(mutation.replacement))
		case "merge":
			root, err = assign(root, mutation.match.Path, clone(mutation.replacement))
		case "rename":
			root, err = renameAt(root, mutation.match.Path, mutation.name)
		}
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", mutation.operation, mutation.match.Path, err)
		}
	}
	return root, nil
}

func compareMutationPaths(left, right spec.NormalizedPath) int {
	return left.Compare(right)
}

func createMember(root any, path spec.NormalizedPath, value any) (any, error) {
	current := root
	for _, selector := range path[:len(path)-1] {
		switch selector := selector.(type) {
		case spec.Name:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("parent is %T, want object", current)
			}
			child, exists := object[string(selector)]
			if !exists {
				child = map[string]any{}
				object[string(selector)] = child
			}
			current = child
		case spec.Index:
			array, ok := current.([]any)
			if !ok || int(selector) < 0 || int(selector) >= len(array) {
				return nil, fmt.Errorf("array index %d does not exist", selector)
			}
			current = array[int(selector)]
		}
	}
	object, ok := current.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parent is %T, want object", current)
	}
	name := string(path[len(path)-1].(spec.Name))
	object[name] = value
	return root, nil
}

func valueAt(root any, path spec.NormalizedPath) (any, error) {
	current := root
	for _, selector := range path {
		switch selector := selector.(type) {
		case spec.Name:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("parent is %T, want object", current)
			}
			var exists bool
			current, exists = object[string(selector)]
			if !exists {
				return nil, fmt.Errorf("key %q does not exist", selector)
			}
		case spec.Index:
			array, ok := current.([]any)
			if !ok || int(selector) < 0 || int(selector) >= len(array) {
				return nil, fmt.Errorf("array index %d does not exist", selector)
			}
			current = array[int(selector)]
		}
	}
	return current, nil
}

func removeAt(root any, path spec.NormalizedPath) (any, error) {
	parentPath := path[:len(path)-1]
	parent, err := valueAt(root, parentPath)
	if err != nil {
		return nil, err
	}
	switch selector := path[len(path)-1].(type) {
	case spec.Name:
		delete(parent.(map[string]any), string(selector))
	case spec.Index:
		array := parent.([]any)
		array = append(array[:int(selector)], array[int(selector)+1:]...)
		return assign(root, parentPath, array)
	}
	return root, nil
}

func insertAt(root any, path spec.NormalizedPath, position string, value any) (any, error) {
	index := int(path[len(path)-1].(spec.Index))
	if position == "after" {
		index++
	}
	parentPath := path[:len(path)-1]
	parent, err := valueAt(root, parentPath)
	if err != nil {
		return nil, err
	}
	array := parent.([]any)
	array = append(array, nil)
	copy(array[index+1:], array[index:])
	array[index] = value
	return assign(root, parentPath, array)
}

func renameAt(root any, path spec.NormalizedPath, name string) (any, error) {
	parent, err := valueAt(root, path[:len(path)-1])
	if err != nil {
		return nil, err
	}
	object := parent.(map[string]any)
	old := string(path[len(path)-1].(spec.Name))
	if old == name {
		return root, nil
	}
	object[name] = object[old]
	delete(object, old)
	return root, nil
}

func validateArrayElement(root any, path spec.NormalizedPath) error {
	if len(path) == 0 {
		return fmt.Errorf("document root is not an array element")
	}
	if _, ok := path[len(path)-1].(spec.Index); !ok {
		return fmt.Errorf("selected value is not an array element")
	}
	parent, err := valueAt(root, path[:len(path)-1])
	if err != nil {
		return err
	}
	if _, ok := parent.([]any); !ok {
		return fmt.Errorf("parent is %T, want array", parent)
	}
	return nil
}

func deepMerge(current, patch map[string]any) map[string]any {
	result := clone(current).(map[string]any)
	for key, value := range patch {
		left, leftMap := result[key].(map[string]any)
		right, rightMap := value.(map[string]any)
		if leftMap && rightMap {
			result[key] = deepMerge(left, right)
			continue
		}
		result[key] = clone(value)
	}
	return result
}

// sortedKeys orders the members a merge adds so the same merge writes the same
// bytes on every run; Go randomizes map iteration order.
func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func appendPath(path spec.NormalizedPath, selector spec.NormalSelector) spec.NormalizedPath {
	result := append(spec.NormalizedPath(nil), path...)
	return append(result, selector)
}

func mutationMatches(mutations []mutation) []*spec.LocatedNode {
	matches := make([]*spec.LocatedNode, len(mutations))
	for i := range mutations {
		matches[i] = mutations[i].match
	}
	return matches
}

func mutationOutputs(mutations []mutation) []any {
	values := make([]any, 0, len(mutations))
	for _, mutation := range mutations {
		if mutation.operation != "delete" {
			values = append(values, mutation.output)
		}
	}
	return values
}

func mutationChangedCount(mutations []mutation) int {
	count := 0
	for _, mutation := range mutations {
		if mutation.changed {
			count++
		}
	}
	return count
}
