//go:build fuzz

package expression

import "testing"

// FuzzEval checks that no expression panics or exhausts memory, whatever the source and the
// untyped values it reads.
func FuzzEval(f *testing.F) {
	for _, source := range []string{
		`indent(vars.s, vars.n)`, `chunk(vars.list, vars.n)`, `repeat(vars.s, vars.n)`, `truncate(vars.s, vars.n)`,
		`parseJSON(vars.s)`, `toJSON(vars)`, `toYAML(vars)`, `parseTime(vars.s)`, `randomInt(0, vars.n)`,
		`base64Decode(vars.s)`, `get(vars, vars.s)`, `dict("a", 1, vars.s)`, `keys(vars.s)`, `sortAlpha(vars.list)`,
		`vars.n / 0`, `vars.n % 0`, `1 << 70`, `vars.list[vars.n]`, `split(vars.s, "")`, `slugify(vars.s)`,
		`normalizeUnicode(vars.s, "NFKC")`, `randomInt(0, 9223372036854775808.0)`,
	} {
		f.Add(source, "a,b\nc", int64(3), 2.5)
	}
	f.Fuzz(func(t *testing.T, source, text string, number int64, real float64) {
		environment := map[string]any{"vars": map[string]any{
			"s": text, "n": number, "f": real,
			"list": []any{text, number, real, nil, map[string]any{"k": text}},
		}}
		_, _ = Eval(source, environment)
	})
}
