// Package edit implements RFC 9535 JSONPath updates for structured values and files.
package edit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	toml "github.com/pelletier/go-toml/v2"
	theoryjsonpath "github.com/theory/jsonpath"
	"github.com/theory/jsonpath/spec"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"gopkg.in/yaml.v3"
)

const defaultMaxBytes = "1MiB"

type Source struct {
	File string `yaml:"file,omitempty"`
	Var  string `yaml:"var,omitempty"`
	Expr string `yaml:"expr,omitempty"`
}

type Config struct {
	Operation string `yaml:"operation"`
	From      Source `yaml:"from"`
	Path      string `yaml:"path"`
	Value     any    `yaml:"value,omitempty"`
	Expr      string `yaml:"expr,omitempty"`
	Position  string `yaml:"position,omitempty"`
	Name      string `yaml:"name,omitempty"`
	Result    string `yaml:"result,omitempty"`
	Missing   string `yaml:"missing,omitempty"`
	Format    string `yaml:"format,omitempty"`
	MaxBytes  string `yaml:"max_bytes,omitempty"`
}

type expressionEnvironment struct {
	Inputs       map[string]any               `expr:"inputs"`
	Vars         map[string]any               `expr:"vars"`
	Env          map[string]string            `expr:"env"`
	Steps        map[string]any               `expr:"steps"`
	Dependencies map[string]map[string]any    `expr:"dependencies"`
	Batch        map[string]any               `expr:"batch"`
	Foreach      map[string]any               `expr:"foreach"`
	Matrix       map[string]any               `expr:"matrix"`
	Observe      map[string]any               `expr:"observe"`
	Finally      map[string]any               `expr:"finally"`
	Error        map[string]any               `expr:"error"`
	Workflow     step.WorkflowValue           `expr:"workflow"`
	Run          runValue                     `expr:"run"`
	Current      any                          `expr:"current"`
	Path         string                       `expr:"path"`
	Index        int                          `expr:"index"`
	Secret       func(string) (string, error) `expr:"secret"`
}

type runValue struct {
	Dir string `expr:"dir"`
}

type Runner struct {
	config      Config
	hasValue    bool
	path        *theoryjsonpath.Path
	sourceExpr  *vm.Program
	replaceExpr *vm.Program
	maxBytes    int64
}

func Register(registry *step.Registry) error { return registry.Register("edit", New) }

