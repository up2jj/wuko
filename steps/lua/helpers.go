package lua

import (
	"fmt"
	"strings"

	"github.com/up2jj/wuko/expression"
	glua "github.com/yuin/gopher-lua"
)

func helperFunctions() map[string]glua.LGFunction {
	return map[string]glua.LGFunction{
		"lower":           helperLower,
		"upper":           helperUpper,
		"trim":            helperTrim,
		"trim_prefix":     helperTrimPrefix,
		"trim_suffix":     helperTrimSuffix,
		"contains":        helperContains,
		"has_prefix":      helperHasPrefix,
		"has_suffix":      helperHasSuffix,
		"replace":         helperReplace,
		"split":           helperSplit,
		"join":            helperJoin,
		"slugify":         helperSlugify,
		"default":         helperDefault,
		"coalesce":        helperCoalesce,
		"required":        helperRequired,
		"indent":          helperIndent,
		"nindent":         helperNindent,
		"list":            helperList,
		"dict":            helperDict,
		"get":             helperGet,
		"has_key":         helperHasKey,
		"keys":            helperKeys,
		"sort_alpha":      helperSortAlpha,
		"to_json":         helperToJSON,
		"to_json_compact": helperToJSONCompact,
		"to_yaml":         helperToYAML,
	}
}

func helperLower(state *glua.LState) int {
	state.Push(glua.LString(strings.ToLower(state.CheckString(1))))
	return 1
}

func helperUpper(state *glua.LState) int {
	state.Push(glua.LString(strings.ToUpper(state.CheckString(1))))
	return 1
}

func helperTrim(state *glua.LState) int {
	state.Push(glua.LString(strings.TrimSpace(state.CheckString(1))))
	return 1
}

func helperTrimPrefix(state *glua.LState) int {
	state.Push(glua.LString(strings.TrimPrefix(state.CheckString(1), state.CheckString(2))))
	return 1
}

func helperTrimSuffix(state *glua.LState) int {
	state.Push(glua.LString(strings.TrimSuffix(state.CheckString(1), state.CheckString(2))))
	return 1
}

func helperContains(state *glua.LState) int {
	state.Push(glua.LBool(strings.Contains(state.CheckString(1), state.CheckString(2))))
	return 1
}

func helperHasPrefix(state *glua.LState) int {
	state.Push(glua.LBool(strings.HasPrefix(state.CheckString(1), state.CheckString(2))))
	return 1
}

func helperHasSuffix(state *glua.LState) int {
	state.Push(glua.LBool(strings.HasSuffix(state.CheckString(1), state.CheckString(2))))
	return 1
}

func helperReplace(state *glua.LState) int {
	state.Push(glua.LString(strings.ReplaceAll(state.CheckString(1), state.CheckString(2), state.CheckString(3))))
	return 1
}

func helperSplit(state *glua.LState) int {
	return pushHelperResult(state, "split", strings.Split(state.CheckString(1), state.CheckString(2)), nil)
}

func helperJoin(state *glua.LState) int {
	value, err := helperValue(state.Get(1))
	if err != nil {
		return pushHelperResult(state, "join", nil, err)
	}
	result, err := expression.Join(value, state.CheckString(2))
	return pushHelperResult(state, "join", result, err)
}

func helperSlugify(state *glua.LState) int {
	if state.GetTop() > 2 {
		state.RaiseError("helpers.slugify: expected a string and optional options object")
		return 0
	}
	var options map[string]any
	if state.GetTop() == 2 {
		value, err := helperValue(state.Get(2))
		if err != nil {
			return pushHelperResult(state, "slugify", nil, err)
		}
		if value != nil {
			var ok bool
			options, ok = value.(map[string]any)
			if !ok {
				return pushHelperResult(state, "slugify", nil, fmt.Errorf("options must be an object, got %T", value))
			}
		}
	}
	result, err := expression.Slugify(state.CheckString(1), options)
	return pushHelperResult(state, "slugify", result, err)
}

func helperDefault(state *glua.LState) int {
	values, err := helperValues(state, 1)
	if err != nil {
		return pushHelperResult(state, "default", nil, err)
	}
	if len(values) != 2 {
		state.RaiseError("helpers.default: expected 2 arguments, got %d", len(values))
		return 0
	}
	return pushHelperResult(state, "default", expression.Default(values[0], values[1]), nil)
}

