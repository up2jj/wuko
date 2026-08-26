//go:build darwin || linux

package process

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"golang.org/x/term"
)

// TerminalAppearance describes best-effort outer-terminal styling during a live PTY handoff.
type TerminalAppearance struct {
	Background string
	Foreground string
	Title      string
}

type terminalWriter interface {
	io.Writer
	Fd() uintptr
}

func applyTerminalAppearance(writer io.Writer, appearance *TerminalAppearance) func() {
	if appearance == nil {
		return nil
	}
	terminal, ok := writer.(terminalWriter)
	if !ok || !term.IsTerminal(int(terminal.Fd())) {
		return nil
	}

	var apply strings.Builder
	hasTitle := appearance.Title != "" && strings.IndexFunc(appearance.Title, unicode.IsControl) < 0
	hasForeground := false
	hasBackground := false
	if hasTitle {
		apply.WriteString("\x1b[22;2t")
		fmt.Fprintf(&apply, "\x1b]2;%s\x1b\\", appearance.Title)
	}
	if color, ok := terminalRGB(appearance.Foreground); ok {
		fmt.Fprintf(&apply, "\x1b]10;%s\x1b\\", color)
		hasForeground = true
	}
	if color, ok := terminalRGB(appearance.Background); ok {
		fmt.Fprintf(&apply, "\x1b]11;%s\x1b\\", color)
		hasBackground = true
	}
	if apply.Len() == 0 {
		return nil
	}
	var restore strings.Builder
	if hasBackground {
		restore.WriteString("\x1b]111\x1b\\")
	}
	if hasForeground {
		restore.WriteString("\x1b]110\x1b\\")
	}
	if hasTitle {
		restore.WriteString("\x1b[23;2t")
	}
	_, _ = io.WriteString(terminal, apply.String())
	return func() { _, _ = io.WriteString(terminal, restore.String()) }
}

func terminalRGB(color string) (string, bool) {
	if len(color) != 7 || color[0] != '#' {
		return "", false
	}
	for _, value := range color[1:] {
		if !isHexDigit(value) {
			return "", false
		}
	}
	return fmt.Sprintf("rgb:%s/%s/%s", color[1:3], color[3:5], color[5:7]), true
}

func isHexDigit(value rune) bool {
	return value >= '0' && value <= '9' || value >= 'A' && value <= 'F' || value >= 'a' && value <= 'f'
}
