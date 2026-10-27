package tui

import (
	"image/color"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

func TestPickerLogoRequiresMatchingGraphicsResponse(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		message tea.Msg
	}{
		{"keyboard", tea.KeyboardEnhancementsMsg{}},
		{"other query", uv.KittyGraphicsEvent{Options: kitty.Options{ID: pickerLogoQueryID + 1}, Payload: []byte("OK")}},
		{"unsupported", uv.KittyGraphicsEvent{Options: kitty.Options{ID: pickerLogoQueryID}, Payload: []byte("ENOTSUP")}},
		{"unsolicited tick", pickerLogoTickMsg{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			logo := pickerLogo{enabled: true}
			_, command := logo.update(test.message)
			if command != nil || logo.supported || logo.title("Workflows", true) != "Workflows" {
				t.Fatal("unrelated or unsuccessful response enabled animation")
			}
		})
	}
	logo := pickerLogo{enabled: true}
	query := logo.query()().(tea.RawMsg).Msg.(string)
	for _, want := range []string{"a=q", "i=" + strconv.Itoa(pickerLogoQueryID), ";AAAA"} {
		if !strings.Contains(query, want) {
			t.Fatalf("query = %q, want %q", query, want)
		}
	}
}

func TestPickerLogoAnimatesAfterGraphicsResponse(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const title = "Wuko v1.2.3"
		model := newSelectionModelWithConfig(SelectionPickerConfig{Title: title, AnimateLogo: true, Options: []Option{{Label: "build"}}})
		// Set the screen's appearance explicitly so this test is independent of NO_COLOR.
		model.logo.enabled = true
		model.plain = false
		model.styles = newInteractiveStyles(true, false)
		updated, command := model.Update(uv.KittyGraphicsEvent{Options: kitty.Options{ID: pickerLogoQueryID}, Payload: []byte("OK")})
		model = updated.(selectionModel)
		if command == nil || !strings.Contains(ansi.Strip(model.View().Content), "W  "+title) {
			t.Fatal("successful graphics response did not start the logo")
		}
		if !strings.Contains(model.View().Content, model.styles.message.Render(title)) {
			t.Fatal("logo reset the title's style")
		}
		first := model.View().Content
		updated, next := model.Update(command())
		model = updated.(selectionModel)
		if next == nil || first == model.View().Content || ansi.Strip(first) != ansi.Strip(model.View().Content) {
			t.Fatal("animation must change color without moving the title or workflow rows")
		}
		_, duplicate := model.Update(uv.KittyGraphicsEvent{Options: kitty.Options{ID: pickerLogoQueryID}, Payload: []byte("OK")})
		if duplicate != nil {
			t.Fatal("duplicate response started another animation loop")
		}
		updated, _ = model.Update(tea.BackgroundColorMsg{Color: color.White})
		model = updated.(selectionModel)
		if model.View().Content == first || !strings.Contains(ansi.Strip(model.View().Content), "W  "+title) {
			t.Fatal("background reply lost the logo")
		}
		for _, size := range []tea.WindowSizeMsg{{Width: 40, Height: 20}, {Width: 80, Height: 8}} {
			updated, _ = model.Update(size)
			if strings.Contains(ansi.Strip(updated.View().Content), "W  "+title) {
				t.Fatal("compact picker shows logo")
			}
		}
		for _, key := range []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: 'c', Mod: tea.ModCtrl}} {
			updated, _ = model.Update(key)
			updated, command = updated.Update(pickerLogoTickMsg{})
			if command != nil || updated.View().Content != "" {
				t.Fatalf("%s left the animation running", key.String())
			}
		}
	})
}

func TestPickerLogoRespectsNoColorAndOptIn(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	model := newSelectionModelWithConfig(SelectionPickerConfig{Title: "Workflows", AnimateLogo: true})
	if model.logo.query() != nil {
		t.Fatal("NO_COLOR issued a graphics query")
	}
	updated, command := model.Update(uv.KittyGraphicsEvent{Options: kitty.Options{ID: pickerLogoQueryID}, Payload: []byte("OK")})
	if command != nil || strings.Contains(ansi.Strip(updated.View().Content), "W  Workflows") {
		t.Fatal("NO_COLOR enabled animation")
	}
	t.Setenv("NO_COLOR", "")
	model = newSelectionModel("Workflows", nil)
	if model.logo.query() != nil {
		t.Fatal("generic picker issued a graphics query")
	}
}
