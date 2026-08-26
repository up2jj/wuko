package shell

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/up2jj/wuko/process"
)

type terminalConfig struct {
	Background string `yaml:"background,omitempty"`
	Foreground string `yaml:"foreground,omitempty"`
	Title      string `yaml:"title,omitempty"`
}

var terminalNamedColors = map[string]string{
	"aqua": "#00ffff", "black": "#000000", "blue": "#0000ff", "cyan": "#00ffff",
	"fuchsia": "#ff00ff", "gray": "#808080", "green": "#008000", "grey": "#808080",
	"lime": "#00ff00", "magenta": "#ff00ff", "maroon": "#800000", "navy": "#000080",
	"olive": "#808000", "orange": "#ffa500", "purple": "#800080", "red": "#ff0000",
	"silver": "#c0c0c0", "teal": "#008080", "white": "#ffffff", "yellow": "#ffff00",
}

func validateTerminalConfig(config *terminalConfig, tty, handoff bool) error {
	if config == nil {
		return nil
	}
	if !tty {
		return fmt.Errorf("terminal requires tty")
	}
	if !handoff {
		return fmt.Errorf("terminal requires user handoff; set interact to true")
	}
	if config.Background == "" && config.Foreground == "" && config.Title == "" {
		return fmt.Errorf("terminal must configure background, foreground, or title")
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{name: "background", value: &config.Background},
		{name: "foreground", value: &config.Foreground},
	} {
		if *field.value == "" || templated(*field.value) {
			continue
		}
		normalized, ok := normalizeTerminalColor(*field.value)
		if !ok {
			return fmt.Errorf("terminal %s must be #RGB, #RRGGBB, rgb(r, g, b), or a supported color name", field.name)
		}
		*field.value = normalized
	}
	if strings.IndexFunc(config.Title, unicode.IsControl) >= 0 {
		return fmt.Errorf("terminal title must not contain control characters")
	}
	return nil
}

func normalizeTerminalColor(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if named, ok := terminalNamedColors[strings.ToLower(value)]; ok {
		return named, true
	}
	if strings.HasPrefix(value, "#") {
		value = strings.ToLower(value)
		switch len(value) {
		case 4:
			if !allHexDigits(value[1:]) {
				return "", false
			}
			return fmt.Sprintf("#%c%c%c%c%c%c", value[1], value[1], value[2], value[2], value[3], value[3]), true
		case 7:
			if !allHexDigits(value[1:]) {
				return "", false
			}
			return value, true
		default:
			return "", false
		}
	}

	lower := strings.ToLower(value)
	if !strings.HasPrefix(lower, "rgb(") || !strings.HasSuffix(lower, ")") {
		return "", false
	}
	parts := strings.Split(value[4:len(value)-1], ",")
	if len(parts) != 3 {
		return "", false
	}
	var channels [3]int
	for index, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || strings.IndexFunc(part, func(value rune) bool { return value < '0' || value > '9' }) >= 0 {
			return "", false
		}
		channel, err := strconv.Atoi(part)
		if err != nil || channel > 255 {
			return "", false
		}
		channels[index] = channel
	}
	return fmt.Sprintf("#%02x%02x%02x", channels[0], channels[1], channels[2]), true
}

func allHexDigits(value string) bool {
	return strings.IndexFunc(value, func(value rune) bool {
		return !(value >= '0' && value <= '9' || value >= 'A' && value <= 'F' || value >= 'a' && value <= 'f')
	}) < 0
}

func terminalAppearance(config *terminalConfig) *process.TerminalAppearance {
	if config == nil {
		return nil
	}
	return &process.TerminalAppearance{
		Background: config.Background,
		Foreground: config.Foreground,
		Title:      config.Title,
	}
}
