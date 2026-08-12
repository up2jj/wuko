package shell

import (
	"io"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestInlineShellArgumentsAndEnvironment(t *testing.T) {
	runner, err := New(map[string]any{
		"script": `printf '%s:%s' "$1" "$VALUE"`,
		"args":   []any{"argument"},
		"env":    map[string]any{"VALUE": "step"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		RunDir: t.TempDir(), Env: map[string]string{"VALUE": "base"}, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["stdout"]; got != "argument:step" {
		t.Fatalf("stdout = %q", got)
	}
}
