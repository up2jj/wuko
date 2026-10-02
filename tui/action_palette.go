package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// actionPalette is the shared contextual action view used by selection pickers and dashboards.
// Its caller owns selection and execution; the palette owns only navigation and presentation.
type actionPalette struct {
	Title       string
	Description string
	Path        string
	Actions     []SelectionAction
	Cursor      int
	Error       string
	Width       int
	Height      int
}

func (palette actionPalette) move(key string) (int, bool) {
	last := max(len(palette.Actions)-1, 0)
	switch key {
	case "up", "k":
		return max(palette.Cursor-1, 0), true
	case "down", "j":
		return min(palette.Cursor+1, last), true
	case "home":
		return 0, true
	case "end":
		return last, true
	case "pgup":
		return max(palette.Cursor-palette.pageSize(), 0), true
	case "pgdown":
		return min(palette.Cursor+palette.pageSize(), last), true
	default:
		return palette.Cursor, false
	}
}

func (palette actionPalette) view() string {
	var view strings.Builder
	view.WriteString(interactiveStyles.message.Render("Actions — "+palette.Title) + "\n")
	if palette.Description != "" {
		view.WriteString(interactiveStyles.description.Render(palette.Description) + "\n")
	}
	if palette.Path != "" {
		view.WriteString(interactiveStyles.description.Render(palette.Path) + "\n")
	}
	view.WriteByte('\n')
	start, end := palette.visibleRange()
	for index := start; index < end; index++ {
		action := palette.Actions[index]
		prefix := "  "
		if index == palette.Cursor {
			prefix = interactiveStyles.cursor.Render("> ")
		}
		label := action.Label
		description := action.Description
		if action.Disabled {
			label = interactiveStyles.disabled.Render(label)
			if description != "" {
				description += " • "
			}
			description += "disabled: " + action.DisabledReason
			description = interactiveStyles.disabled.Render(description)
		} else if index == palette.Cursor {
			label = interactiveStyles.selected.Render(label)
			if description != "" {
				description = interactiveStyles.description.Render(description)
			}
		} else {
			label = interactiveStyles.content.Render(label)
			if description != "" {
				description = interactiveStyles.description.Render(description)
			}
		}
		line := prefix + label
		if description != "" {
			line += " " + interactiveStyles.description.Render("—") + " " + description
		}
		view.WriteString(ansi.Truncate(line, max(palette.Width, 1), "…") + "\n")
	}
	if palette.Error != "" {
		view.WriteString(interactiveStyles.error.Render(palette.Error) + "\n")
	}
	view.WriteString(renderHelpText(wrapHelp(actionPaletteHelp, palette.Width)))
	return view.String()
}

func (palette actionPalette) visibleRange() (int, int) {
	page := palette.pageSize()
	length := len(palette.Actions)
	if length <= page {
		return 0, length
	}
	start := max(palette.Cursor-page/2, 0)
	start = min(start, length-page)
	return start, start + page
}

func (palette actionPalette) pageSize() int {
	headerLines := 2
	if palette.Description != "" {
		headerLines++
	}
	if palette.Path != "" {
		headerLines++
	}
	if palette.Error != "" {
		headerLines++
	}
	helpLines := strings.Count(wrapHelp(actionPaletteHelp, palette.Width), "\n")
	return max(palette.Height-headerLines-helpLines, 1)
}

var actionPaletteHelp = []string{"↑/↓ move", "enter select", "esc back", cancelHelp}
