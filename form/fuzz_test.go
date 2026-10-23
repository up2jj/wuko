//go:build fuzz

package form

import (
	"net/url"
	"strconv"
	"testing"

	"gopkg.in/yaml.v3"
)

// FuzzDecodeResolveSubmit checks that form declarations and browser submissions, however
// malformed, produce errors rather than panics.
func FuzzDecodeResolveSubmit(f *testing.F) {
	f.Add([]byte("fields:\n  - variable: name\n    type: string\n    pattern: '^v'\n    min_length: 2\n"), "")
	f.Add([]byte("fields:\n  - variable: env\n    type: string\n    choices: [{value: a}, {value: b}]\n"), "a")
	f.Add([]byte("fields:\n  - variable: items\n    type: array\n"), "[1,2] {}")
	f.Add([]byte("fields:\n  - variable: count\n    type: number\n"), "1e400")
	f.Fuzz(func(t *testing.T, data []byte, submitted string) {
		var document yaml.Node
		if yaml.Unmarshal(data, &document) != nil || len(document.Content) == 0 {
			return
		}
		vars := map[string]any{"name": "x", "env": "a", "items": []any{}, "count": 1}
		definition, err := Decode(document.Content[0], vars)
		if err != nil {
			return
		}
		fields, err := definition.Resolve(vars, nil)
		if err != nil {
			return
		}
		values := url.Values{}
		for index := range fields {
			values.Set("field_"+strconv.Itoa(index), submitted)
		}
		_, _ = Submit(fields, values)
	})
}
