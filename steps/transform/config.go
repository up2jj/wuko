// Package transform implements deterministic collection transformation pipelines.
package transform

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/types"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"gopkg.in/yaml.v3"
)

type valueKind uint8

const (
	kindUnknown valueKind = iota
	kindList
	kindObject
	kindScalar
	kindRecord
)

type kindSet uint8

const (
	acceptList kindSet = 1 << iota
	acceptObject
)

type descriptor struct {
	accept kindSet
	result valueKind
}

var descriptors = buildDescriptors()

func buildDescriptors() map[string]descriptor {
	result := make(map[string]descriptor)
	add := func(accept kindSet, output valueKind, names ...string) {
		for _, name := range names {
			result[name] = descriptor{accept: accept, result: output}
		}
	}
	add(acceptList, kindList,
		"filter", "map", "flat_map", "filter_map", "reject", "reject_map",
		"unique", "unique_by", "unique_map", "find_uniques", "find_uniques_by", "find_duplicates", "find_duplicates_by",
		"partition_by", "chunk", "flatten", "concat", "window", "sliding", "interleave", "fill",
		"drop", "drop_right", "drop_while", "drop_right_while", "drop_by_index", "take", "take_while", "take_filter", "subset", "slice",
		"replace", "replace_all", "clone", "compact", "splice", "trim", "trim_left", "trim_prefix", "trim_right", "trim_suffix",
		"union", "intersect", "intersect_by", "without", "without_by", "without_nth", "sort", "sort_by", "mode",
		"zip", "zip_with", "unzip", "unzip_with", "cross_join", "cross_join_with",
	)
	add(acceptList, kindObject, "group_by", "group_by_map", "key_by", "associate", "filter_to_map", "keyify", "count_values", "count_values_by")
	add(acceptList, kindScalar,
		"contains", "contains_by", "every", "every_by", "some", "some_by", "none", "none_by", "elements_match", "elements_match_by",
		"index_of", "last_index_of", "has_prefix", "has_suffix", "find_or_else", "first_or", "first_or_empty", "last_or", "last_or_empty",
		"nth", "nth_or", "nth_or_empty", "min", "min_by", "max", "max_by", "is_sorted", "is_sorted_by",
		"reduce", "reduce_right", "count", "count_by", "sum", "sum_by", "product", "product_by", "mean", "mean_by",
	)
	add(acceptList, kindRecord, "filter_reject", "difference", "find", "find_index", "find_last_index", "first", "last", "min_index", "min_index_by", "max_index", "max_index_by", "cut", "cut_prefix", "cut_suffix")

	add(acceptObject, kindList, "keys", "unique_keys", "values", "unique_values", "entries", "chunk_entries", "map_to_list", "filter_map_to_list", "filter_keys", "filter_values")
	add(acceptObject, kindObject, "pick", "pick_keys", "pick_values", "omit", "omit_keys", "omit_values", "invert", "assign", "map_keys", "map_values", "map_entries")
	add(acceptObject, kindScalar, "has_key", "value_or")
	add(acceptObject, kindRecord, "find_key", "find_key_by")
	add(acceptList, kindObject, "from_entries")
	return result
}

// OperationNames returns the stable operation manifest in lexical order.
func OperationNames() []string { return slices.Sorted(maps.Keys(descriptors)) }

type source struct {
	path string
	expr string
}

func (source *source) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag != "!!str" || strings.TrimSpace(node.Value) == "" {
			return fmt.Errorf("from must be a dotted path or an object containing expr")
		}
		source.path = node.Value
		return nil
	case yaml.MappingNode:
		if len(node.Content) != 2 || node.Content[0].Value != "expr" || node.Content[1].Tag != "!!str" || strings.TrimSpace(node.Content[1].Value) == "" {
			return fmt.Errorf("from expression must contain exactly one non-empty expr field")
		}
		source.expr = node.Content[1].Value
		return nil
	default:
		return fmt.Errorf("from must be a dotted path or an object containing expr")
	}
}

type config struct {
	From       source `yaml:"from"`
	Operations []any  `yaml:"operations"`
	Variable   string `yaml:"variable,omitempty"`
}

type compiledExpression struct {
	label   string
	source  string
	program *vm.Program
	locals  []string
}

type operand struct {
	literal    any
	expression *compiledExpression
}

type operation struct {
	name        string
	descriptor  descriptor
	argument    any
	expressions map[string]*compiledExpression
	operands    map[string]operand
	operandList []operand
}

type Runner struct {
	config        config
	sourceProgram *vm.Program
	operations    []operation
}

// ExpressionReference describes one transform expression and the callback-local
// names that should be excluded from workflow reference validation.
type ExpressionReference struct {
	Label  string
	Source string
	Locals []string
}