func New(raw map[string]any) (step.Runner, error) {
	config := Config{Result: "one", Missing: "error", MaxBytes: defaultMaxBytes}
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if !oneOf(config.Operation, "set", "delete", "append", "insert", "merge", "rename") {
		return nil, fmt.Errorf("operation must be set, delete, append, insert, merge, or rename")
	}
	sources := 0
	for _, value := range []string{config.From.File, config.From.Var, config.From.Expr} {
		if strings.TrimSpace(value) != "" {
			sources++
		}
	}
	if sources != 1 {
		return nil, fmt.Errorf("from must contain exactly one of file, var, or expr")
	}
	if strings.TrimSpace(config.Path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	_, hasValue := raw["value"]
	_, hasExpr := raw["expr"]
	needsReplacement := oneOf(config.Operation, "set", "append", "insert", "merge")
	if needsReplacement && hasValue == hasExpr {
		return nil, fmt.Errorf("operation %s requires exactly one of value or expr", config.Operation)
	}
	if !needsReplacement && (hasValue || hasExpr) {
		return nil, fmt.Errorf("value and expr are not allowed with operation %s", config.Operation)
	}
	if config.Operation == "insert" {
		if !templated(config.Position) && config.Position != "before" && config.Position != "after" {
			return nil, fmt.Errorf("position must be before or after with operation insert")
		}
	} else if config.Position != "" {
		return nil, fmt.Errorf("position is only allowed with operation insert")
	}
	if config.Operation == "rename" {
		if strings.TrimSpace(config.Name) == "" {
			return nil, fmt.Errorf("name is required with operation rename")
		}
	} else if config.Name != "" {
		return nil, fmt.Errorf("name is only allowed with operation rename")
	}
	if !templated(config.Result) && config.Result != "one" && config.Result != "all" {
		return nil, fmt.Errorf("result must be one or all")
	}
	if !templated(config.Missing) && !oneOf(config.Missing, "error", "ignore", "create") {
		return nil, fmt.Errorf("missing must be error, ignore, or create")
	}
	if config.Missing == "create" && config.Operation != "set" {
		return nil, fmt.Errorf("missing create is only allowed with operation set")
	}
	if config.From.File == "" && config.Format != "" {
		return nil, fmt.Errorf("format is only supported with from.file")
	}
	if config.Format != "" && !templated(config.Format) && config.Format != "json" && config.Format != "yaml" && config.Format != "toml" {
		return nil, fmt.Errorf("format must be json, yaml, or toml")
	}

	runner := &Runner{config: config, hasValue: hasValue}
	if !templated(config.MaxBytes) {
		maximum, err := parseSize(config.MaxBytes)
		if err != nil || maximum <= 0 {
			return nil, fmt.Errorf("max_bytes must be a positive byte size")
		}
		runner.maxBytes = maximum
	}
	if !templated(config.Path) {
		path, err := theoryjsonpath.Parse(config.Path)
		if err != nil {
			return nil, fmt.Errorf("parsing path: %w", err)
		}
		runner.path = path
	}
	if config.From.Expr != "" && !templated(config.From.Expr) {
		program, err := wukoexpr.Compile(config.From.Expr, expr.Env(expressionEnvironment{}))
		if err != nil {
			return nil, fmt.Errorf("compiling from.expr: %w", err)
		}
		runner.sourceExpr = program
	}
	if needsReplacement && hasExpr {
		if strings.TrimSpace(config.Expr) == "" {
			return nil, fmt.Errorf("expr must not be empty")
		}
		if !templated(config.Expr) {
			program, err := wukoexpr.Compile(config.Expr, expr.Env(expressionEnvironment{}))
			if err != nil {
				return nil, fmt.Errorf("compiling expr: %w", err)
			}
			runner.replaceExpr = program
		}
	}
	if hasValue {
		if err := validateJSON(config.Value); err != nil {
			return nil, fmt.Errorf("value is not JSON-compatible: %w", err)
		}
	}
	return runner, nil
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if r.path == nil {
		return step.Result{}, fmt.Errorf("path contains an unresolved template")
	}
	if r.maxBytes == 0 {
		return step.Result{}, fmt.Errorf("max_bytes contains an unresolved template")
	}
	if templated(r.config.Result) || templated(r.config.Missing) || templated(r.config.Format) || templated(r.config.Name) || templated(r.config.Position) {
		return step.Result{}, fmt.Errorf("configuration contains an unresolved template")
	}
	if oneOf(r.config.Operation, "set", "append", "insert", "merge") && !r.hasValue && r.replaceExpr == nil {
		return step.Result{}, fmt.Errorf("expr contains an unresolved template")
	}
	value, file, err := r.resolveSource(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	original := value
	mutations, err := r.planMutations(ctx, request, original)
	if err != nil {
		return step.Result{}, err
	}
	if len(mutations) == 0 {
		return r.result(original, file, nil, nil, 0), nil
	}
	updated, err := applyMutations(clone(original), mutations)
	if err != nil {
		return step.Result{}, err
	}
	matches := mutationMatches(mutations)
	replacements := mutationOutputs(mutations)
	changedCount := mutationChangedCount(mutations)
	if file != nil && changedCount > 0 {
		patched, err := patchMutations(file.data, file.format, original, updated, mutations)
		if err != nil {
			return step.Result{}, fmt.Errorf("editing %s: %w", file.path, err)
		}
		verified, err := decodeDocument(patched, file.format)
		if err != nil {
			return step.Result{}, fmt.Errorf("verifying edited %s: %w", file.path, err)
		}
		if !sameValue(verified, updated) {
			return step.Result{}, fmt.Errorf("verifying edited %s: document differs from requested value", file.path)
		}
		if err := atomicReplace(ctx, file.path, patched, file.mode); err != nil {
			return step.Result{}, err
		}
	}
	return r.result(updated, file, matches, replacements, changedCount), nil
}

type fileSource struct {
	path   string
	format string
	data   []byte
	mode   os.FileMode
}

func (r *Runner) resolveSource(ctx context.Context, request step.Request) (any, *fileSource, error) {
	switch {
	case r.config.From.Var != "":
		value, ok := request.Vars[r.config.From.Var]
		if !ok {
			return nil, nil, fmt.Errorf("variable %q is not defined", r.config.From.Var)
		}
		return clone(value), nil, nil
	case r.config.From.Expr != "":
		if r.sourceExpr == nil {
			return nil, nil, fmt.Errorf("from.expr contains an unresolved template")
		}
		value, err := expr.Run(r.sourceExpr, r.environment(request, nil, "", 0))
		if err != nil {
			return nil, nil, fmt.Errorf("evaluating from.expr: %w", err)
		}
		if err := validateJSON(value); err != nil {
			return nil, nil, fmt.Errorf("from.expr result is not JSON-compatible: %w", err)
		}
		return clone(value), nil, nil
	default:
		path := r.config.From.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(request.RunDir, path)
		}
		path, err := filepath.Abs(path)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving edit file: %w", err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, nil, fmt.Errorf("inspecting edit file %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("edit file %s must be a regular file", path)
		}
		if info.Size() > r.maxBytes {
			return nil, nil, fmt.Errorf("edit file %s exceeds max_bytes", path)
		}
		data, err := readFile(ctx, path, r.maxBytes)
		if err != nil {
			return nil, nil, err
		}
		format, err := inferFormat(path, r.config.Format)
		if err != nil {
			return nil, nil, err
		}
		value, err := decodeDocument(data, format)
		if err != nil {
			return nil, nil, fmt.Errorf("decoding edit file %s as %s: %w", path, format, err)
		}
		return value, &fileSource{path: path, format: format, data: data, mode: info.Mode().Perm()}, nil
	}
}

