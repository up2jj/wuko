package lua

import (
	"fmt"
	"strings"

	"github.com/up2jj/wuko/expression"
	glua "github.com/yuin/gopher-lua"
)

func helperFunctions() map[string]glua.LGFunction {
	return map[string]glua.LGFunction{
		"lower":                     helperLower,
		"upper":                     helperUpper,
		"trim":                      helperTrim,
		"trim_prefix":               helperTrimPrefix,
		"trim_suffix":               helperTrimSuffix,
		"contains":                  helperContains,
		"has_prefix":                helperHasPrefix,
		"has_suffix":                helperHasSuffix,
		"replace":                   helperReplace,
		"split":                     helperSplit,
		"join":                      helperJoin,
		"reverse_text":              helperReverseText,
		"reverse_words":             helperReverseWords,
		"repeat_text":               helperRepeat,
		"truncate":                  helperTruncate,
		"squeeze":                   helperSqueeze,
		"remove_whitespace":         helperRemoveWhitespace,
		"remove_punctuation":        helperRemovePunctuation,
		"remove_accents":            helperRemoveAccents,
		"remove_non_ascii":          helperRemoveNonASCII,
		"strip_html":                helperStripHTML,
		"tabs_to_spaces":            helperTabsToSpaces,
		"spaces_to_tabs":            helperSpacesToTabs,
		"newlines_to_spaces":        helperNewlinesToSpaces,
		"spaces_to_newlines":        helperSpacesToNewlines,
		"rotate":                    helperRotate,
		"quote":                     helperQuote,
		"escape_regex":              helperEscapeRegex,
		"normalize_unicode":         helperNormalizeUnicode,
		"slugify":                   helperSlugify,
		"default":                   helperDefault,
		"coalesce":                  helperCoalesce,
		"required":                  helperRequired,
		"indent":                    helperIndent,
		"nindent":                   helperNindent,
		"list":                      helperList,
		"dict":                      helperDict,
		"get":                       helperGet,
		"has_key":                   helperHasKey,
		"keys":                      helperKeys,
		"sort_alpha":                helperSortAlpha,
		"to_json":                   helperToJSON,
		"to_json_compact":           helperToJSONCompact,
		"to_yaml":                   helperToYAML,
		"parse_time":                helperParseTime,
		"add_time":                  helperAddTime,
		"format_time":               helperFormatTime,
		"parse_uri":                 helperParseURI,
		"build_uri":                 helperBuildURI,
		"build_conventional_commit": helperBuildConventionalCommit,
		"is_conventional_commit":    helperIsConventionalCommit,
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

func helperReverseText(state *glua.LState) int {
	state.Push(glua.LString(expression.ReverseText(state.CheckString(1))))
	return 1
}

func helperReverseWords(state *glua.LState) int {
	state.Push(glua.LString(expression.ReverseWords(state.CheckString(1))))
	return 1
}

func helperRepeat(state *glua.LState) int {
	if state.GetTop() < 1 || state.GetTop() > 3 {
		state.RaiseError("helpers.repeat_text: expected a value, optional count, and optional separator")
		return 0
	}
	count := 2
	if state.GetTop() >= 2 {
		count = state.CheckInt(2)
	}
	separator := ""
	if state.GetTop() == 3 {
		separator = state.CheckString(3)
	}
	result, err := expression.Repeat(state.CheckString(1), count, separator)
	return pushHelperResult(state, "repeat_text", result, err)
}

func helperTruncate(state *glua.LState) int {
	if state.GetTop() < 1 || state.GetTop() > 3 {
		state.RaiseError("helpers.truncate: expected a value, optional length, and optional suffix")
		return 0
	}
	length := 80
	if state.GetTop() >= 2 {
		length = state.CheckInt(2)
	}
	suffix := ""
	if state.GetTop() == 3 {
		suffix = state.CheckString(3)
	}
	result, err := expression.Truncate(state.CheckString(1), length, suffix)
	return pushHelperResult(state, "truncate", result, err)
}

func helperSqueeze(state *glua.LState) int {
	state.Push(glua.LString(expression.Squeeze(state.CheckString(1))))
	return 1
}

func helperRemoveWhitespace(state *glua.LState) int {
	state.Push(glua.LString(expression.RemoveWhitespace(state.CheckString(1))))
	return 1
}

func helperRemovePunctuation(state *glua.LState) int {
	state.Push(glua.LString(expression.RemovePunctuation(state.CheckString(1))))
	return 1
}

func helperRemoveAccents(state *glua.LState) int {
	state.Push(glua.LString(expression.RemoveAccents(state.CheckString(1))))
	return 1
}

func helperRemoveNonASCII(state *glua.LState) int {
	state.Push(glua.LString(expression.RemoveNonASCII(state.CheckString(1))))
	return 1
}

func helperStripHTML(state *glua.LState) int {
	state.Push(glua.LString(expression.StripHTML(state.CheckString(1))))
	return 1
}

func helperTabsToSpaces(state *glua.LState) int {
	if state.GetTop() < 1 || state.GetTop() > 2 {
		state.RaiseError("helpers.tabs_to_spaces: expected a value and optional width")
		return 0
	}
	width := 4
	if state.GetTop() == 2 {
		width = state.CheckInt(2)
	}
	result, err := expression.TabsToSpaces(state.CheckString(1), width)
	return pushHelperResult(state, "tabs_to_spaces", result, err)
}

func helperSpacesToTabs(state *glua.LState) int {
	if state.GetTop() < 1 || state.GetTop() > 2 {
		state.RaiseError("helpers.spaces_to_tabs: expected a value and optional width")
		return 0
	}
	width := 4
	if state.GetTop() == 2 {
		width = state.CheckInt(2)
	}
	result, err := expression.SpacesToTabs(state.CheckString(1), width)
	return pushHelperResult(state, "spaces_to_tabs", result, err)
}

func helperNewlinesToSpaces(state *glua.LState) int {
	state.Push(glua.LString(expression.NewlinesToSpaces(state.CheckString(1))))
	return 1
}

func helperSpacesToNewlines(state *glua.LState) int {
	state.Push(glua.LString(expression.SpacesToNewlines(state.CheckString(1))))
	return 1
}

func helperRotate(state *glua.LState) int {
	if state.GetTop() < 1 || state.GetTop() > 2 {
		state.RaiseError("helpers.rotate: expected a value and optional count")
		return 0
	}
	count := 1
	if state.GetTop() == 2 {
		count = state.CheckInt(2)
	}
	state.Push(glua.LString(expression.Rotate(state.CheckString(1), count)))
	return 1
}

func helperQuote(state *glua.LState) int {
	if state.GetTop() < 1 || state.GetTop() > 2 {
		state.RaiseError("helpers.quote: expected a value and optional delimiter")
		return 0
	}
	delimiter := "\""
	if state.GetTop() == 2 {
		delimiter = state.CheckString(2)
	}
	result, err := expression.Quote(state.CheckString(1), delimiter)
	return pushHelperResult(state, "quote", result, err)
}

func helperEscapeRegex(state *glua.LState) int {
	state.Push(glua.LString(expression.EscapeRegex(state.CheckString(1))))
	return 1
}

func helperNormalizeUnicode(state *glua.LState) int {
	if state.GetTop() < 1 || state.GetTop() > 2 {
		state.RaiseError("helpers.normalize_unicode: expected a value and optional form")
		return 0
	}
	form := "nfc"
	if state.GetTop() == 2 {
		form = state.CheckString(2)
	}
	result, err := expression.NormalizeUnicode(state.CheckString(1), form)
	return pushHelperResult(state, "normalize_unicode", result, err)
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

func helperParseTime(state *glua.LState) int {
	if state.GetTop() != 2 && state.GetTop() != 3 {
		state.RaiseError("helpers.parse_time: expected a time value, layout, and optional timezone")
		return 0
	}
	timezone := ""
	if state.GetTop() == 3 {
		timezone = state.CheckString(3)
	}
	result, err := expression.ParseTime(state.CheckString(1), state.CheckString(2), timezone)
	return pushHelperResult(state, "parse_time", result, err)
}

func helperAddTime(state *glua.LState) int {
	if state.GetTop() != 2 {
		state.RaiseError("helpers.add_time: expected a time value and adjustments object")
		return 0
	}
	value, err := helperValue(state.Get(2))
	if err != nil {
		return pushHelperResult(state, "add_time", nil, err)
	}
	adjustments, ok := value.(map[string]any)
	if !ok {
		return pushHelperResult(state, "add_time", nil, fmt.Errorf("adjustments must be an object, got %T", value))
	}
	result, err := expression.AddTime(state.CheckString(1), adjustments)
	return pushHelperResult(state, "add_time", result, err)
}

func helperFormatTime(state *glua.LState) int {
	if state.GetTop() != 2 && state.GetTop() != 3 {
		state.RaiseError("helpers.format_time: expected a time value, layout, and optional timezone")
		return 0
	}
	timezone := ""
	if state.GetTop() == 3 {
		timezone = state.CheckString(3)
	}
	result, err := expression.FormatTime(state.CheckString(1), state.CheckString(2), timezone)
	return pushHelperResult(state, "format_time", result, err)
}

func helperParseURI(state *glua.LState) int {
	if state.GetTop() != 1 {
		state.RaiseError("helpers.parse_uri: expected a URI string")
		return 0
	}
	result, err := expression.ParseURI(state.CheckString(1))
	return pushHelperResult(state, "parse_uri", result, err)
}

func helperBuildURI(state *glua.LState) int {
	if state.GetTop() != 1 {
		state.RaiseError("helpers.build_uri: expected a URI components object")
		return 0
	}
	value, err := helperValue(state.Get(1))
	if err != nil {
		return pushHelperResult(state, "build_uri", nil, err)
	}
	parts, ok := value.(map[string]any)
	if !ok {
		return pushHelperResult(state, "build_uri", nil, fmt.Errorf("components must be an object, got %T", value))
	}
	result, err := expression.BuildURI(parts)
	return pushHelperResult(state, "build_uri", result, err)
}

func helperBuildConventionalCommit(state *glua.LState) int {
	if state.GetTop() != 1 {
		state.RaiseError("helpers.build_conventional_commit: expected a configuration object")
		return 0
	}
	value, err := helperValue(state.Get(1))
	if err != nil {
		return pushHelperResult(state, "build_conventional_commit", nil, err)
	}
	config, ok := value.(map[string]any)
	if !ok {
		return pushHelperResult(state, "build_conventional_commit", nil, fmt.Errorf("configuration must be an object, got %T", value))
	}
	result, err := expression.BuildConventionalCommit(config)
	return pushHelperResult(state, "build_conventional_commit", result, err)
}

func helperIsConventionalCommit(state *glua.LState) int {
	if state.GetTop() != 1 && state.GetTop() != 2 {
		state.RaiseError("helpers.is_conventional_commit: expected a message and optional options object")
		return 0
	}
	message := state.CheckString(1)
	var options map[string]any
	if state.GetTop() == 2 && state.Get(2) != glua.LNil {
		value, err := helperValue(state.Get(2))
		if err != nil {
			return pushHelperResult(state, "is_conventional_commit", nil, err)
		}
		var ok bool
		options, ok = value.(map[string]any)
		if !ok {
			return pushHelperResult(state, "is_conventional_commit", nil, fmt.Errorf("options must be an object, got %T", value))
		}
	}
	result, err := expression.IsConventionalCommit(message, options)
	return pushHelperResult(state, "is_conventional_commit", result, err)
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
