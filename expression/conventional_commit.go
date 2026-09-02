package expression

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var defaultConventionalCommitTypes = []string{
	"build", "chore", "ci", "docs", "feat", "fix", "perf", "refactor", "revert", "style", "test",
}

var autosquashPrefixes = []string{"amend! ", "fixup! ", "squash! "}

const verboseCommitMarker = "# ------------------------ >8 ------------------------"

// ConventionalCommitResult is the structured result of inspecting a commit message.
type ConventionalCommitResult struct {
	Valid          bool
	Message        string
	CleanedMessage string
	Classification string
	Type           string
	Scope          string
	Subject        string
	Breaking       bool
	Body           string
	Task           string
}

type conventionalCommitOptions struct {
	types       []string
	scopes      []string
	forceScope  bool
	strict      bool
	taskPattern string
	taskSuffix  *regexp.Regexp
}

type conventionalCommitValidationError struct{ problem string }

func (err *conventionalCommitValidationError) Error() string {
	return "invalid conventional commit: " + err.problem
}

func invalidConventionalCommit(format string, values ...any) error {
	return &conventionalCommitValidationError{problem: fmt.Sprintf(format, values...)}
}

// BuildConventionalCommit builds and validates a Conventional Commit message from a
// language-neutral configuration object.
func BuildConventionalCommit(config map[string]any) (string, error) {
	if config == nil {
		return "", fmt.Errorf("conventional commit configuration must be an object")
	}
	allowed := map[string]struct{}{
		"type": {}, "scope": {}, "subject": {}, "breaking": {}, "body": {},
		"types": {}, "scopes": {}, "force_scope": {}, "task": {}, "task_regex": {},
	}
	if err := rejectUnknownConventionalCommitFields(config, allowed); err != nil {
		return "", err
	}

	typeName, err := conventionalCommitString(config, "type", true)
	if err != nil {
		return "", err
	}
	typeName = strings.ToLower(typeName)
	scope, err := conventionalCommitString(config, "scope", false)
	if err != nil {
		return "", err
	}
	subject, err := conventionalCommitString(config, "subject", true)
	if err != nil {
		return "", err
	}
	body := ""
	if value, present := config["body"]; present {
		var ok bool
		body, ok = value.(string)
		if !ok {
			return "", fmt.Errorf("body must be a string, got %T", value)
		}
	}
	task, hasTask, err := optionalConventionalCommitString(config, "task")
	if err != nil {
		return "", err
	}
	taskPattern, hasTaskPattern, err := optionalConventionalCommitString(config, "task_regex")
	if err != nil {
		return "", err
	}
	if hasTaskPattern && !hasTask {
		return "", fmt.Errorf("task_regex requires task")
	}
	if hasTaskPattern {
		fullTask, err := compileConventionalCommitTaskPattern(taskPattern, true)
		if err != nil {
			return "", err
		}
		if !fullTask.MatchString(task) {
			return "", fmt.Errorf("task %q does not match task_regex", task)
		}
	}
	breaking, err := conventionalCommitBool(config, "breaking")
	if err != nil {
		return "", err
	}

	header := typeName
	if scope != "" {
		header += "(" + scope + ")"
	}
	if breaking {
		header += "!"
	}
	header += ": " + subject
	if hasTask {
		header += " " + task
	}
	body, err = normalizeConventionalCommitBody(body)
	if err != nil {
		return "", err
	}
	message := header
	if body != "" {
		message += "\n\n" + body
	}

	options := conventionalCommitOptionMap(config)
	if hasTaskPattern {
		options["task_regex"] = taskPattern
	}
	if _, err := InspectConventionalCommit(message, options); err != nil {
		return "", err
	}
	return message, nil
}

