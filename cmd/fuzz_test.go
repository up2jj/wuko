//go:build fuzz

package cmd

import (
	"context"
	"testing"

	"github.com/up2jj/wuko/step"
	"go.yaml.in/yaml/v3"
)

// FuzzBuiltinStepConfigs builds every built-in step type from arbitrary `with:` maps. It only
// builds runners and never runs them, so the fuzzer cannot execute commands.
func FuzzBuiltinStepConfigs(f *testing.F) {
	registry := step.NewRegistry()
	if err := registerBuiltinSteps(registry); err != nil {
		f.Fatal(err)
	}
	names := registry.Names()
	for index, config := range []string{
		"{}", "command: x", "script: x", "url: http://x\nquery: {a: 1}", "path: x\ncontent: y",
		"items: [1, 2]", "value: null", "source: 'return 1'", "args: [1, null, {a: b}]",
		"fields: [{id: a, type: text}]", "choices: [{value: 1}, null]", "env: {A: [1]}", "pattern: '('",
		"files: {a: null}", "headers: {A: {b: c}}", "capture_limit: 0B", "stdout: weird",
	} {
		f.Add(uint16(index), []byte(config))
	}
	f.Fuzz(func(t *testing.T, which uint16, data []byte) {
		var raw map[string]any
		if yaml.Unmarshal(data, &raw) != nil {
			return
		}
		name := names[int(which)%len(names)]
		_, _ = registry.Build(name, raw)
		_, _ = registry.OutputSchemaForConfig(context.Background(), name, raw)
	})
}
