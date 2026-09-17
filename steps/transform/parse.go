package transform

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

var zeroArgumentOperations = stringSet(
	"unique", "find_uniques", "find_duplicates", "flatten", "clone", "compact",
	"first", "first_or_empty", "last", "last_or_empty", "min", "min_index", "max", "max_index", "is_sorted",
	"sum", "product", "mean", "mode", "keyify", "keys", "unique_keys", "values", "unique_values",
	"count_values", "entries", "from_entries", "invert", "unzip",
)

var itemExpressionOperations = stringSet(
	"map", "flat_map", "unique_by", "unique_map", "find_uniques_by", "find_duplicates_by",
	"group_by", "partition_by", "key_by", "count_values_by", "is_sorted_by", "sum_by", "product_by", "mean_by",
	"unzip_with",
)

var itemPredicateOperations = stringSet(
	"filter", "reject", "drop_while", "drop_right_while", "take_while", "contains_by", "every_by", "some_by", "none_by",
	"count_by", "find", "find_index", "find_last_index",
)

var comparatorOperations = stringSet("min_by", "min_index_by", "max_by", "max_index_by")
var objectPredicateOperations = stringSet("pick", "omit", "filter_keys", "filter_values", "find_key_by")
var objectExpressionOperations = stringSet("map_values", "map_to_list")

