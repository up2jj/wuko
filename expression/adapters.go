package expression

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// TemplateFuncs returns the functions exposed to Wuko Go templates.
func TemplateFuncs() template.FuncMap {
	return TemplateFuncsWithSecret(nil)
}

// SecretResolver is the capability exposed to template and expression adapters.
type SecretResolver interface {
	Resolve(string) (string, error)
}

// TemplateFuncsWithSecret returns the shared template functions with secret bound to one
// workflow occurrence. A nil resolver keeps parsing and static validation available, but calls
// fail at execution time.
func TemplateFuncsWithSecret(resolver SecretResolver) template.FuncMap {
	secret := func(reference string) (string, error) {
		if resolver == nil {
			return "", fmt.Errorf("secret resolver is unavailable")
		}
		return resolver.Resolve(reference)
	}
	return template.FuncMap{
		"secret":                  secret,
		"lower":                   strings.ToLower,
		"upper":                   strings.ToUpper,
		"trim":                    strings.TrimSpace,
		"trimPrefix":              func(prefix, value string) string { return strings.TrimPrefix(value, prefix) },
		"trimSuffix":              func(suffix, value string) string { return strings.TrimSuffix(value, suffix) },
		"contains":                func(substring, value string) bool { return strings.Contains(value, substring) },
		"hasPrefix":               func(prefix, value string) bool { return strings.HasPrefix(value, prefix) },
		"hasSuffix":               func(suffix, value string) bool { return strings.HasSuffix(value, suffix) },
		"replace":                 func(old, replacement, value string) string { return strings.ReplaceAll(value, old, replacement) },
		"split":                   func(separator, value string) []string { return strings.Split(value, separator) },
		"join":                    join,
		"reverseText":             reverseText,
		"reverseWords":            reverseWords,
		"repeat":                  templateRepeat,
		"truncate":                templateTruncate,
		"squeeze":                 squeeze,
		"removeWhitespace":        removeWhitespace,
		"removePunctuation":       removePunctuation,
		"removeAccents":           removeAccents,
		"removeNonASCII":          removeNonASCII,
		"stripHTML":               stripHTML,
		"tabsToSpaces":            templateTabsToSpaces,
		"spacesToTabs":            templateSpacesToTabs,
		"newlinesToSpaces":        newlinesToSpaces,
		"spacesToNewlines":        spacesToNewlines,
		"rotate":                  templateRotate,
		"quote":                   templateQuote,
		"escapeRegex":             escapeRegex,
		"normalizeUnicode":        templateNormalizeUnicode,
		"slugify":                 templateSlugify,
		"default":                 defaultValue,
		"coalesce":                coalesce,
		"required":                required,
		"indent":                  indent,
		"nindent":                 nindent,
		"list":                    list,
		"dict":                    dict,
		"get":                     get,
		"hasKey":                  hasKey,
		"keys":                    keys,
		"sortAlpha":               sortAlpha,
		"toJSON":                  toJSON,
		"toJSONCompact":           toJSONCompact,
		"toYAML":                  toYAML,
		"parseJSON":               parseJSON,
		"parseYAML":               parseYAML,
		"parseTime":               templateParseTime,
		"addTime":                 templateAddTime,
		"formatTime":              templateFormatTime,
		"parseURI":                ParseURI,
		"buildURI":                BuildURI,
		"base64Encode":            templateBase64Encode,
		"base64Decode":            templateBase64Decode,
		"hexEncode":               templateHexEncode,
		"hexDecode":               HexDecode,
		"urlEncode":               URLEncode,
		"urlDecode":               URLDecode,
		"htmlEncode":              HTMLEncode,
		"htmlDecode":              HTMLDecode,
		"md5":                     func(values ...any) (string, error) { return templateDigest("md5", MD5, values...) },
		"sha1":                    func(values ...any) (string, error) { return templateDigest("sha1", SHA1, values...) },
		"sha256":                  func(values ...any) (string, error) { return templateDigest("sha256", SHA256, values...) },
		"sha512":                  func(values ...any) (string, error) { return templateDigest("sha512", SHA512, values...) },
		"hmacSHA256":              func(values ...any) (string, error) { return templateHMAC("hmacSHA256", HMACSHA256, values...) },
		"hmacSHA512":              func(values ...any) (string, error) { return templateHMAC("hmacSHA512", HMACSHA512, values...) },
		"baseConvert":             templateBaseConvert,
		"romanEncode":             templateRomanEncode,
		"romanDecode":             RomanDecode,
		"ordinal":                 templateOrdinal,
		"countBytes":              CountBytes,
		"countRunes":              CountRunes,
		"countGraphemes":          CountGraphemes,
		"countWords":              CountWords,
		"countLines":              CountLines,
		"uuid":                    adapterUUID,
		"randomString":            adapterRandomString,
		"randomInt":               adapterRandomInt,
		"randomToken":             adapterRandomToken,
		"password":                adapterPassword,
		"currentTime":             CurrentTime,
		"unixTimestamp":           adapterUnixTimestamp,
		"buildConventionalCommit": BuildConventionalCommit,
		"isConventionalCommit":    templateIsConventionalCommit,
	}
}

