package lua

import (
	"fmt"
	"math"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/internal/dataformat"
	glua "github.com/yuin/gopher-lua"
)

func dataFormatModules(state *glua.LState, module *glua.LTable) {
	yamlModule := state.NewTable()
	state.SetFuncs(yamlModule, map[string]glua.LGFunction{"decode": yamlDecode, "encode": yamlEncode})
	module.RawSetString("yaml", yamlModule)
	tomlModule := state.NewTable()
	state.SetFuncs(tomlModule, map[string]glua.LGFunction{"decode": tomlDecode, "encode": tomlEncode})
	module.RawSetString("toml", tomlModule)
}

func yamlDecode(state *glua.LState) int {
	value, err := dataformat.DecodeYAML(state.CheckString(1), true)
	if err != nil {
		state.RaiseError("decoding YAML: %v", err)
		return 0
	}
	return pushDecoded(state, "YAML", value)
}

func tomlDecode(state *glua.LState) int {
	value, err := dataformat.DecodeTOML(state.CheckString(1))
	if err != nil {
		state.RaiseError("decoding TOML: %v", err)
		return 0
	}
	return pushDecoded(state, "TOML", value)
}

func pushDecoded(state *glua.LState, format string, value any) int {
	converted, err := decodedToLua(state, value)
	if err != nil {
		state.RaiseError("decoding %s: %v", format, err)
		return 0
	}
	state.Push(converted)
	return 1
}

func yamlEncode(state *glua.LState) int {
	value, err := encodableValue(state.Get(1))
	if err != nil {
		state.RaiseError("encoding YAML: %v", err)
		return 0
	}
	text, err := expression.ToYAML(value)
	if err != nil {
		state.RaiseError("encoding YAML: %v", err)
		return 0
	}
	state.Push(glua.LString(text))
	return 1
}

func tomlEncode(state *glua.LState) int {
	value, err := encodableValue(state.Get(1))
	if err != nil {
		state.RaiseError("encoding TOML: %v", err)
		return 0
	}
	document, ok := value.(map[string]any)
	if !ok {
		state.RaiseError("encoding TOML: a document must be a table with string keys")
		return 0
	}
	data, err := toml.Marshal(document)
	if err != nil {
		state.RaiseError("encoding TOML: %v", err)
		return 0
	}
	state.Push(glua.LString(data))
	return 1
}

// encodableValue converts a Lua value for an encoder, writing whole numbers as integers
// rather than as floats such as 3.0, since Lua does not tell the two apart.
func encodableValue(value glua.LValue) (any, error) {
	converted, err := fromLua(value, make(map[*glua.LTable]bool))
	if err != nil {
		return nil, err
	}
	return integralNumbers(converted), nil
}

func integralNumbers(value any) any {
	switch typed := value.(type) {
	case float64:
		if typed == math.Trunc(typed) && typed >= math.MinInt64 && typed < math.MaxInt64 {
			return int64(typed)
		}
	case []any:
		for index, item := range typed {
			typed[index] = integralNumbers(item)
		}
	case map[string]any:
		for key, item := range typed {
			typed[key] = integralNumbers(item)
		}
	}
	return value
}

// decodedToLua converts YAML and TOML values that JSON lacks: numbers become Lua numbers,
// dates and times become strings, and YAML mapping keys keep their scalar type. Null list
// items leave holes, so a list keeps its positions.
func decodedToLua(state *glua.LState, value any) (glua.LValue, error) {
	switch typed := value.(type) {
	case nil:
		return glua.LNil, nil
	case string:
		return glua.LString(typed), nil
	case bool:
		return glua.LBool(typed), nil
	case int:
		return glua.LNumber(typed), nil
	case int64:
		return glua.LNumber(typed), nil
	case uint64:
		return glua.LNumber(typed), nil
	case float64:
		return glua.LNumber(typed), nil
	case time.Time:
		if typed.Location() == time.UTC && typed.Equal(typed.Truncate(24*time.Hour)) {
			return glua.LString(typed.Format(time.DateOnly)), nil
		}
		return glua.LString(typed.Format(time.RFC3339Nano)), nil
	case toml.LocalDate:
		return glua.LString(typed.String()), nil
	case toml.LocalDateTime:
		return glua.LString(typed.String()), nil
	case toml.LocalTime:
		return glua.LString(typed.String()), nil
	case []any:
		table := newList(state, len(typed))
		for index, item := range typed {
			converted, err := decodedToLua(state, item)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index+1, err)
			}
			table.RawSetInt(index+1, converted)
		}
		return table, nil
	case map[string]any:
		table := state.CreateTable(0, len(typed))
		for key, item := range typed {
			converted, err := decodedToLua(state, item)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", key, err)
			}
			table.RawSetString(key, converted)
		}
		return table, nil
	case map[any]any:
		table := state.CreateTable(0, len(typed))
		for key, item := range typed {
			luaKey, err := decodedToLua(state, key)
			if err != nil {
				return nil, fmt.Errorf("key %v: %w", key, err)
			}
			if _, table := luaKey.(*glua.LTable); table || luaKey == glua.LNil {
				return nil, fmt.Errorf("mapping key %v must be a scalar", key)
			}
			if number, ok := luaKey.(glua.LNumber); ok && math.IsNaN(float64(number)) {
				return nil, fmt.Errorf("mapping key must not be NaN")
			}
			converted, err := decodedToLua(state, item)
			if err != nil {
				return nil, fmt.Errorf("field %v: %w", key, err)
			}
			table.RawSet(luaKey, converted)
		}
		return table, nil
	default:
		return nil, fmt.Errorf("unsupported value %T", value)
	}
}