var operandOperations = stringSet(
	"fill", "drop", "drop_right", "drop_by_index", "take", "chunk", "window", "contains", "every", "some", "none",
	"index_of", "last_index_of", "first_or", "last_or", "nth", "nth_or_empty", "count", "without_nth", "find_key",
	"has_key", "chunk_entries", "pick_keys", "pick_values", "omit_keys", "omit_values",
)

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func parseOperation(index int, raw any) (operation, error) {
	name, argument, err := operationEntry(raw)
	if err != nil {
		return operation{}, fmt.Errorf("operation %d: %w", index+1, err)
	}
	descriptor, exists := descriptors[name]
	if !exists {
		return operation{}, fmt.Errorf("operation %d: unknown operation %q", index+1, name)
	}
	parsed := operation{name: name, descriptor: descriptor, argument: argument, expressions: make(map[string]*compiledExpression), operands: make(map[string]operand)}
	if _, multiObject := stringSet("keys", "unique_keys", "values", "unique_values")[name]; multiObject && !emptyArgument(argument) {
		err = parsed.parseWith(argument, true)
		return parsed, wrapOperationError(index, name, err)
	}
	if _, ok := zeroArgumentOperations[name]; ok {
		if !emptyArgument(argument) {
			return operation{}, fmt.Errorf("operation %d (%s) does not accept arguments", index+1, name)
		}
		return parsed, nil
	}
	if _, ok := itemExpressionOperations[name]; ok {
		err = parsed.addExpression("expr", argument, itemLocals(), false)
		return parsed, wrapOperationError(index, name, err)
	}
	if _, ok := itemPredicateOperations[name]; ok {
		err = parsed.addExpression("expr", argument, itemLocals(), true)
		return parsed, wrapOperationError(index, name, err)
	}
	if _, ok := comparatorOperations[name]; ok {
		err = parsed.addExpression("expr", argument, map[string]any{"left": nil, "right": nil}, true)
		return parsed, wrapOperationError(index, name, err)
	}
	if _, ok := objectPredicateOperations[name]; ok {
		err = parsed.addExpression("expr", argument, objectLocals(), true)
		return parsed, wrapOperationError(index, name, err)
	}
	if _, ok := objectExpressionOperations[name]; ok {
		err = parsed.addExpression("expr", argument, objectLocals(), false)
		return parsed, wrapOperationError(index, name, err)
	}
	if _, ok := operandOperations[name]; ok {
		parsed.operands["value"], err = parseOperand(name, argument)
		return parsed, wrapOperationError(index, name, err)
	}

	switch name {
	case "filter_map", "reject_map":
		err = parsed.parseExpressionMap(argument, []expressionField{{"value", itemLocals(), false}, {"when", itemLocals(), true}})
	case "group_by_map", "associate":
		err = parsed.parseExpressionMap(argument, []expressionField{{"key", itemLocals(), false}, {"value", itemLocals(), false}})
	case "filter_to_map":
		err = parsed.parseExpressionMap(argument, []expressionField{{"key", itemLocals(), false}, {"value", itemLocals(), false}, {"when", itemLocals(), true}})
	case "filter_reject":
		err = parsed.addExpression("expr", argument, itemLocals(), true)
	case "take_filter":
		err = parsed.parseMixedMap(argument, []operandField{{"count", true}}, []expressionField{{"when", itemLocals(), true}})
	case "sliding":
		err = parsed.parseMixedMap(argument, []operandField{{"size", true}, {"step", true}}, nil)
	case "subset":
		err = parsed.parseMixedMap(argument, []operandField{{"offset", true}, {"length", true}}, nil)
	case "slice":
		err = parsed.parseMixedMap(argument, []operandField{{"start", true}, {"end", true}}, nil)
	case "replace":
		err = parsed.parseMixedMap(argument, []operandField{{"old", true}, {"new", true}, {"count", true}}, nil)
	case "replace_all":
		err = parsed.parseMixedMap(argument, []operandField{{"old", true}, {"new", true}}, nil)
	case "splice":
		err = parsed.parseMixedMap(argument, []operandField{{"index", true}, {"elements", true}}, nil)
	case "trim", "trim_left", "trim_right":
		err = parsed.parseNamedOrDirectOperand(argument, "cutset")
	case "trim_prefix", "has_prefix":
		err = parsed.parseNamedOrDirectOperand(argument, "prefix")
	case "trim_suffix", "has_suffix":
		err = parsed.parseNamedOrDirectOperand(argument, "suffix")
	case "cut":
		err = parsed.parseNamedOrDirectOperand(argument, "separator")
	case "cut_prefix":
		err = parsed.parseNamedOrDirectOperand(argument, "prefix")
	case "cut_suffix":
		err = parsed.parseNamedOrDirectOperand(argument, "suffix")
	case "concat", "interleave", "union", "intersect", "zip", "cross_join":
		err = parsed.parseWith(argument, true)
	case "difference", "elements_match":
		err = parsed.parseWith(argument, false)
	case "intersect_by":
		err = parsed.parseWithExpression(argument, "by", itemLocals())
	case "elements_match_by":
		err = parsed.parseSingleWithExpression(argument, "by", itemLocals())
	case "without":
		err = parsed.parseNamedOrDirectOperand(argument, "values")
	case "without_by":
		err = parsed.parseMixedMap(argument, []operandField{{"values", true}}, []expressionField{{"by", itemLocals(), false}})
	case "find_or_else":
		err = parsed.parseMixedMap(argument, []operandField{{"fallback", true}}, []expressionField{{"when", itemLocals(), true}})
	case "nth_or":
		err = parsed.parseMixedMap(argument, []operandField{{"nth", true}, {"fallback", true}}, nil)
	case "sort":
		err = parsed.parseSort(argument)
	case "sort_by":
		err = parsed.parseSortBy(argument)
	case "reduce", "reduce_right":
		locals := itemLocals()
		locals["acc"] = nil
		err = parsed.parseMixedMap(argument, []operandField{{"initial", true}}, []expressionField{{"expr", locals, false}})
	case "assign":
		err = parsed.parseWith(argument, true)
	case "value_or":
		err = parsed.parseMixedMap(argument, []operandField{{"key", true}, {"fallback", true}}, nil)
	case "map_keys":
		err = parsed.addExpression("expr", argument, objectLocals(), false)
	case "map_entries":
		err = parsed.parseExpressionMap(argument, []expressionField{{"key", objectLocals(), false}, {"value", objectLocals(), false}})
	case "filter_map_to_list":
		err = parsed.parseExpressionMap(argument, []expressionField{{"value", objectLocals(), false}, {"when", objectLocals(), true}})
	case "zip_with", "cross_join_with":
		err = parsed.parseTupleWith(argument)
	default:
		err = fmt.Errorf("operation parser is not implemented")
	}
	return parsed, wrapOperationError(index, name, err)
}