// Compile compiles an Expr expression with Wuko's shared helper functions.
func Compile(source string, options ...expr.Option) (*vm.Program, error) {
	allOptions := append(exprOptions(), options...)
	return expr.Compile(source, allOptions...)
}

// Eval compiles and evaluates an Expr expression with Wuko's shared helpers.
func Eval(source string, environment any) (any, error) {
	program, err := Compile(source, expr.Env(environment))
	if err != nil {
		return nil, err
	}
	return expr.Run(program, environment)
}

func exprOptions() []expr.Option {
	return []expr.Option{
		// Expr's implicit now() would let any expression read the clock without saying so.
		// Wuko keeps the clock behind explicitly named boundaries instead: the recordable,
		// --var-overridable time step, and the documented currentTime() and unixTimestamp()
		// helpers. Disabling the builtin fails such an expression at compile time with
		// "unknown name now".
		expr.DisableBuiltin("now"),
		expr.Function("default", func(values ...any) (any, error) {
			return defaultValue(values[1], values[0]), nil
		}, new(func(any, any) any)),
		expr.Function("coalesce", func(values ...any) (any, error) {
			return coalesce(values...), nil
		}, new(func(...any) any)),
		expr.Function("reverseText", func(values ...any) (any, error) {
			return reverseText(values[0].(string)), nil
		}, new(func(string) string)),
		expr.Function("reverseWords", func(values ...any) (any, error) {
			return reverseWords(values[0].(string)), nil
		}, new(func(string) string)),
		expr.Function("repeat", exprRepeat, new(func(...any) string)),
		expr.Function("truncate", exprTruncate, new(func(...any) string)),
		expr.Function("squeeze", func(values ...any) (any, error) {
			return squeeze(values[0].(string)), nil
		}, new(func(string) string)),
		expr.Function("removeWhitespace", func(values ...any) (any, error) {
			return removeWhitespace(values[0].(string)), nil
		}, new(func(string) string)),
		expr.Function("removePunctuation", func(values ...any) (any, error) {
			return removePunctuation(values[0].(string)), nil
		}, new(func(string) string)),
		expr.Function("removeAccents", func(values ...any) (any, error) {
			return removeAccents(values[0].(string)), nil
		}, new(func(string) string)),
		expr.Function("removeNonASCII", func(values ...any) (any, error) {
			return removeNonASCII(values[0].(string)), nil
		}, new(func(string) string)),
		expr.Function("stripHTML", func(values ...any) (any, error) {
			return stripHTML(values[0].(string)), nil
		}, new(func(string) string)),
		expr.Function("tabsToSpaces", exprTabsToSpaces, new(func(...any) string)),
		expr.Function("spacesToTabs", exprSpacesToTabs, new(func(...any) string)),
		expr.Function("newlinesToSpaces", func(values ...any) (any, error) {
			return newlinesToSpaces(values[0].(string)), nil
		}, new(func(string) string)),
		expr.Function("spacesToNewlines", func(values ...any) (any, error) {
			return spacesToNewlines(values[0].(string)), nil
		}, new(func(string) string)),
		expr.Function("rotate", exprRotate, new(func(...any) string)),
		expr.Function("quote", exprQuote, new(func(...any) string)),
		expr.Function("escapeRegex", func(values ...any) (any, error) {
			return escapeRegex(values[0].(string)), nil
		}, new(func(string) string)),
		expr.Function("normalizeUnicode", exprNormalizeUnicode, new(func(...any) string)),
		expr.Function("slugify", exprSlugify, new(func(...any) string)),
		expr.Function("required", func(values ...any) (any, error) {
			return required(values[1].(string), values[0])
		}, new(func(any, string) any)),
		expr.Function("indent", func(values ...any) (any, error) {
			return indent(values[1].(int), values[0].(string))
		}, new(func(string, int) string)),
		expr.Function("nindent", func(values ...any) (any, error) {
			return nindent(values[1].(int), values[0].(string))
		}, new(func(string, int) string)),
		expr.Function("list", func(values ...any) (any, error) {
			return list(values...), nil
		}, new(func(...any) []any)),
		expr.Function("dict", func(values ...any) (any, error) {
			return dict(values...)
		}, new(func(...any) map[string]any)),
		expr.Function("get", func(values ...any) (any, error) {
			return get(values[1].(string), values[0])
		}, new(func(any, string) any)),
		expr.Function("hasKey", func(values ...any) (any, error) {
			return hasKey(values[1].(string), values[0])
		}, new(func(any, string) bool)),
		expr.Function("keys", func(values ...any) (any, error) {
			return keys(values[0])
		}, new(func(any) []string)),
		expr.Function("sortAlpha", func(values ...any) (any, error) {
			return sortAlpha(values[0])
		}, new(func(any) []string)),
		expr.Function("indexBy", func(values ...any) (any, error) {
			return indexBy(values[0], values[1].(string))
		}, new(func(any, string) map[string]any)),
		expr.Function("chunk", func(values ...any) (any, error) {
			return chunk(values[0], values[1].(int))
		}, new(func(any, int) [][]any)),
		expr.Function("toJSON", func(values ...any) (any, error) {
			return toJSON(values[0])
		}, new(func(any) string)),
		expr.Function("toJSONCompact", func(values ...any) (any, error) {
			return toJSONCompact(values[0])
		}, new(func(any) string)),
		expr.Function("toYAML", func(values ...any) (any, error) {
			return toYAML(values[0])
		}, new(func(any) string)),
		expr.Function("parseJSON", func(values ...any) (any, error) {
			return parseJSON(values[0].(string))
		}, new(func(string) any)),
		expr.Function("parseYAML", func(values ...any) (any, error) {
			return parseYAML(values[0].(string))
		}, new(func(string) any)),
		expr.Function("parseTime", exprParseTime, new(func(...any) string)),
		expr.Function("addTime", exprAddTime, new(func(...any) string)),
		expr.Function("formatTime", exprFormatTime, new(func(...any) string)),
		expr.Function("parseURI", func(values ...any) (any, error) {
			return ParseURI(values[0].(string))
		}, new(func(string) map[string]any)),
		expr.Function("buildURI", func(values ...any) (any, error) {
			return BuildURI(values[0].(map[string]any))
		}, new(func(map[string]any) string)),
		expr.Function("base64Encode", exprBase64Encode, new(func(...any) string)),
		expr.Function("base64Decode", exprBase64Decode, new(func(...any) string)),
		expr.Function("hexEncode", exprHexEncode, new(func(...any) string)),
		expr.Function("hexDecode", exprText("hexDecode", HexDecode), new(func(string) string)),
		expr.Function("urlEncode", exprText("urlEncode", URLEncode), new(func(string) string)),
		expr.Function("urlDecode", exprText("urlDecode", URLDecode), new(func(string) string)),
		expr.Function("htmlEncode", exprText("htmlEncode", HTMLEncode), new(func(string) string)),
		expr.Function("htmlDecode", exprText("htmlDecode", HTMLDecode), new(func(string) string)),
		expr.Function("md5", func(values ...any) (any, error) { return exprDigest("md5", MD5, values...) }, new(func(...any) string)),
		expr.Function("sha1", func(values ...any) (any, error) { return exprDigest("sha1", SHA1, values...) }, new(func(...any) string)),
		expr.Function("sha256", func(values ...any) (any, error) { return exprDigest("sha256", SHA256, values...) }, new(func(...any) string)),
		expr.Function("sha512", func(values ...any) (any, error) { return exprDigest("sha512", SHA512, values...) }, new(func(...any) string)),
		expr.Function("hmacSHA256", func(values ...any) (any, error) { return exprHMAC("hmacSHA256", HMACSHA256, values...) }, new(func(...any) string)),
		expr.Function("hmacSHA512", func(values ...any) (any, error) { return exprHMAC("hmacSHA512", HMACSHA512, values...) }, new(func(...any) string)),
		expr.Function("baseConvert", exprBaseConvert, new(func(...any) string)),
		expr.Function("romanEncode", func(values ...any) (any, error) {
			value, err := utilityInt("Roman numeral value", values[0])
			if err != nil {
				return nil, err
			}
			return RomanEncode(value)
		}, new(func(any) string)),
		expr.Function("romanDecode", func(values ...any) (any, error) {
			text, err := utilityString("romanDecode value", values[0])
			if err != nil {
				return nil, err
			}
			return RomanDecode(text)
		}, new(func(string) int)),
		expr.Function("ordinal", func(values ...any) (any, error) {
			value, err := utilityInt64("ordinal value", values[0])
			if err != nil {
				return nil, err
			}
			return Ordinal(value), nil
		}, new(func(any) string)),
		expr.Function("countBytes", exprCount("countBytes", CountBytes), new(func(string) int)),
		expr.Function("countRunes", exprCount("countRunes", CountRunes), new(func(string) int)),
		expr.Function("countGraphemes", exprCount("countGraphemes", CountGraphemes), new(func(string) int)),
		expr.Function("countWords", exprCount("countWords", CountWords), new(func(string) int)),
		expr.Function("countLines", exprCount("countLines", CountLines), new(func(string) int)),
		expr.Function("uuid", adapterUUID, new(func(...any) string)),
		expr.Function("randomString", adapterRandomString, new(func(...any) string)),
		expr.Function("randomInt", adapterRandomInt, new(func(...any) int64)),
		expr.Function("randomToken", adapterRandomToken, new(func(...any) string)),
		expr.Function("password", adapterPassword, new(func(...any) string)),
		expr.Function("currentTime", func(...any) (any, error) { return CurrentTime(), nil }, new(func() string)),
		expr.Function("unixTimestamp", adapterUnixTimestamp, new(func(...any) int64)),
		expr.Function("buildConventionalCommit", func(values ...any) (any, error) {
			return BuildConventionalCommit(values[0].(map[string]any))
		}, new(func(map[string]any) string)),
		expr.Function("isConventionalCommit", exprIsConventionalCommit, new(func(...any) bool)),
	}
}

