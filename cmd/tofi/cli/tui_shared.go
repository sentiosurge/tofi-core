package cli

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ctrlCResetMsg is sent after the Ctrl+C double-press timeout expires.
type ctrlCResetMsg struct{}

// ctrlCGuard manages the double-press Ctrl+C quit pattern.
// Embed this in any bubbletea model that needs it.
type ctrlCGuard struct {
	pressed bool
}

// HandleCtrlC processes a Ctrl+C keypress. Returns (should quit, tea.Cmd).
// First press: arms the guard, returns a 3-second reset timer.
// Second press within 3 seconds: returns quit=true.
func (g *ctrlCGuard) HandleCtrlC() (quit bool, cmd tea.Cmd) {
	if g.pressed {
		return true, nil
	}
	g.pressed = true
	return false, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return ctrlCResetMsg{} })
}

// HandleReset resets the guard when the timeout fires or another key is pressed.
func (g *ctrlCGuard) HandleReset() {
	g.pressed = false
}

// IsArmed returns true if waiting for the second Ctrl+C press.
func (g *ctrlCGuard) IsArmed() bool {
	return g.pressed
}

// RenderWarning returns the warning text to display when armed.
func (g *ctrlCGuard) RenderWarning() string {
	return errorStyle.Render("  Press Ctrl+C again to quit")
}