// InspectConventionalCommit validates and decomposes a commit message. Invalid messages return
// a validation error; malformed options return ordinary configuration errors.
func InspectConventionalCommit(message string, rawOptions map[string]any) (ConventionalCommitResult, error) {
	options, err := parseConventionalCommitOptions(rawOptions)
	if err != nil {
		return ConventionalCommitResult{}, err
	}
	result := ConventionalCommitResult{Message: message, CleanedMessage: cleanConventionalCommit(message)}
	if strings.TrimSpace(result.CleanedMessage) == "" {
		return result, invalidConventionalCommit("message must not be blank")
	}

	header, remainder, hasRemainder := strings.Cut(result.CleanedMessage, "\n")
	if options.taskSuffix != nil {
		location := options.taskSuffix.FindStringSubmatchIndex(header)
		if location == nil {
			return result, invalidConventionalCommit("header must end with a task matching task_regex %q", options.taskPattern)
		}
		result.Task = header[location[2]:location[3]]
		header = strings.TrimRightFunc(header[:location[2]], unicode.IsSpace)
		if header == "" {
			return result, invalidConventionalCommit("task suffix must follow a commit header")
		}
	}

	if !options.strict {
		if isConventionalCommitMerge(header) {
			result.Valid = true
			result.Classification = "merge"
			return result, nil
		}
		if hasConventionalCommitAutosquashPrefix(header) {
			result.Valid = true
			result.Classification = "autosquash"
			return result, nil
		}
	}

	body, err := conventionalCommitBody(remainder, hasRemainder)
	if err != nil {
		return result, err
	}
	typeName, scope, breaking, subject, err := parseConventionalCommitHeader(header, options)
	if err != nil {
		return result, err
	}
	result.Valid = true
	result.Classification = "conventional"
	result.Type = typeName
	result.Scope = scope
	result.Subject = subject
	result.Breaking = breaking
	result.Body = body
	return result, nil
}

// IsConventionalCommit reports whether message is valid. Malformed options are returned as
// errors, while an invalid message is a normal false result.
func IsConventionalCommit(message string, options map[string]any) (bool, error) {
	_, err := InspectConventionalCommit(message, options)
	if err == nil {
		return true, nil
	}
	var validationErr *conventionalCommitValidationError
	if errors.As(err, &validationErr) {
		return false, nil
	}
	return false, err
}

func parseConventionalCommitOptions(raw map[string]any) (conventionalCommitOptions, error) {
	allowed := map[string]struct{}{
		"types": {}, "scopes": {}, "force_scope": {}, "strict": {}, "task_regex": {},
	}
	if err := rejectUnknownConventionalCommitFields(raw, allowed); err != nil {
		return conventionalCommitOptions{}, err
	}
	options := conventionalCommitOptions{types: slices.Clone(defaultConventionalCommitTypes)}
	if raw == nil {
		return options, nil
	}

	if value, present := raw["types"]; present {
		types, err := conventionalCommitStringList(value, "types")
		if err != nil {
			return conventionalCommitOptions{}, err
		}
		if len(types) > 0 {
			options.types = types
			if !slices.Contains(types, "feat") && !slices.Contains(types, "fix") {
				options.types = append(options.types, "feat", "fix")
			}
		}
	}
	if value, present := raw["scopes"]; present {
		scopes, err := conventionalCommitStringList(value, "scopes")
		if err != nil {
			return conventionalCommitOptions{}, err
		}
		options.scopes = scopes
	}
	var err error
	options.forceScope, err = conventionalCommitBool(raw, "force_scope")
	if err != nil {
		return conventionalCommitOptions{}, err
	}
	options.strict, err = conventionalCommitBool(raw, "strict")
	if err != nil {
		return conventionalCommitOptions{}, err
	}
	if taskPattern, present, err := optionalConventionalCommitString(raw, "task_regex"); err != nil {
		return conventionalCommitOptions{}, err
	} else if present {
		options.taskPattern = taskPattern
		options.taskSuffix, err = compileConventionalCommitTaskPattern(taskPattern, false)
		if err != nil {
			return conventionalCommitOptions{}, err
		}
	}
	return options, nil
}