// baseEnvironment holds the roots shared by every match; only current, path,
// and index change from one match to the next.
func (r *Runner) baseEnvironment(request step.Request) expressionEnvironment {
	return expressionEnvironment{
		Inputs: request.Inputs, Vars: request.Vars, Env: request.Env, Steps: request.Steps,
		Dependencies: request.Dependencies, Batch: binding(request.Bindings, "batch"),
		Foreach: binding(request.Bindings, "foreach"), Matrix: binding(request.Bindings, "matrix"), Observe: binding(request.Bindings, "observe"),
		Finally: binding(request.Bindings, "finally"), Error: binding(request.Bindings, "error"),
		Workflow: request.WorkflowValue(),
		Run:      runValue{Dir: request.RunDir},
		Secret:   request.ResolveSecret,
	}
}

func (r *Runner) environment(request step.Request, current any, path string, index int) expressionEnvironment {
	environment := r.baseEnvironment(request)
	environment.Current = exprValue(current)
	environment.Path = path
	environment.Index = index
	return environment
}

// result takes ownership of value and replacements: both are documents this
// Run built for itself, and assign already copied every replacement it stored
// into value, so the outputs can reference them directly.
func (r *Runner) result(value any, file *fileSource, matches []*spec.LocatedNode, replacements []any, changedCount int) step.Result {
	paths := make([]any, len(matches))
	values := make([]any, len(replacements))
	for i := range matches {
		paths[i] = matches[i].Path.String()
	}
	for i := range replacements {
		values[i] = replacements[i]
	}
	outputs := map[string]any{
		"value": value, "paths": paths, "replacements": values, "count": len(matches),
		"changed": changedCount > 0, "changed_count": changedCount,
	}
	if file != nil {
		outputs["file"] = file.path
		outputs["format"] = file.format
	}
	return step.Result{Outputs: outputs}
}

func uniqueNonOverlapping(matches []*spec.LocatedNode) ([]*spec.LocatedNode, error) {
	seen := make(map[string]struct{}, len(matches))
	result := make([]*spec.LocatedNode, 0, len(matches))
	pointers := make([]string, 0, len(matches))
	for _, match := range matches {
		pointer := match.Path.Pointer()
		if _, ok := seen[pointer]; ok {
			continue
		}
		seen[pointer] = struct{}{}
		result = append(result, match)
		pointers = append(pointers, pointer)
	}
	// Sorting segment-wise puts every location immediately before its own
	// descendants, so comparing neighbours is enough to spot an overlap.
	sort.Slice(pointers, func(i, j int) bool { return pointerLess(pointers[i], pointers[j]) })
	for i := 1; i < len(pointers); i++ {
		if ancestor(pointers[i-1], pointers[i]) {
			return nil, fmt.Errorf("path selects overlapping locations %q and %q", pointers[i-1], pointers[i])
		}
	}
	return result, nil
}

