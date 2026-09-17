package validation

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

func UnknownField(path Path, name string, allowed []string) Issue {
	return Issue{Code: CodeUnknownField, Path: path, Message: fmt.Sprintf("unknown field %q", name), Hint: suggestionHint(name, allowed)}
}

// UnknownFieldIn reports an unknown field in a named declaration while
// retaining the legacy domain wording.
func UnknownFieldIn(path Path, name string, allowed []string, declaration string) Issue {
	issue := UnknownField(path, name, allowed)
	issue.Message = fmt.Sprintf("field %s not found in %s group", name, declaration)
	return issue
}

func MissingField(path Path, name string) Issue {
	return Issue{Code: CodeMissingField, Path: path.Field(name), Message: fmt.Sprintf("missing required field %q", name)}
}

func Conflict(path Path, left, right string) Issue {
	return Issue{Code: CodeConflict, Path: path, Message: fmt.Sprintf("%q conflicts with %q", left, right), Hint: "remove one of the conflicting fields"}
}

func InvalidType(path Path, expected, actual string) Issue {
	return Issue{Code: CodeInvalidType, Path: path, Message: fmt.Sprintf("expected %s, got %s", expected, actual)}
}

func InvalidValue(path Path, value string, allowed []string) Issue {
	message := fmt.Sprintf("invalid value %q", value)
	if len(allowed) != 0 {
		message += "; expected one of " + quotedList(allowed)
	}
	return Issue{Code: CodeInvalidValue, Path: path, Message: message, Hint: suggestionHint(value, allowed)}
}

func UnavailableReference(path Path, kind, name string, available []string) Issue {
	code := CodeUnavailableReference
	message := fmt.Sprintf("unknown %s %q", kind, name)
	switch kind {
	case "step":
		code = CodeUnknownStep
		message = fmt.Sprintf("step %q is not available here", name)
	case "variable":
		code = CodeUnknownVariable
		message = fmt.Sprintf("variable %q is not declared", name)
	case "input":
		message = fmt.Sprintf("input %q is not declared", name)
	case "environment value":
		message = fmt.Sprintf("environment value %q is not available", name)
	case "dependency":
		code = CodeUnknownDependency
		message = fmt.Sprintf("dependency alias %q is not declared", name)
	case "output":
		code = CodeUnknownOutput
		message = fmt.Sprintf("dependency output %q is not declared", name)
	}
	return Issue{Code: code, Path: path, Message: message, Hint: suggestionHint(name, available)}
}

// UnavailableField reports a missing field inside a known closed object.
func UnavailableField(path Path, container, name string, available []string) Issue {
	return Issue{Code: CodeUnavailableReference, Path: path, Message: fmt.Sprintf("field %q is not available in %s", name, container), Hint: suggestionHint(name, available)}
}

func UnknownStepOutput(path Path, step, output string, available []string) Issue {
	return Issue{Code: CodeUnknownStepOutput, Path: path, Message: fmt.Sprintf("step %q has no output %q", step, output), Hint: suggestionHint(output, available)}
}

func DuplicateDeclaration(path Path, kind, name string, previous Span) Issue {
	issue := Issue{Code: CodeDuplicateDeclaration, Path: path, Message: fmt.Sprintf("duplicate %s %q", kind, name)}
	if previous.Source != "" || previous.Line > 0 {
		issue.Related = []Related{{Message: "first declared here", Span: previous}}
	}
	return issue
}

func suggestionHint(value string, allowed []string) string {
	if suggestion := Suggest(value, allowed); suggestion != "" {
		return fmt.Sprintf("did you mean %q?", suggestion)
	}
	return ""
}

func quotedList(values []string) string {
	values = append([]string(nil), values...)
	slices.Sort(values)
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = fmt.Sprintf("%q", value)
	}
	return strings.Join(quoted, ", ")
}

// Suggest returns the closest plausible candidate using a deterministic edit
// distance. It intentionally declines weak guesses.
func Suggest(value string, candidates []string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	best, bestDistance := "", int(^uint(0)>>1)
	ordered := append([]string(nil), candidates...)
	slices.Sort(ordered)
	for _, candidate := range ordered {
		distance := levenshtein(value, strings.ToLower(candidate))
		if distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	limit := max(1, utf8.RuneCountInString(value)/3)
	if bestDistance > limit {
		return ""
	}
	return best
}

func levenshtein(left, right string) int {
	a, b := []rune(left), []rune(right)
	previous := make([]int, len(b)+1)
	for index := range previous {
		previous[index] = index
	}
	for row, leftRune := range a {
		current := make([]int, len(b)+1)
		current[0] = row + 1
		for column, rightRune := range b {
			cost := 0
			if leftRune != rightRune {
				cost = 1
			}
			current[column+1] = min(current[column]+1, previous[column+1]+1, previous[column]+cost)
		}
		previous = current
	}
	return previous[len(b)]
}