func templateParseTime(values ...any) (string, error) {
	value, layout, timezone, err := templateTimeArguments("parseTime", values)
	if err != nil {
		return "", err
	}
	return ParseTime(value, layout, timezone)
}

func templateFormatTime(values ...any) (string, error) {
	value, layout, timezone, err := templateTimeArguments("formatTime", values)
	if err != nil {
		return "", err
	}
	return FormatTime(value, layout, timezone)
}

func templateTimeArguments(name string, values []any) (value, layout, timezone string, err error) {
	if len(values) != 2 && len(values) != 3 {
		return "", "", "", fmt.Errorf("%s expects a layout, optional timezone, and piped time value", name)
	}
	stringsByPosition := make([]string, len(values))
	for i, candidate := range values {
		text, ok := candidate.(string)
		if !ok {
			return "", "", "", fmt.Errorf("%s argument %d must be a string, got %T", name, i+1, candidate)
		}
		stringsByPosition[i] = text
	}
	layout = stringsByPosition[0]
	value = stringsByPosition[len(stringsByPosition)-1]
	if len(stringsByPosition) == 3 {
		timezone = stringsByPosition[1]
	}
	return value, layout, timezone, nil
}

func templateAddTime(values ...any) (string, error) {
	if len(values) != 2 {
		return "", fmt.Errorf("addTime expects an adjustments object and piped time value")
	}
	adjustments, ok := values[0].(map[string]any)
	if !ok {
		return "", fmt.Errorf("addTime adjustments must be an object, got %T", values[0])
	}
	value, ok := values[1].(string)
	if !ok {
		return "", fmt.Errorf("addTime value must be a string, got %T", values[1])
	}
	return AddTime(value, adjustments)
}