// pointerLess orders JSON pointers one segment at a time. Plain string order
// will not do: "/a." sorts between "/a" and "/a/b" and would hide the overlap
// between them.
func pointerLess(left, right string) bool {
	for left != "" && right != "" {
		leftSegment, leftRest := splitPointer(left)
		rightSegment, rightRest := splitPointer(right)
		if leftSegment != rightSegment {
			return leftSegment < rightSegment
		}
		left, right = leftRest, rightRest
	}
	return left == "" && right != ""
}

// splitPointer separates the leading segment of a JSON pointer, which always
// starts with the separator, from the rest of it.
func splitPointer(pointer string) (string, string) {
	pointer = pointer[1:]
	if end := strings.IndexByte(pointer, '/'); end >= 0 {
		return pointer[:end], pointer[end:]
	}
	return pointer, ""
}

func ancestor(parent, child string) bool {
	return parent != child && (parent == "" || strings.HasPrefix(child, parent+"/"))
}

func assign(root any, path spec.NormalizedPath, value any) (any, error) {
	if len(path) == 0 {
		return value, nil
	}
	current := root
	for _, selector := range path[:len(path)-1] {
		switch selector := selector.(type) {
		case spec.Name:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("parent is %T, want object", current)
			}
			current = object[string(selector)]
		case spec.Index:
			array, ok := current.([]any)
			if !ok {
				return nil, fmt.Errorf("parent is %T, want array", current)
			}
			current = array[int(selector)]
		}
	}
	switch selector := path[len(path)-1].(type) {
	case spec.Name:
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("parent is %T, want object", current)
		}
		object[string(selector)] = value
	case spec.Index:
		array, ok := current.([]any)
		if !ok {
			return nil, fmt.Errorf("parent is %T, want array", current)
		}
		array[int(selector)] = value
	}
	return root, nil
}

func decodeDocument(data []byte, format string) (any, error) {
	var value any
	switch format {
	case "json":
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		var extra any
		switch err := decoder.Decode(&extra); {
		case err == nil:
			return nil, fmt.Errorf("multiple JSON values are not supported")
		case !errors.Is(err, io.EOF):
			// A second value that fails to decode is a syntax error in the document,
			// not a second document; reporting it as one hid the real problem.
			return nil, err
		}
	case "yaml":
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		var extra any
		switch err := decoder.Decode(&extra); {
		case err == nil:
			return nil, fmt.Errorf("multiple YAML documents are not supported")
		case !errors.Is(err, io.EOF):
			return nil, err
		}
	case "toml":
		value = map[string]any{}
		if err := toml.Unmarshal(data, &value); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
	return normalizeStringMaps(value), nil
}

func normalizeStringMaps(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			value[key] = normalizeStringMaps(child)
		}
		return value
	case map[any]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			result[fmt.Sprint(key)] = normalizeStringMaps(child)
		}
		return result
	case []any:
		for i := range value {
			value[i] = normalizeStringMaps(value[i])
		}
		return value
	default:
		return value
	}
}

// sameValue reports whether two JSON-compatible values are equal once numbers
// are compared by value: a decoded document holds json.Number, while a
// configured value or an expression result holds a plain Go number.
//
// Two collections of the same kind are compared entry by entry rather than
// through normalizeNumbers, which reconciles their numbers by marshalling and
// re-decoding whole documents. Verifying an edited file compares two of them,
// so that round trip dominated the cost of a single-key edit.
func sameValue(left, right any) bool {
	switch left := left.(type) {
	case map[string]any:
		if right, ok := right.(map[string]any); ok {
			return sameObject(left, right)
		}
	case []any:
		if right, ok := right.([]any); ok {
			return sameArray(left, right)
		}
	}
	if reflect.DeepEqual(left, right) {
		return true
	}
	leftNumber, leftIsNumber := numberText(left)
	rightNumber, rightIsNumber := numberText(right)
	if leftIsNumber || rightIsNumber {
		return leftIsNumber && rightIsNumber && leftNumber == rightNumber
	}
	return reflect.DeepEqual(normalizeNumbers(left), normalizeNumbers(right))
}

