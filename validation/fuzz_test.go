//go:build fuzz

package validation

import "testing"

// FuzzSourceIndex checks span lookup and redacted excerpts against arbitrary source text, paths,
// and line numbers.
func FuzzSourceIndex(f *testing.F) {
	f.Add([]byte("steps:\n  - id: a\n    with:\n      password: |\n        value\n"), "steps[0].with.password", 5)
	f.Add([]byte("tokens:\n- a\n- b\n"), "tokens[1]", -1)
	f.Add([]byte(""), "", 0)
	f.Fuzz(func(t *testing.T, data []byte, path string, line int) {
		index := NewLazySourceIndex("wuko.yaml", data)
		span := index.Span(Path(path))
		_ = index.Excerpt(span.Line)
		_ = index.Excerpt(line)
		_ = index.Attach(Issue{Path: Path(path)})
	})
}
