package cli

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

type adminItem struct {
	id   string
	name string
	desc string
}

var adminMenuItems = []adminItem{
	{"users", "Users", "Manage user accounts"},
	{"usage", "Usage", "Token usage by model"},
}

type adminMenuModel struct {
	cursor     int
	selected   string
	exitReason tuiExitReason
	ctrlC      ctrlCGuard
}

func newAdminMenuModel() *adminMenuModel {
	return &adminMenuModel{}
}

func (m *adminMenuModel) Init() tea.Cmd { return nil }

func (m *adminMenuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if quit, cmd := m.ctrlC.HandleCtrlC(); quit {
				m.exitReason = exitQuit
				return m, tea.Quit
			} else {
				return m, cmd
			}
		case "esc":
			m.exitReason = exitToMenu
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(adminMenuItems)-1 {
				m.cursor++
			}
		case "enter":
			m.selected = adminMenuItems[m.cursor].id
			return m, tea.Quit
		default:
			m.ctrlC.HandleReset()
		}
	case ctrlCResetMsg:
		m.ctrlC.HandleReset()
		return m, nil
	}
	return m, nil
}

func (m *adminMenuModel) View() string {
	var items string
	for i, item := range adminMenuItems {
		if i == m.cursor {
			label := fmt.Sprintf("%-16s %s", item.name, tuiSelectedDesc.Render(item.desc))
			items += tuiSelectedRow.Render("► "+label) + "\n"
		} else {
			label := fmt.Sprintf("%-16s %s", item.name, subtitleStyle.Render(item.desc))
			items += "  " + label + "\n"
		}
	}

	footer := subtitleStyle.Render("↑↓ navigate · enter select · esc back")
	content := items + "\n" + footer

	warn := ""
	if m.ctrlC.IsArmed() {
		warn = "\n" + m.ctrlC.RenderWarning()
	}

	return "\n" + renderTUIBox("Admin", content) + warn + "\n"
}

// runAdminSection runs the admin menu loop.
func runAdminSection(cmd *cobra.Command) (tuiExitReason, error) {
	for {
		model := newAdminMenuModel()
		p := tea.NewProgram(model)
		if _, err := p.Run(); err != nil {
			return exitQuit, err
		}

		if model.exitReason == exitQuit {
			return exitQuit, nil
		}
		if model.selected == "" {
			return exitToMenu, nil
		}

		clearScreen()
		var subErr error
		switch model.selected {
		case "users":
			subErr = runUsersSection(cmd)
		case "usage":
			subErr = runUsageSection(cmd)
		}
		if subErr != nil {
			fmt.Println(errorStyle.Render("  ✗ " + subErr.Error()))
		}
		clearScreen()
	}
}