func exprParseTime(values ...any) (any, error) {
	value, layout, timezone, err := exprTimeArguments("parseTime", values)
	if err != nil {
		return nil, err
	}
	return ParseTime(value, layout, timezone)
}

func exprFormatTime(values ...any) (any, error) {
	value, layout, timezone, err := exprTimeArguments("formatTime", values)
	if err != nil {
		return nil, err
	}
	return FormatTime(value, layout, timezone)
}

func exprTimeArguments(name string, values []any) (value, layout, timezone string, err error) {
	if len(values) != 2 && len(values) != 3 {
		return "", "", "", fmt.Errorf("%s expects a time value, layout, and optional timezone", name)
	}
	for i, candidate := range values {
		if _, ok := candidate.(string); !ok {
			return "", "", "", fmt.Errorf("%s argument %d must be a string, got %T", name, i+1, candidate)
		}
	}
	value = values[0].(string)
	layout = values[1].(string)
	if len(values) == 3 {
		timezone = values[2].(string)
	}
	return value, layout, timezone, nil
}

func exprAddTime(values ...any) (any, error) {
	if len(values) != 2 {
		return nil, fmt.Errorf("addTime expects a time value and adjustments object")
	}
	value, ok := values[0].(string)
	if !ok {
		return nil, fmt.Errorf("addTime value must be a string, got %T", values[0])
	}
	adjustments, ok := values[1].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("addTime adjustments must be an object, got %T", values[1])
	}
	return AddTime(value, adjustments)
}