func operationEntry(raw any) (string, any, error) {
	if name, ok := raw.(string); ok {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", nil, fmt.Errorf("operation name must not be empty")
		}
		return name, nil, nil
	}
	mapping, ok := raw.(map[string]any)
	if !ok || len(mapping) != 1 {
		return "", nil, fmt.Errorf("operation must be a name or an object containing exactly one operation")
	}
	for name, argument := range mapping {
		if strings.TrimSpace(name) == "" {
			return "", nil, fmt.Errorf("operation name must not be empty")
		}
		return name, argument, nil
	}
	panic("unreachable")
}

func emptyArgument(value any) bool {
	if value == nil {
		return true
	}
	mapping, ok := value.(map[string]any)
	return ok && len(mapping) == 0
}

func wrapOperationError(index int, name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("operation %d (%s): %w", index+1, name, err)
}

func itemLocals() map[string]any { return map[string]any{"item": nil, "index": 0} }
func objectLocals() map[string]any {
	return map[string]any{"key": "", "value": nil, "index": 0}
}

func (operation *operation) addExpression(field string, raw any, locals map[string]any, boolean bool) error {
	source, ok := raw.(string)
	if !ok || strings.TrimSpace(source) == "" {
		return fmt.Errorf("%s must be a non-empty expression string", field)
	}
	compiled := &compiledExpression{label: field, source: source, locals: slices.Sorted(maps.Keys(locals))}
	if !templated(source) {
		program, err := compile(source, locals, boolean)
		if err != nil {
			return fmt.Errorf("compiling %s: %w", field, err)
		}
		compiled.program = program
	}
	operation.expressions[field] = compiled
	return nil
}

type expressionField struct {
	name    string
	locals  map[string]any
	boolean bool
}

type operandField struct {
	name     string
	required bool
}

func (operation *operation) parseExpressionMap(raw any, fields []expressionField) error {
	mapping, err := exactMapping(raw, fieldNames(fields), nil)
	if err != nil {
		return err
	}
	for _, field := range fields {
		if err := operation.addExpression(field.name, mapping[field.name], field.locals, field.boolean); err != nil {
			return err
		}
	}
	return nil
}

func (operation *operation) parseMixedMap(raw any, operands []operandField, expressions []expressionField) error {
	allowed := make([]string, 0, len(operands)+len(expressions))
	required := make([]string, 0, len(allowed))
	for _, field := range operands {
		allowed = append(allowed, field.name)
		if field.required {
			required = append(required, field.name)
		}
	}
	for _, field := range expressions {
		allowed = append(allowed, field.name)
		required = append(required, field.name)
	}
	mapping, err := exactMapping(raw, allowed, required)
	if err != nil {
		return err
	}
	for _, field := range operands {
		value, exists := mapping[field.name]
		if !exists {
			continue
		}
		operation.operands[field.name], err = parseOperand(field.name, value)
		if err != nil {
			return err
		}
	}
	for _, field := range expressions {
		if err := operation.addExpression(field.name, mapping[field.name], field.locals, field.boolean); err != nil {
			return err
		}
	}
	return nil
}

func (operation *operation) parseNamedOperand(raw any, name string) error {
	mapping, err := exactMapping(raw, []string{name}, []string{name})
	if err != nil {
		return err
	}
	operation.operands[name], err = parseOperand(name, mapping[name])
	return err
}

func (operation *operation) parseNamedOrDirectOperand(raw any, name string) error {
	if mapping, ok := raw.(map[string]any); ok {
		if _, exists := mapping[name]; exists {
			return operation.parseNamedOperand(raw, name)
		}
	}
	value, err := parseOperand(name, raw)
	operation.operands[name] = value
	return err
}

func (operation *operation) parseWith(raw any, many bool) error {
	mapping, err := exactMapping(raw, []string{"with"}, []string{"with"})
	if err != nil {
		return err
	}
	if many {
		values, ok := mapping["with"].([]any)
		if !ok || len(values) == 0 {
			return fmt.Errorf("with must be a non-empty list of operands")
		}
		for index, value := range values {
			parsed, parseErr := parseOperand(fmt.Sprintf("with[%d]", index), value)
			if parseErr != nil {
				return parseErr
			}
			operation.operandList = append(operation.operandList, parsed)
		}
		return nil
	}
	parsed, err := parseOperand("with", mapping["with"])
	operation.operands["with"] = parsed
	return err
}

