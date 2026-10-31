package workflow

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// stepBindingRules describes where a built-in step departs from the generic binding grammar.
type stepBindingRules struct {
	// owned matches configuration with a step-specific expression grammar or a later
	// evaluation scope, which generic field bindings must not consume.
	owned func(path []string) bool
	// static matches selectors that determine workflow structure, variable declarations,
	// or runner capabilities before execution, so they reject expression bindings.
	static func(path []string) bool
}

// builtinBindingRules lists every built-in step that accepts host-owned bindings. It is
// consulted while loading dependency references, before a runtime registry exists; the
// built-in registration contract test keeps its keys aligned with the registry.
var builtinBindingRules = map[string]stepBindingRules{
	"assert":  {owned: under("message")},
	"cache":   {},
	"changed": {},
	"dashboard": {
		owned: func(path []string) bool {
			return slices.ContainsFunc(path, func(component string) bool {
				return component == "refresh" || component == "actions" || component == "template" || component == "confirm"
			})
		},
		static: func(path []string) bool {
			if len(path) == 1 {
				return path[0] == "groups"
			}
			// Group and widget IDs: groups.N.id and groups.N.widgets.N.id.
			return path[0] == "groups" && path[len(path)-1] == "id" && (len(path) == 3 || (len(path) == 5 && path[2] == "widgets"))
		},
	},
	"decode":                  {},
	"devenv_task":             {},
	"docker":                  {static: exactly("cleanup")},
	"edit":                    {owned: under("from")},
	"extract":                 {},
	"file":                    {},
	"git_branch":              {},
	"git_branch_name":         {},
	"git_clean":               {},
	"git_commit":              {},
	"git_conventional_commit": {},
	"git_diff":                {owned: under("files", "size_exempt")},
	"git_diff_check":          {owned: under("files", "size_exempt")},
	"git_files":               {owned: under("files", "size_exempt")},
	"git_log":                 {},
	"git_merge":               {},
	"git_merge_base":          {},
	"git_on_branch":           {},
	"git_rebase":              {},
	"git_remote_branch":       {},
	"git_revision":            {},
	"git_squash":              {},
	"git_worktree":            {},
	"github_actions":          {},
	"github_pr":               {},
	"github_release":          {},
	"glob":                    {owned: under("patterns")},
	"http":                    {},
	"import_vars":             {},
	"jsonpath":                {},
	"key_value":               {},
	"log_wait":                {},
	"lua":                     {owned: under("source", "args")},
	"markdown_edit": {
		owned: func(path []string) bool {
			return path[0] == "edits" || (path[0] == "from" && len(path) > 1 && path[1] == "expr")
		},
		static: exactly("edits"),
	},
	"multiplexer":  {},
	"process":      {owned: under("argv", "env")},
	"process_call": {owned: under("pool")},
	"require_tool": {},
	"run_workflow": {owned: under("vars"), static: exactly("workflow", "target")},
	"scaffold":     {},
	"semver":       {},
	"set":          {owned: under("variables")},
	"shell": {
		owned:  under("argv", "sdk_args", "files", "interactions", "env"),
		static: exactly("replace", "sdk"),
	},
	"tcp_probe":   {},
	"temp":        {},
	"template":    {owned: under("source", "data")},
	"text_check":  {owned: under("files", "size_exempt")},
	"text_fix":    {owned: under("files", "size_exempt")},
	"time":        {},
	"transform":   {owned: under("from", "operations")},
	"tui_choice":  {},
	"tui_confirm": {},
	"tui_date":    {},
	"tui_form": {
		// Field values and validation resolve in the form's own scope: fields.N.value.
		owned: func(path []string) bool {
			return path[0] == "fields" && len(path) >= 3 && slices.Contains([]string{"value", "default", "validation", "from"}, path[2])
		},
		static: func(path []string) bool {
			return (len(path) == 1 && path[0] == "fields") ||
				(len(path) == 3 && path[0] == "fields" && (path[2] == "id" || path[2] == "type"))
		},
	},
	"tui_input":    {},
	"tui_password": {},
	"tui_path":     {},
	"tui_review":   {},
	"tui_table":    {},
	"watch":        {},
}

// under matches a top-level field and everything beneath it.
func under(fields ...string) func([]string) bool {
	return func(path []string) bool { return slices.Contains(fields, path[0]) }
}

// exactly matches only the top-level fields themselves.
func exactly(fields ...string) func([]string) bool {
	return func(path []string) bool { return len(path) == 1 && slices.Contains(fields, path[0]) }
}

// BuiltinStepBindings reports whether a step type uses the built-in binding grammar.
// External configurations retain their own grammar.
func BuiltinStepBindings(stepType string) bool {
	_, ok := builtinBindingRules[stepType]
	return ok
}