// References decodes a transform configuration into its dotted source and Expr
// references. The engine uses this to validate workflow data dependencies without
// duplicating transform's operation grammar.
func References(raw map[string]any) (string, []ExpressionReference, error) {
	var config config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return "", nil, err
	}
	references := make([]ExpressionReference, 0)
	if config.From.expr != "" {
		references = append(references, ExpressionReference{Label: "from.expr", Source: config.From.expr})
	}
	for index, rawOperation := range config.Operations {
		operation, err := parseOperation(index, rawOperation)
		if err != nil {
			return "", nil, err
		}
		for _, name := range slices.Sorted(maps.Keys(operation.expressions)) {
			expression := operation.expressions[name]
			references = append(references, ExpressionReference{
				Label:  fmt.Sprintf("operation %d (%s) %s", index+1, operation.name, name),
				Source: expression.source,
				Locals: slices.Clone(expression.locals),
			})
		}
		for _, name := range slices.Sorted(maps.Keys(operation.operands)) {
			if expression := operation.operands[name].expression; expression != nil {
				references = append(references, ExpressionReference{
					Label:  fmt.Sprintf("operation %d (%s) %s", index+1, operation.name, expression.label),
					Source: expression.source,
				})
			}
		}
		for operandIndex, operand := range operation.operandList {
			if operand.expression != nil {
				references = append(references, ExpressionReference{
					Label:  fmt.Sprintf("operation %d (%s) with[%d]", index+1, operation.name, operandIndex),
					Source: operand.expression.source,
				})
			}
		}
	}
	return config.From.path, references, nil
}

func Register(registry *step.Registry) error {
	return registry.RegisterDefinition("transform", step.Registration{
		Builder: New,
		Outputs: step.ClosedObject(map[string]step.OutputSchema{"value": step.OpenObject()}),
	})
}

func New(raw map[string]any) (step.Runner, error) {
	var config config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if config.From.path == "" && config.From.expr == "" {
		return nil, fmt.Errorf("from is required")
	}
	if config.From.path != "" && !validSourcePath(config.From.path) {
		return nil, fmt.Errorf("from path must start with steps. or vars.")
	}
	if len(config.Operations) == 0 {
		return nil, fmt.Errorf("operations must contain at least one operation")
	}
	if _, declared := raw["variable"]; declared && strings.TrimSpace(config.Variable) == "" {
		return nil, fmt.Errorf("variable must not be empty")
	}

	runner := &Runner{config: config}
	if config.From.expr != "" && !templated(config.From.expr) {
		program, err := compile(config.From.expr, nil, false)
		if err != nil {
			return nil, fmt.Errorf("compiling from.expr: %w", err)
		}
		runner.sourceProgram = program
	}

	known := kindUnknown
	for index, rawOperation := range config.Operations {
		parsed, err := parseOperation(index, rawOperation)
		if err != nil {
			return nil, err
		}
		if known == kindScalar || known == kindRecord {
			return nil, fmt.Errorf("operation %d (%s) follows terminal operation %d", index+1, parsed.name, index)
		}
		if known != kindUnknown && !parsed.accepts(known) {
			return nil, fmt.Errorf("operation %d (%s) cannot consume %s produced by the previous operation", index+1, parsed.name, known)
		}
		known = parsed.descriptor.result
		runner.operations = append(runner.operations, parsed)
	}
	return runner, nil
}

func (kind valueKind) String() string {
	switch kind {
	case kindList:
		return "list"
	case kindObject:
		return "object"
	case kindScalar:
		return "scalar"
	case kindRecord:
		return "record"
	default:
		return "unknown value"
	}
}

func (operation operation) accepts(kind valueKind) bool {
	switch kind {
	case kindList:
		return operation.descriptor.accept&acceptList != 0
	case kindObject:
		return operation.descriptor.accept&acceptObject != 0
	default:
		return false
	}
}

func compile(source string, locals map[string]any, boolean bool) (*vm.Program, error) {
	shape := step.ExpressionEnvironmentShape(locals)
	environment := make(types.Map, len(shape))
	for name, value := range shape {
		if value == nil {
			environment[name] = types.Any
			continue
		}
		environment[name] = types.TypeOf(value)
	}
	options := []expr.Option{expr.Env(environment), expr.AllowUndefinedVariables()}
	if boolean {
		options = append(options, expr.AsBool())
	}
	return wukoexpr.Compile(source, options...)
}

func templated(value string) bool { return strings.Contains(value, "{{") }

func validSourcePath(path string) bool {
	for _, prefix := range []string{"steps.", "vars."} {
		if strings.HasPrefix(path, prefix) && len(path) > len(prefix) {
			return true
		}
	}
	return false
}