func parseConventionalCommitHeader(header string, options conventionalCommitOptions) (string, string, bool, string, error) {
	delimiter := -1
	depth := 0
	for index, character := range header {
		switch character {
		case '(':
			if depth == 0 {
				depth = 1
			}
		case ')':
			if depth == 1 {
				depth = 0
			}
		case ':':
			if depth == 0 {
				delimiter = index
			}
		}
		if delimiter >= 0 {
			break
		}
	}
	if delimiter < 0 {
		return "", "", false, "", invalidConventionalCommit("expected ':' delimiter after type and optional scope")
	}
	left := header[:delimiter]
	right := header[delimiter+1:]
	if !strings.HasPrefix(right, " ") || strings.TrimSpace(right) == "" {
		return "", "", false, "", invalidConventionalCommit("subject must follow ': ' and must not be blank")
	}
	subject := strings.TrimSpace(right[1:])
	breaking := strings.HasSuffix(left, "!")
	if breaking {
		left = strings.TrimSuffix(left, "!")
	}

	typeName := left
	scope := ""
	if open := strings.IndexByte(left, '('); open >= 0 {
		if !strings.HasSuffix(left, ")") || strings.Contains(left[open+1:len(left)-1], "(") {
			return "", "", false, "", invalidConventionalCommit("scope must be enclosed in one pair of parentheses")
		}
		typeName = left[:open]
		scope = left[open+1 : len(left)-1]
		if strings.TrimSpace(scope) == "" {
			return "", "", false, "", invalidConventionalCommit("scope must not be blank")
		}
	}
	if strings.TrimSpace(typeName) == "" || typeName != strings.TrimSpace(typeName) {
		return "", "", false, "", invalidConventionalCommit("type is missing or contains surrounding whitespace")
	}
	if !containsFold(options.types, typeName) {
		return "", "", false, "", invalidConventionalCommit("type %q is not allowed; expected one of: %s", typeName, strings.Join(options.types, ", "))
	}
	if scope == "" && options.forceScope {
		return "", "", false, "", invalidConventionalCommit("scope is required")
	}
	if scope != "" {
		if err := validateConventionalCommitScope(scope, options.scopes); err != nil {
			return "", "", false, "", err
		}
	}
	return typeName, scope, breaking, subject, nil
}

func validateConventionalCommitScope(scope string, allowed []string) error {
	if len(allowed) == 0 {
		for _, character := range scope {
			if unicode.IsLetter(character) || unicode.IsNumber(character) || character == '_' || unicode.IsSpace(character) ||
				strings.ContainsRune(":,-/.#", character) {
				continue
			}
			return invalidConventionalCommit("scope %q contains unsupported character %q", scope, character)
		}
		return nil
	}

	remaining := strings.TrimSpace(scope)
	for remaining != "" {
		matched := ""
		for _, candidate := range allowed {
			if len(candidate) > len(matched) && len(remaining) >= len(candidate) && strings.EqualFold(remaining[:len(candidate)], candidate) {
				matched = candidate
			}
		}
		if matched == "" {
			return invalidConventionalCommit("scope %q contains a value outside the allowed scopes: %s", scope, strings.Join(allowed, ", "))
		}
		remaining = strings.TrimLeftFunc(remaining[len(matched):], unicode.IsSpace)
		if remaining == "" {
			return nil
		}
		if !strings.ContainsRune(":,-/.#", rune(remaining[0])) {
			return invalidConventionalCommit("scope %q must separate allowed values with ':', ',', '-', '/', '.', or '#'", scope)
		}
		remaining = strings.TrimLeftFunc(remaining[1:], unicode.IsSpace)
		if remaining == "" {
			return invalidConventionalCommit("scope %q ends with a delimiter", scope)
		}
	}
	return nil
}

func conventionalCommitBody(remainder string, present bool) (string, error) {
	if !present || remainder == "" {
		return "", nil
	}
	if !strings.HasPrefix(remainder, "\n") {
		return "", invalidConventionalCommit("body must be separated from the subject by a blank line")
	}
	return strings.Trim(remainder[1:], "\n"), nil
}

func cleanConventionalCommit(message string) string {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	lines := strings.Split(message, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == verboseCommitMarker {
			break
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.Join(cleaned, "\n")
}

// normalizeConventionalCommitBody trims the body and rejects comment lines. Git strips
// "#"-prefixed lines from a commit message, so keeping them would silently drop content
// from the message a workflow ends up committing.
func normalizeConventionalCommitBody(body string) (string, error) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.Trim(body, "\n")
	if strings.TrimSpace(body) == "" {
		return "", nil
	}
	for index, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") {
			return "", fmt.Errorf("body line %d must not start with '#' because git strips comment lines", index+1)
		}
	}
	return body, nil
}

func compileConventionalCommitTaskPattern(pattern string, full bool) (*regexp.Regexp, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("task_regex must not be blank")
	}
	expression := `(?:\A|\s)((?:` + pattern + `))\z`
	if full {
		expression = `\A((?:` + pattern + `))\z`
	}
	compiled, err := regexp.Compile(expression)
	if err != nil {
		return nil, fmt.Errorf("compiling task_regex: %w", err)
	}
	if compiled.MatchString("") {
		return nil, fmt.Errorf("task_regex must not match an empty task")
	}
	return compiled, nil
}

