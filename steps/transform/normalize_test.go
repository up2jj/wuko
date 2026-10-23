package transform

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

type namedString string
type namedInt int16

type taggedRecord struct {
	Name   string `json:"name"`
	Hidden string `json:"-"`
	Count  int    `json:"count,omitempty"`
}

type selfEncoding []int

func (selfEncoding) MarshalJSON() ([]byte, error) { return []byte(`"encoded"`), nil }

// The direct walk must reproduce the JSON round trip it replaces, value for value and error
// for error.
func TestNormalizeResultMatchesJSONRoundTrip(t *testing.T) {
	t.Parallel()
	var nilList []any
	var nilObject map[string]any
	var nilTyped []string
	var nilPointer *int
	seven := 7
	tests := map[string]any{
		"null":                   nil,
		"bool":                   true,
		"string":                 "text",
		"invalid utf-8":          "a\xffb\xfe",
		"invalid utf-8 key":      map[string]any{"k\xff": 1},
		"int":                    42,
		"negative zero":          math.Copysign(0, -1),
		"integral float":         2.0,
		"fraction":               0.1,
		"tiny":                   1e-7,
		"large integral float":   1e20,
		"exponent float":         1e21,
		"max int float":          9.223372036854775e18,
		"min int float":          -9.223372036854775808e18,
		"NaN":                    math.NaN(),
		"infinity":               math.Inf(1),
		"float32":                float32(0.1),
		"json integer":           json.Number("12"),
		"json float":             json.Number("1.50"),
		"json exponent":          json.Number("1E3"),
		"json negative zero":     json.Number("-0"),
		"json out of range":      json.Number("99999999999999999999"),
		"json plus":              json.Number("+1"),
		"json leading dot":       json.Number(".5"),
		"json trailing dot":      json.Number("1."),
		"json hex float":         json.Number("0x1.8p1"),
		"json leading zero":      json.Number("01"),
		"json empty":             json.Number(""),
		"int8":                   int8(-8),
		"uint64 max":             uint64(math.MaxUint64),
		"uint8":                  uint8(200),
		"duration":               2 * time.Second,
		"named string":           namedString("named"),
		"named int":              namedInt(5),
		"bytes":                  []byte("hello"),
		"byte array":             [3]byte{1, 2, 3},
		"typed list":             []string{"a", "b"},
		"typed object":           map[string]int{"a": 1},
		"named key object":       map[namedString]int{"a": 1},
		"nested typed":           map[string][]float64{"a": {1, 1.5}},
		"nil list":               nilList,
		"nil object":             nilObject,
		"nil typed list":         nilTyped,
		"empty list":             []any{},
		"empty object":           map[string]any{},
		"pointer":                &seven,
		"nil pointer":            nilPointer,
		"struct":                 taggedRecord{Name: "a", Hidden: "secret"},
		"time":                   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		"self encoding":          selfEncoding{1},
		"func":                   func() {},
		"non-string keys":        map[int]any{1: "a"},
		"nested non-string keys": []any{map[string]any{"a": map[int]int{1: 1}}},
		"mixed": []any{
			map[string]any{"id": json.Number("1"), "score": json.Number("0.5"), "tags": []any{"x", nil, true}},
			[]any{2.0, 3.5, int64(4)},
		},
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertNormalizesLikeJSON(t, value)
		})
	}
}

func TestNormalizeResultMatchesJSONRoundTripOnRandomTrees(t *testing.T) {
	t.Parallel()
	random := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		assertNormalizesLikeJSON(t, randomTree(random, 0))
	}
}

func randomTree(random *rand.Rand, depth int) any {
	choice := random.IntN(12)
	if depth > 3 {
		choice %= 8
	}
	switch choice {
	case 0:
		return nil
	case 1:
		return random.IntN(2) == 0
	case 2:
		return strconv.Itoa(random.IntN(100))
	case 3:
		return random.IntN(1000) - 500
	case 4:
		return float64(random.IntN(100)) / float64(1+random.IntN(4))
	case 5:
		return json.Number(strconv.Itoa(random.IntN(1000)))
	case 6:
		return json.Number(strconv.FormatFloat(random.Float64()*100, 'f', random.IntN(4), 64))
	case 7:
		return int64(random.IntN(1000))
	case 8, 9:
		list := make([]any, random.IntN(5))
		for index := range list {
			list[index] = randomTree(random, depth+1)
		}
		return list
	default:
		size := random.IntN(5)
		object := make(map[string]any, size)
		for index := range size {
			object["k"+strconv.Itoa(index)] = randomTree(random, depth+1)
		}
		return object
	}
}

func assertNormalizesLikeJSON(t *testing.T, value any) {
	t.Helper()
	got, gotErr := normalizeResult(value)
	want, wantErr := referenceNormalize(value)
	if (gotErr != nil) != (wantErr != nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
		t.Fatalf("normalizeResult(%#v) error = %v, want %v", value, gotErr, wantErr)
	}
	if diff := cmp.Diff(want, got); gotErr == nil && diff != "" {
		t.Fatalf("normalizeResult(%#v) mismatch (-want +got):\n%s", value, diff)
	}
}

// referenceNormalize is the normalization transform used before the direct walk.
func referenceNormalize(value any) (any, error) {
	if err := validateStringMapKeys(reflect.ValueOf(value)); err != nil {
		return nil, err
	}
	return normalizeViaJSON(value)
}

// Operations pass source items and literal operands through by reference, so the result
// must be built from new lists and objects that the caller can change freely.
func TestNormalizeResultSharesNothingWithItsInput(t *testing.T) {
	t.Parallel()
	inner := map[string]any{"n": json.Number("1")}
	input := []any{inner, []any{inner}}
	result, err := normalizeResult(input)
	if err != nil {
		t.Fatal(err)
	}
	result.([]any)[0].(map[string]any)["n"] = "changed"
	result.([]any)[1].([]any)[0] = "changed"
	if inner["n"] != json.Number("1") || input[1].([]any)[0].(map[string]any)["n"] != json.Number("1") {
		t.Fatalf("changing the result changed its input: %#v", input)
	}
}
