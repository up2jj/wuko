package workflow

import (
	"slices"
	"strings"
)

// BuiltinStepBindings identifies built-in syntax while loading dependency references,
// before a runtime registry exists. External configurations retain their own grammar.
// The built-in registration contract test keeps this list aligned with the registry.
func BuiltinStepBindings(stepType string) bool {
	switch stepType {
	case "assert", "cache", "changed", "dashboard", "decode", "devenv_task", "docker", "edit", "extract", "file",
		"git_branch", "git_branch_name", "git_clean", "git_commit", "git_conventional_commit", "git_diff", "git_diff_check",
		"git_files", "git_log", "git_merge", "git_merge_base", "git_on_branch", "git_rebase", "git_remote_branch",
		"git_revision", "git_squash", "git_worktree", "github_actions", "github_pr", "github_release", "glob", "http",
		"import_vars", "jsonpath", "key_value", "log_wait", "lua", "markdown_edit", "multiplexer", "process", "process_call",
		"require_tool", "run_workflow", "scaffold", "semver", "set", "shell", "tcp_probe", "temp", "template",
		"text_check", "text_fix", "time", "transform", "tui_choice", "tui_confirm", "tui_date", "tui_form", "tui_input",
		"tui_password", "tui_path", "tui_review", "tui_table", "watch":
		return true
	}
	return false
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

// StepOwnsBindings identifies configuration that already has a step-specific expression
// grammar or a later evaluation scope. Generic field bindings must not consume it.
func StepOwnsBindings(stepType string, path []string) bool {
	if len(path) == 0 {
		return false
	}
	field := path[0]
	if field == "expr" || strings.HasSuffix(field, "_expr") {
		return true
	}
	switch stepType {
	case "shell":
		return field == "argv" || field == "sdk_args" || field == "files" || field == "interactions" || field == "env"
	case "process":
		return field == "argv" || field == "env"
	case "process_call":
		return field == "pool"
	case "template":
		return field == "source" || field == "data"
	case "lua":
		return field == "source" || field == "args"
	case "assert":
		return field == "message"
	case "set":
		return field == "variables"
	case "run_workflow":
		return field == "vars"
	case "transform":
		return field == "from" || field == "operations"
	case "edit":
		return field == "from"
	case "markdown_edit":
		return field == "edits" || (field == "from" && len(path) > 1 && path[1] == "expr")
	case "tui_form":
		if field == "fields" && len(path) >= 3 {
			return path[2] == "value" || path[2] == "default" || path[2] == "validation" || path[2] == "from"
		}
	case "dashboard":
		for _, component := range path {
			if component == "refresh" || component == "actions" || component == "template" || component == "confirm" {
				return true
			}
		}
	case "glob":
		return field == "patterns"
	case "git_files", "git_diff", "git_diff_check", "text_fix", "text_check":
		return field == "files" || field == "size_exempt"
	}
	return false
}

// StaticStepBindingField names selectors whose values determine workflow structure,
// variable declarations, or runner capabilities before execution.
func StaticStepBindingField(stepType string, path []string) bool {
	if stepType == "tui_form" && len(path) == 3 && path[0] == "fields" {
		return path[2] == "id" || path[2] == "type"
	}
	if stepType == "dashboard" && len(path) > 0 && path[len(path)-1] == "id" {
		return (len(path) == 3 && path[0] == "groups") || (len(path) == 5 && path[0] == "groups" && path[2] == "widgets")
	}
	if len(path) != 1 {
		return false
	}
	field := path[0]
	if field == "variable" || field == "operation" {
		return true
	}
	switch stepType {
	case "run_workflow":
		return field == "workflow" || field == "target"
	case "shell":
		return field == "replace" || field == "sdk"
	case "tui_form":
		return field == "fields"
	case "dashboard":
		return field == "groups"
	case "markdown_edit":
		return field == "edits"
	case "docker":
		return field == "cleanup"
	}
	return false
}