func (operation *operation) parseWithExpression(raw any, field string, locals map[string]any) error {
	mapping, err := exactMapping(raw, []string{"with", field}, []string{"with", field})
	if err != nil {
		return err
	}
	values, ok := mapping["with"].([]any)
	if !ok || len(values) == 0 {
		return fmt.Errorf("with must be a non-empty list of operands")
	}
	for index, value := range values {
		parsed, parseErr := parseOperand(fmt.Sprintf("with[%d]", index), value)
		if parseErr != nil {
			return parseErr
		}
		operation.operandList = append(operation.operandList, parsed)
	}
	return operation.addExpression(field, mapping[field], locals, false)
}

func (operation *operation) parseSingleWithExpression(raw any, field string, locals map[string]any) error {
	mapping, err := exactMapping(raw, []string{"with", field}, []string{"with", field})
	if err != nil {
		return err
	}
	operation.operands["with"], err = parseOperand("with", mapping["with"])
	if err != nil {
		return err
	}
	return operation.addExpression(field, mapping[field], locals, false)
}

func (operation *operation) parseSort(raw any) error {
	if raw == nil {
		operation.argument = "asc"
		return nil
	}
	order, ok := raw.(string)
	if !ok || (order != "asc" && order != "desc") {
		return fmt.Errorf("order must be asc or desc")
	}
	operation.argument = order
	return nil
}

func (operation *operation) parseSortBy(raw any) error {
	if source, ok := raw.(string); ok {
		operation.argument = "asc"
		return operation.addExpression("expr", source, itemLocals(), false)
	}
	mapping, err := exactMapping(raw, []string{"expr", "order"}, []string{"expr"})
	if err != nil {
		return err
	}
	order := "asc"
	if configured, exists := mapping["order"]; exists {
		order, _ = configured.(string)
		if order != "asc" && order != "desc" {
			return fmt.Errorf("order must be asc or desc")
		}
	}
	operation.argument = order
	return operation.addExpression("expr", mapping["expr"], itemLocals(), false)
}

func (operation *operation) parseTupleWith(raw any) error {
	mapping, err := exactMapping(raw, []string{"with", "expr"}, []string{"with", "expr"})
	if err != nil {
		return err
	}
	values, ok := mapping["with"].([]any)
	if !ok || len(values) == 0 {
		return fmt.Errorf("with must be a non-empty list of operands")
	}
	for index, value := range values {
		parsed, parseErr := parseOperand(fmt.Sprintf("with[%d]", index), value)
		if parseErr != nil {
			return parseErr
		}
		operation.operandList = append(operation.operandList, parsed)
	}
	return operation.addExpression("expr", mapping["expr"], map[string]any{"items": []any{}, "index": 0}, false)
}

func parseOperand(label string, raw any) (operand, error) {
	mapping, ok := raw.(map[string]any)
	if !ok {
		return operand{literal: raw}, nil
	}
	if len(mapping) == 1 {
		if source, exists := mapping["expr"]; exists {
			text, ok := source.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return operand{}, fmt.Errorf("%s.expr must be a non-empty expression string", label)
			}
			compiled := &compiledExpression{label: label + ".expr", source: text}
			if !templated(text) {
				program, err := compile(text, nil, false)
				if err != nil {
					return operand{}, fmt.Errorf("compiling %s.expr: %w", label, err)
				}
				compiled.program = program
			}
			return operand{expression: compiled}, nil
		}
		if value, exists := mapping["literal"]; exists {
			return operand{literal: value}, nil
		}
	}
	return operand{literal: raw}, nil
}

func exactMapping(raw any, allowed, required []string) (map[string]any, error) {
	mapping, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("arguments must be an object")
	}
	allowedSet := stringSet(allowed...)
	for _, name := range slices.Sorted(maps.Keys(mapping)) {
		if _, ok := allowedSet[name]; !ok {
			return nil, fmt.Errorf("unknown argument %q", name)
		}
	}
	for _, name := range required {
		if _, exists := mapping[name]; !exists {
			return nil, fmt.Errorf("%s is required", name)
		}
	}
	return mapping, nil
}

func fieldNames(fields []expressionField) []string {
	result := make([]string, len(fields))
	for index := range fields {
		result[index] = fields[index].name
	}
	return result
}
