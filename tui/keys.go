package tui

import tea "charm.land/bubbletea/v2"

const cancelHelp = "ctrl+c cancel"

func isCancelKey(key tea.KeyPressMsg) bool {
	return key.String() == "ctrl+c"
}
