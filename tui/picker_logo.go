package tui

import (
	"image/color"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// The query does not store or display an image. Match its ID so unrelated terminal
// replies (including Kitty keyboard enhancements) cannot enable the animation.
const pickerLogoQueryID = 0x57756b

const pickerLogoInterval = 150 * time.Millisecond

var pickerLogoBrightness = [...]uint8{150, 160, 180, 205, 230, 248, 255, 248, 230, 205, 180, 160}

type pickerLogoTickMsg struct{}

type pickerLogo struct {
	enabled   bool
	supported bool
	frame     int
}

func (logo pickerLogo) query() tea.Cmd {
	if !logo.enabled {
		return nil
	}
	return tea.Raw(ansi.KittyGraphics([]byte("AAAA"), "a=q", "t=d", "f=24", "s=1", "v=1", "i="+strconv.Itoa(pickerLogoQueryID)))
}

func (logo *pickerLogo) update(message tea.Msg) (bool, tea.Cmd) {
	if !logo.enabled {
		return false, nil
	}
	switch message := message.(type) {
	case uv.KittyGraphicsEvent:
		if message.Options.ID != pickerLogoQueryID {
			return false, nil
		}
		if logo.supported || string(message.Payload) != "OK" {
			return true, nil
		}
		logo.supported = true
	case pickerLogoTickMsg:
		if !logo.supported {
			return true, nil
		}
		logo.frame = (logo.frame + 1) % len(pickerLogoBrightness)
	default:
		return false, nil
	}
	return true, tea.Tick(pickerLogoInterval, func(time.Time) tea.Msg { return pickerLogoTickMsg{} })
}

func (logo pickerLogo) title(title string, dark bool) string {
	if !logo.enabled || !logo.supported {
		return title
	}
	accent := color.RGBA{R: 0x67, G: 0xd6, B: 0xee, A: 255}
	if !dark {
		accent = color.RGBA{R: 0x08, G: 0x7e, B: 0x99, A: 255}
	}
	brightness := uint16(pickerLogoBrightness[logo.frame])
	accent.R = uint8(uint16(accent.R) * brightness / 255)
	accent.G = uint8(uint16(accent.G) * brightness / 255)
	accent.B = uint8(uint16(accent.B) * brightness / 255)
	return lipgloss.NewStyle().Foreground(accent).Bold(true).Render("W") + "  " + title
}
