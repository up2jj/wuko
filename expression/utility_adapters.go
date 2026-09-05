package expression

import "fmt"

func optionalOptions(name string, value any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	options, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s options must be an object, got %T", name, value)
	}
	return options, nil
}

func stringOptionsArguments(name string, values []any) (string, map[string]any, error) {
	if len(values) != 1 && len(values) != 2 {
		return "", nil, fmt.Errorf("%s expects a value and optional options object", name)
	}
	value, ok := values[0].(string)
	if !ok {
		return "", nil, fmt.Errorf("%s value must be a string, got %T", name, values[0])
	}
	if len(values) == 1 {
		return value, nil, nil
	}
	options, err := optionalOptions(name, values[1])
	return value, options, err
}

func templateStringOptionsArguments(name string, values []any) (string, map[string]any, error) {
	if len(values) != 1 && len(values) != 2 {
		return "", nil, fmt.Errorf("%s expects optional options and a piped value", name)
	}
	value, ok := values[len(values)-1].(string)
	if !ok {
		return "", nil, fmt.Errorf("%s value must be a string, got %T", name, values[len(values)-1])
	}
	if len(values) == 1 {
		return value, nil, nil
	}
	options, err := optionalOptions(name, values[0])
	return value, options, err
}

func templateBase64Encode(values ...any) (string, error) {
	value, options, err := templateStringOptionsArguments("base64Encode", values)
	if err != nil {
		return "", err
	}
	return Base64Encode(value, options)
}

func templateBase64Decode(values ...any) (string, error) {
	value, options, err := templateStringOptionsArguments("base64Decode", values)
	if err != nil {
		return "", err
	}
	return Base64Decode(value, options)
}

func exprBase64Encode(values ...any) (any, error) {
	value, options, err := stringOptionsArguments("base64Encode", values)
	if err != nil {
		return nil, err
	}
	return Base64Encode(value, options)
}

func exprBase64Decode(values ...any) (any, error) {
	value, options, err := stringOptionsArguments("base64Decode", values)
	if err != nil {
		return nil, err
	}
	return Base64Decode(value, options)
}

func templateHexEncode(values ...any) (string, error) {
	if len(values) != 1 && len(values) != 2 {
		return "", fmt.Errorf("hexEncode expects optional uppercase and a piped value")
	}
	value, ok := values[len(values)-1].(string)
	if !ok {
		return "", fmt.Errorf("hexEncode value must be a string, got %T", values[len(values)-1])
	}
	uppercase := false
	if len(values) == 2 {
		uppercase, ok = values[0].(bool)
		if !ok {
			return "", fmt.Errorf("hexEncode uppercase must be a boolean, got %T", values[0])
		}
	}
	return HexEncode(value, uppercase)
}

func exprHexEncode(values ...any) (any, error) {
	if len(values) != 1 && len(values) != 2 {
		return nil, fmt.Errorf("hexEncode expects a value and optional uppercase boolean")
	}
	value, ok := values[0].(string)
	if !ok {
		return nil, fmt.Errorf("hexEncode value must be a string, got %T", values[0])
	}
	uppercase := false
	if len(values) == 2 {
		uppercase, ok = values[1].(bool)
		if !ok {
			return nil, fmt.Errorf("hexEncode uppercase must be a boolean, got %T", values[1])
		}
	}
	return HexEncode(value, uppercase)
}

func templateDigest(name string, run func(string, map[string]any) (string, error), values ...any) (string, error) {
	value, options, err := templateStringOptionsArguments(name, values)
	if err != nil {
		return "", err
	}
	return run(value, options)
}

func exprDigest(name string, run func(string, map[string]any) (string, error), values ...any) (any, error) {
	value, options, err := stringOptionsArguments(name, values)
	if err != nil {
		return nil, err
	}
	return run(value, options)
}

func templateHMAC(name string, run func(string, string, map[string]any) (string, error), values ...any) (string, error) {
	if len(values) != 2 && len(values) != 3 {
		return "", fmt.Errorf("%s expects a key, optional options, and a piped value", name)
	}
	key, ok := values[0].(string)
	if !ok {
		return "", fmt.Errorf("%s key must be a string, got %T", name, values[0])
	}
	value, ok := values[len(values)-1].(string)
	if !ok {
		return "", fmt.Errorf("%s value must be a string, got %T", name, values[len(values)-1])
	}
	var options map[string]any
	var err error
	if len(values) == 3 {
		options, err = optionalOptions(name, values[1])
	}
	if err != nil {
		return "", err
	}
	return run(value, key, options)
}

func exprHMAC(name string, run func(string, string, map[string]any) (string, error), values ...any) (any, error) {
	if len(values) != 2 && len(values) != 3 {
		return nil, fmt.Errorf("%s expects a value, key, and optional options object", name)
	}
	value, ok := values[0].(string)
	if !ok {
		return nil, fmt.Errorf("%s value must be a string, got %T", name, values[0])
	}
	key, ok := values[1].(string)
	if !ok {
		return nil, fmt.Errorf("%s key must be a string, got %T", name, values[1])
	}
	var options map[string]any
	var err error
	if len(values) == 3 {
		options, err = optionalOptions(name, values[2])
	}
	if err != nil {
		return nil, err
	}
	return run(value, key, options)
}

func utilityInt(name string, value any) (int, error) {
	return integerArgument(name, value)
}

func utilityInt64(name string, value any) (int64, error) {
	result, err := integerArgument(name, value)
	return int64(result), err
}

func utilityString(name string, value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string, got %T", name, value)
	}
	return text, nil
}

