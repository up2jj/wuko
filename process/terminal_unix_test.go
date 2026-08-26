//go:build darwin || linux

package process

import (
	"bytes"
	"testing"
)

func TestTerminalRGB(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "#1e1e2e", want: "rgb:1e/1e/2e", ok: true},
		{input: "#CDD6F4", want: "rgb:CD/D6/F4", ok: true},
		{input: "#123"},
		{input: "#12345g"},
		{input: "red"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, ok := terminalRGB(test.input)
			if got != test.want || ok != test.ok {
				t.Fatalf("terminalRGB(%q) = %q, %v; want %q, %v", test.input, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestApplyTerminalAppearanceSkipsNonTerminalWriter(t *testing.T) {
	var output bytes.Buffer
	restore := applyTerminalAppearance(&output, &TerminalAppearance{Background: "#112233", Title: "Console"})
	if restore != nil || output.Len() != 0 {
		t.Fatalf("restore = %v, output = %q; want no terminal styling", restore != nil, output.String())
	}
}

func TestApplyTerminalAppearanceDropsUnsafeValues(t *testing.T) {
	terminal, _ := testTerminal(t, 24, 80)
	output := &terminalReadyOutput{readyOutput: &readyOutput{ready: make(chan struct{})}, terminal: terminal}
	restore := applyTerminalAppearance(output, &TerminalAppearance{
		Background: "\x1b]11;red", Foreground: "red", Title: "unsafe\x1b]2;title",
	})
	if restore != nil || output.String() != "" {
		t.Fatalf("restore = %v, output = %q; want unsafe appearance dropped", restore != nil, output.String())
	}
}