// Binding is a typed configuration value written as {expr: ...} or {literal: ...}.
type Binding struct {
	// Expr is the expression source; empty for a literal or a non-string expr.
	Expr string
	// Literal holds the value of a literal binding.
	Literal   any
	IsLiteral bool
}

// ParseBinding reports whether value is a single-key expr or literal map. Any such map at a
// binding position is a binding, including inside free-form data such as an HTTP body;
// {literal: ...} sends one of these maps as data.
func ParseBinding(value any) (Binding, bool) {
	typed, ok := value.(map[string]any)
	if !ok || len(typed) != 1 {
		return Binding{}, false
	}
	if literal, ok := typed["literal"]; ok {
		return Binding{Literal: literal, IsLiteral: true}, true
	}
	if source, ok := typed["expr"]; ok {
		text, _ := source.(string)
		return Binding{Expr: text}, true
	}
	return Binding{}, false
}

// OwnedBinding returns a binding inside configuration the step owns that the step evaluates
// itself. An assertion message is evaluated only when the assertion fails, so the host
// validates it and passes it through unchanged.
func OwnedBinding(stepType string, path []string, value any) (Binding, bool) {
	if stepType != "assert" || len(path) != 1 || path[0] != "message" {
		return Binding{}, false
	}
	return ParseBinding(value)
}

// HasStepBindings reports bindings in a native step's configuration, evaluated by the host
// or, for OwnedBinding, by the step.
func HasStepBindings(stepType string, raw map[string]any) bool {
	var visit func(any, []string) bool
	visit = func(value any, path []string) bool {
		if StepOwnsBindings(stepType, path) {
			_, owned := OwnedBinding(stepType, path, value)
			return owned
		}
		if _, ok := ParseBinding(value); ok && len(path) > 0 {
			return true
		}
		switch value := value.(type) {
		case map[string]any:
			for name, child := range value {
				if visit(child, append(slices.Clone(path), name)) {
					return true
				}
			}
		case []any:
			for _, child := range value {
				if visit(child, append(slices.Clone(path), "*")) {
					return true
				}
			}
		}
		return false
	}
	return visit(raw, nil)
}

// StepConfigVisitor handles the leaves of a built-in step configuration.
type StepConfigVisitor struct {
	// Owned receives, whole, a subtree the step owns (see StepOwnsBindings).
	Owned func(value any, path []string) (any, error)
	// Binding receives each host-owned {expr} or {literal} binding below the root.
	Binding func(binding Binding, path []string) (any, error)
	// Text receives every other string.
	Text func(text string, path []string) (any, error)
}

// TransformStepConfig rebuilds a built-in step configuration in its binding grammar,
// replacing owned subtrees, bindings, and strings with the visitor's results. Other
// scalars are kept. Fields are visited in sorted order, so the first error is stable.
func TransformStepConfig(stepType string, value any, visitor StepConfigVisitor) (any, error) {
	return transformStepConfig(stepType, value, nil, visitor)
}

func transformStepConfig(stepType string, value any, path []string, visitor StepConfigVisitor) (any, error) {
	if StepOwnsBindings(stepType, path) {
		return visitor.Owned(value, path)
	}
	if binding, ok := ParseBinding(value); ok && len(path) > 0 {
		return visitor.Binding(binding, path)
	}
	switch typed := value.(type) {
	case string:
		return visitor.Text(typed, path)
	case []any:
		result := make([]any, len(typed))
		for i, child := range typed {
			resolved, err := transformStepConfig(stepType, child, append(slices.Clone(path), strconv.Itoa(i)), visitor)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			result[i] = resolved
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for _, name := range slices.Sorted(maps.Keys(typed)) {
			resolved, err := transformStepConfig(stepType, typed[name], append(slices.Clone(path), name), visitor)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", name, err)
			}
			result[name] = resolved
		}
		return result, nil
	default:
		return value, nil
	}
}

// StepOwnsBindings identifies configuration that already has a step-specific expression
// grammar or a later evaluation scope. Generic field bindings must not consume it.
func StepOwnsBindings(stepType string, path []string) bool {
	if len(path) == 0 {
		return false
	}
	if field := path[0]; field == "expr" || strings.HasSuffix(field, "_expr") {
		return true
	}
	owned := builtinBindingRules[stepType].owned
	return owned != nil && owned(path)
}

// StaticStepBindingField names selectors whose values determine workflow structure,
// variable declarations, or runner capabilities before execution.
func StaticStepBindingField(stepType string, path []string) bool {
	if len(path) == 0 {
		return false
	}
	if len(path) == 1 && (path[0] == "variable" || path[0] == "operation") {
		return true
	}
	static := builtinBindingRules[stepType].static
	return static != nil && static(path)
}