// exprText adapts a single-string helper to Expr. The declared Expr signature keeps a
// literal of the wrong type a compile error; the checked assertion turns a dynamically
// typed value, such as vars.<name>, into a named error instead of a bare panic message.
func exprText(name string, run func(string) (string, error)) func(...any) (any, error) {
	return func(values ...any) (any, error) {
		text, err := utilityString(name+" value", values[0])
		if err != nil {
			return nil, err
		}
		return run(text)
	}
}

func exprCount(name string, run func(string) int) func(...any) (any, error) {
	return func(values ...any) (any, error) {
		text, err := utilityString(name+" value", values[0])
		if err != nil {
			return nil, err
		}
		return run(text), nil
	}
}

func templateRomanEncode(values ...any) (string, error) {
	if len(values) != 1 {
		return "", fmt.Errorf("romanEncode expects one integer")
	}
	value, err := utilityInt("Roman numeral value", values[0])
	if err != nil {
		return "", err
	}
	return RomanEncode(value)
}

func templateOrdinal(values ...any) (string, error) {
	if len(values) != 1 {
		return "", fmt.Errorf("ordinal expects one integer")
	}
	value, err := utilityInt64("ordinal value", values[0])
	if err != nil {
		return "", err
	}
	return Ordinal(value), nil
}

func templateBaseConvert(values ...any) (string, error) {
	if len(values) != 3 && len(values) != 4 {
		return "", fmt.Errorf("baseConvert expects source base, target base, optional uppercase, and a piped value")
	}
	from, err := utilityInt("source base", values[0])
	if err != nil {
		return "", err
	}
	to, err := utilityInt("target base", values[1])
	if err != nil {
		return "", err
	}
	uppercase := false
	if len(values) == 4 {
		var ok bool
		uppercase, ok = values[2].(bool)
		if !ok {
			return "", fmt.Errorf("baseConvert uppercase must be a boolean, got %T", values[2])
		}
	}
	value, ok := values[len(values)-1].(string)
	if !ok {
		return "", fmt.Errorf("baseConvert value must be a string, got %T", values[len(values)-1])
	}
	return BaseConvert(value, from, to, uppercase)
}

func exprBaseConvert(values ...any) (any, error) {
	if len(values) != 3 && len(values) != 4 {
		return nil, fmt.Errorf("baseConvert expects a value, source base, target base, and optional uppercase")
	}
	value, ok := values[0].(string)
	if !ok {
		return nil, fmt.Errorf("baseConvert value must be a string, got %T", values[0])
	}
	from, err := utilityInt("source base", values[1])
	if err != nil {
		return nil, err
	}
	to, err := utilityInt("target base", values[2])
	if err != nil {
		return nil, err
	}
	uppercase := false
	if len(values) == 4 {
		uppercase, ok = values[3].(bool)
		if !ok {
			return nil, fmt.Errorf("baseConvert uppercase must be a boolean, got %T", values[3])
		}
	}
	return BaseConvert(value, from, to, uppercase)
}

func adapterUUID(values ...any) (any, error) {
	if len(values) > 1 {
		return nil, fmt.Errorf("uuid expects an optional options object")
	}
	var options map[string]any
	var err error
	if len(values) == 1 {
		options, err = optionalOptions("uuid", values[0])
	}
	if err != nil {
		return nil, err
	}
	return UUID(options)
}

func adapterRandomString(values ...any) (any, error) {
	if len(values) > 2 {
		return nil, fmt.Errorf("randomString expects optional length and charset")
	}
	length := 16
	charset := defaultRandomAlphabet
	var err error
	if len(values) >= 1 {
		length, err = utilityInt("random string length", values[0])
		if err != nil {
			return nil, err
		}
	}
	if len(values) == 2 {
		var ok bool
		charset, ok = values[1].(string)
		if !ok {
			return nil, fmt.Errorf("randomString charset must be a string, got %T", values[1])
		}
	}
	return RandomString(length, charset)
}

func adapterRandomInt(values ...any) (any, error) {
	if len(values) != 2 {
		return nil, fmt.Errorf("randomInt expects minimum and maximum")
	}
	minimum, err := utilityInt64("random integer minimum", values[0])
	if err != nil {
		return nil, err
	}
	maximum, err := utilityInt64("random integer maximum", values[1])
	if err != nil {
		return nil, err
	}
	return RandomInt(minimum, maximum)
}

func adapterRandomToken(values ...any) (any, error) {
	if len(values) > 2 {
		return nil, fmt.Errorf("randomToken expects optional byte count and encoding")
	}
	byteCount := 32
	encoding := "hex"
	var err error
	if len(values) >= 1 {
		byteCount, err = utilityInt("random token byte count", values[0])
		if err != nil {
			return nil, err
		}
	}
	if len(values) == 2 {
		var ok bool
		encoding, ok = values[1].(string)
		if !ok {
			return nil, fmt.Errorf("randomToken encoding must be a string, got %T", values[1])
		}
	}
	return RandomToken(byteCount, encoding)
}

func adapterPassword(values ...any) (any, error) {
	if len(values) > 2 {
		return nil, fmt.Errorf("password expects optional length and options object")
	}
	length := 20
	var options map[string]any
	var err error
	if len(values) >= 1 {
		length, err = utilityInt("password length", values[0])
		if err != nil {
			return nil, err
		}
	}
	if len(values) == 2 {
		options, err = optionalOptions("password", values[1])
	}
	if err != nil {
		return nil, err
	}
	return Password(length, options)
}

func adapterUnixTimestamp(values ...any) (any, error) {
	if len(values) > 1 {
		return nil, fmt.Errorf("unixTimestamp expects an optional unit")
	}
	unit := ""
	if len(values) == 1 {
		var ok bool
		unit, ok = values[0].(string)
		if !ok {
			return nil, fmt.Errorf("unixTimestamp unit must be a string, got %T", values[0])
		}
	}
	return UnixTimestamp(unit)
}