func sameObject(left, right map[string]any) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		other, ok := right[key]
		if !ok || !sameValue(value, other) {
			return false
		}
	}
	return true
}

func sameArray(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !sameValue(left[i], right[i]) {
			return false
		}
	}
	return true
}

// numberText returns the JSON text of a number, the same form normalizeNumbers
// canonicalizes it to, or false when the value is not a number. Comparing that
// text avoids a JSON round trip for the scalar replacements that dominate.
func numberText(value any) (string, bool) {
	switch value := value.(type) {
	case json.Number:
		return value.String(), true
	case int:
		return strconv.Itoa(value), true
	case int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		text, err := json.Marshal(value)
		if err != nil {
			return "", false
		}
		return string(text), true
	default:
		return "", false
	}
}

// exprValue turns decoded json.Number values into the plain numbers expression
// operators understand, so arithmetic on current works for every format.
//
// An integer too large for any Go integer type is left as the json.Number it already
// was. Converting it to a float loses digits, and because a replacement is compared
// against the original to decide whether the file changed, that rounded value counted
// as a change and was written back: an expr of just "current" rewrote the document
// with a truncated number. Passing it through unchanged makes that edit the no-op it
// should be, and leaves arithmetic on such a number to fail rather than quietly round.
func exprValue(value any) any {
	switch value := value.(type) {
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			return integer
		}
		if unsigned, err := strconv.ParseUint(value.String(), 10, 64); err == nil {
			return unsigned
		}
		// Only a genuinely fractional number becomes a float; a plain integer this
		// large would lose its low digits on the way.
		if strings.ContainsAny(value.String(), ".eE") {
			if number, err := value.Float64(); err == nil {
				return number
			}
		}
		return value
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			result[key] = exprValue(child)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, child := range value {
			result[i] = exprValue(child)
		}
		return result
	default:
		return value
	}
}

func normalizeNumbers(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var normalized any
	if decoder.Decode(&normalized) != nil {
		return value
	}
	return normalized
}

func clone(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			result[key] = clone(child)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, child := range value {
			result[i] = clone(child)
		}
		return result
	default:
		return value
	}
}

func validateJSON(value any) error {
	if number, ok := value.(float64); ok && (math.IsNaN(number) || math.IsInf(number, 0)) {
		return fmt.Errorf("non-finite number")
	}
	_, err := json.Marshal(value)
	return err
}

func binding(bindings map[string]any, name string) map[string]any {
	value, _ := bindings[name].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func templated(value string) bool { return strings.Contains(value, "{{") }

func inferFormat(path, configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "json", nil
	case ".yaml", ".yml":
		return "yaml", nil
	case ".toml":
		return "toml", nil
	default:
		return "", fmt.Errorf("cannot infer edit format from %s; set format", path)
	}
}

func parseSize(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	for _, unit := range []struct {
		suffix string
		factor int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}} {
		if !strings.HasSuffix(trimmed, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(trimmed, unit.suffix))
		parsed, err := strconv.ParseInt(number, 10, 64)
		if err != nil || parsed <= 0 || parsed > math.MaxInt64/unit.factor {
			return 0, fmt.Errorf("invalid size")
		}
		return parsed * unit.factor, nil
	}
	return 0, fmt.Errorf("size must use B, KiB, MiB, or GiB")
}

func readFile(ctx context.Context, path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening edit file %s: %w", path, err)
	}
	defer file.Close()
	reader := io.LimitReader(file, maximum+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("reading edit file %s: %w", path, err)
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("edit file %s exceeds max_bytes", path)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func atomicReplace(ctx context.Context, path string, data []byte, mode os.FileMode) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".wuko-edit-*")
	if err != nil {
		return fmt.Errorf("creating temporary file for %s: %w", path, err)
	}
	name := temporary.Name()
	open := true
	defer func() {
		_ = os.Remove(name)
		if open {
			_ = temporary.Close()
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("setting temporary file mode: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("writing temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("syncing temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing temporary file: %w", err)
	}
	open = false
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("installing edited file %s: %w", path, err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("opening edit directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("syncing edit directory: %w", err)
	}
	return nil
}