func isConventionalCommitMerge(header string) bool {
	if len(header) < len("merge") || !strings.EqualFold(header[:len("merge")], "merge") {
		return false
	}
	if len(header) == len("merge") {
		return true
	}
	next, size := utf8.DecodeRuneInString(header[len("merge"):])
	if next == utf8.RuneError && size <= 1 {
		return false
	}
	return !unicode.IsLetter(next) && !unicode.IsNumber(next) && next != '_'
}

func hasConventionalCommitAutosquashPrefix(header string) bool {
	return slices.ContainsFunc(autosquashPrefixes, func(prefix string) bool { return strings.HasPrefix(header, prefix) })
}

func rejectUnknownConventionalCommitFields(values map[string]any, allowed map[string]struct{}) error {
	for name := range values {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unknown conventional commit option %q", name)
		}
	}
	return nil
}

func conventionalCommitString(values map[string]any, name string, required bool) (string, error) {
	value, present, err := optionalConventionalCommitString(values, name)
	if err != nil {
		return "", err
	}
	if !present {
		if required {
			return "", fmt.Errorf("%s is required", name)
		}
		return "", nil
	}
	return value, nil
}

func optionalConventionalCommitString(values map[string]any, name string) (string, bool, error) {
	value, present := values[name]
	if !present {
		return "", false, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", true, fmt.Errorf("%s must be a string, got %T", name, value)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", true, fmt.Errorf("%s must not be blank", name)
	}
	return text, true, nil
}

func conventionalCommitBool(values map[string]any, name string) (bool, error) {
	value, present := values[name]
	if !present {
		return false, nil
	}
	boolean, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean, got %T", name, value)
	}
	return boolean, nil
}

func conventionalCommitStringList(value any, name string) ([]string, error) {
	items := reflect.ValueOf(value)
	if !items.IsValid() || items.Kind() != reflect.Array && items.Kind() != reflect.Slice {
		return nil, fmt.Errorf("%s must be a list of strings, got %T", name, value)
	}
	result := make([]string, items.Len())
	for index := range items.Len() {
		item := items.Index(index)
		if item.Kind() == reflect.Interface {
			if item.IsNil() {
				return nil, fmt.Errorf("%s item %d is <nil>, want string", name, index)
			}
			item = item.Elem()
		}
		if item.Kind() != reflect.String {
			return nil, fmt.Errorf("%s item %d is %s, want string", name, index, item.Kind())
		}
		result[index] = strings.TrimSpace(item.String())
		if result[index] == "" {
			return nil, fmt.Errorf("%s item %d must not be blank", name, index)
		}
	}
	return result, nil
}

func conventionalCommitOptionMap(config map[string]any) map[string]any {
	result := make(map[string]any, 4)
	for _, name := range []string{"types", "scopes", "force_scope", "task_regex"} {
		if value, present := config[name]; present {
			result[name] = value
		}
	}
	return result
}

func containsFold(values []string, wanted string) bool {
	return slices.ContainsFunc(values, func(value string) bool { return strings.EqualFold(value, wanted) })
}

func templateIsConventionalCommit(values ...any) (bool, error) {
	if len(values) < 1 || len(values) > 2 {
		return false, fmt.Errorf("isConventionalCommit expects a message and optional options object")
	}
	var message string
	messageSet := false
	var options map[string]any
	for _, candidate := range values {
		switch typed := candidate.(type) {
		case string:
			if messageSet {
				return false, fmt.Errorf("isConventionalCommit expects one message string")
			}
			message = typed
			messageSet = true
		case map[string]any:
			if options != nil {
				return false, fmt.Errorf("isConventionalCommit expects one options object")
			}
			options = typed
		case nil:
			if len(values) == 1 {
				return false, fmt.Errorf("isConventionalCommit expects a message string")
			}
		default:
			return false, fmt.Errorf("isConventionalCommit argument must be a message string or options object, got %T", candidate)
		}
	}
	if !messageSet {
		return false, fmt.Errorf("isConventionalCommit expects a message string")
	}
	return IsConventionalCommit(message, options)
}

func exprIsConventionalCommit(values ...any) (any, error) {
	if len(values) < 1 || len(values) > 2 {
		return nil, fmt.Errorf("isConventionalCommit expects a message and optional options object")
	}
	message, ok := values[0].(string)
	if !ok {
		return nil, fmt.Errorf("isConventionalCommit message must be a string, got %T", values[0])
	}
	var options map[string]any
	if len(values) == 2 {
		var ok bool
		options, ok = values[1].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("isConventionalCommit options must be an object, got %T", values[1])
		}
	}
	return IsConventionalCommit(message, options)
}