func helperCoalesce(state *glua.LState) int {
	values, err := helperValues(state, 1)
	if err != nil {
		return pushHelperResult(state, "coalesce", nil, err)
	}
	return pushHelperResult(state, "coalesce", expression.Coalesce(values...), nil)
}

func helperRequired(state *glua.LState) int {
	value, err := helperValue(state.Get(1))
	if err != nil {
		return pushHelperResult(state, "required", nil, err)
	}
	result, err := expression.Required(value, state.CheckString(2))
	return pushHelperResult(state, "required", result, err)
}

func helperIndent(state *glua.LState) int {
	result, err := expression.Indent(state.CheckString(1), state.CheckInt(2))
	return pushHelperResult(state, "indent", result, err)
}

func helperNindent(state *glua.LState) int {
	result, err := expression.Nindent(state.CheckString(1), state.CheckInt(2))
	return pushHelperResult(state, "nindent", result, err)
}

func helperList(state *glua.LState) int {
	values, err := helperValues(state, 1)
	if err != nil {
		return pushHelperResult(state, "list", nil, err)
	}
	return pushHelperResult(state, "list", expression.List(values...), nil)
}

func helperDict(state *glua.LState) int {
	values, err := helperValues(state, 1)
	if err != nil {
		return pushHelperResult(state, "dict", nil, err)
	}
	result, err := expression.Dict(values...)
	return pushHelperResult(state, "dict", result, err)
}

func helperGet(state *glua.LState) int {
	value, err := helperValue(state.Get(1))
	if err != nil {
		return pushHelperResult(state, "get", nil, err)
	}
	result, err := expression.Get(value, state.CheckString(2))
	return pushHelperResult(state, "get", result, err)
}

func helperHasKey(state *glua.LState) int {
	value, err := helperValue(state.Get(1))
	if err != nil {
		return pushHelperResult(state, "has_key", nil, err)
	}
	result, err := expression.HasKey(value, state.CheckString(2))
	return pushHelperResult(state, "has_key", result, err)
}

func helperKeys(state *glua.LState) int {
	value, err := helperValue(state.Get(1))
	if err != nil {
		return pushHelperResult(state, "keys", nil, err)
	}
	result, err := expression.Keys(value)
	return pushHelperResult(state, "keys", result, err)
}

func helperSortAlpha(state *glua.LState) int {
	value, err := helperValue(state.Get(1))
	if err != nil {
		return pushHelperResult(state, "sort_alpha", nil, err)
	}
	result, err := expression.SortAlpha(value)
	return pushHelperResult(state, "sort_alpha", result, err)
}

func helperToJSON(state *glua.LState) int {
	value, err := helperValue(state.Get(1))
	if err != nil {
		return pushHelperResult(state, "to_json", nil, err)
	}
	result, err := expression.ToJSON(value)
	return pushHelperResult(state, "to_json", result, err)
}

func helperToJSONCompact(state *glua.LState) int {
	value, err := helperValue(state.Get(1))
	if err != nil {
		return pushHelperResult(state, "to_json_compact", nil, err)
	}
	result, err := expression.ToJSONCompact(value)
	return pushHelperResult(state, "to_json_compact", result, err)
}

func helperToYAML(state *glua.LState) int {
	value, err := helperValue(state.Get(1))
	if err != nil {
		return pushHelperResult(state, "to_yaml", nil, err)
	}
	result, err := expression.ToYAML(value)
	return pushHelperResult(state, "to_yaml", result, err)
}

func helperValues(state *glua.LState, start int) ([]any, error) {
	values := make([]any, 0, state.GetTop()-start+1)
	for index := start; index <= state.GetTop(); index++ {
		value, err := helperValue(state.Get(index))
		if err != nil {
			return nil, fmt.Errorf("argument %d: %w", index, err)
		}
		values = append(values, value)
	}
	return values, nil
}

func helperValue(value glua.LValue) (any, error) {
	return fromLua(value, make(map[*glua.LTable]bool))
}

func pushHelperResult(state *glua.LState, name string, value any, err error) int {
	if err != nil {
		state.RaiseError("helpers.%s: %v", name, err)
		return 0
	}
	converted, err := toLua(state, value)
	if err != nil {
		state.RaiseError("helpers.%s: %v", name, err)
		return 0
	}
	state.Push(converted)
	return 1
}
