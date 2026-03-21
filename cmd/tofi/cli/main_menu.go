package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// clearScreen clears the terminal screen.
func clearScreen() {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("cmd", "/c", "cls")
		cmd.Stdout = os.Stdout
		_ = cmd.Run()
	} else {
		fmt.Print("\033[H\033[2J")
	}
}

// --- Main Menu TUI ---

type menuItem struct {
	id   string
	name string
	desc string
}

var mainMenuItems = []menuItem{
	{"chat", "Chat", "Interactive AI conversation"},
	{"apps", "Apps", "Manage AI applications"},
	{"settings", "Settings", "Keys, models, connections"},
	{"engine", "Engine", "Status, start, stop, restart"},
}

// --- Model ---

type mainMenuModel struct {
	cursor     int
	selected   string
	exitReason tuiExitReason
	ctrlC      ctrlCGuard
}

func newMainMenuModel() *mainMenuModel {
	return &mainMenuModel{}
}

func (m *mainMenuModel) Init() tea.Cmd {
	return nil
}

func (m *mainMenuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		case "esc", "q":
			m.exitReason = exitQuit
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(mainMenuItems)-1 {
				m.cursor++
			}
		case "enter":
			m.selected = mainMenuItems[m.cursor].id
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

func (m *mainMenuModel) View() string {
	header := logoText + "  " + subtitleStyle.Render("AI App Engine")
	version := subtitleStyle.Render("v" + Version)

	var items string
	for i, item := range mainMenuItems {
		label := fmt.Sprintf("%-16s %s", item.name, subtitleStyle.Render(item.desc))
		if i == m.cursor {
			items += tuiSelectedRow.Render("► "+label) + "\n"
		} else {
			items += "  " + label + "\n"
		}
	}

	footer := subtitleStyle.Render("↑↓ navigate · enter select · esc quit")

	content := header + "\n" + version + "\n\n" + items + "\n" + footer

	warn := ""
	if m.ctrlC.IsArmed() {
		warn = "\n" + m.ctrlC.RenderWarning()
	}

	return "\n" + tuiBoxStyle.Render(content) + warn + "\n"
}

// --- Outer Loop ---

// runMainMenuLoop runs the main menu in a loop. When a section exits with
// exitToMenu, the loop re-displays the main menu. exitQuit breaks the loop.
func runMainMenuLoop(cmd *cobra.Command) error {
	for {
		model := newMainMenuModel()
		p := tea.NewProgram(model)
		if _, err := p.Run(); err != nil {
			return err
		}

		if model.exitReason == exitQuit || model.selected == "" {
			return nil
		}

		clearScreen()
		reason, err := runSection(cmd, model.selected)
		if err != nil {
			return err
		}
		if reason == exitQuit {
			return nil
		}
		// exitToMenu → loop continues, re-show main menu
		clearScreen()
	}
}

// runSection dispatches to the appropriate TUI section and returns its exit reason.
func runSection(cmd *cobra.Command, section string) (tuiExitReason, error) {
	switch section {
	case "chat":
		return runChatSection(cmd, nil)
	case "apps":
		return runAppSection(cmd)
	case "settings":
		return runSettingsLoop(cmd)
	case "engine":
		return runEngineSection(cmd)
	default:
		return exitToMenu, nil
	}
}

// --- Settings Loop ---

type settingsItem struct {
	id   string
	name string
	desc string
}

var settingsMenuItems = []settingsItem{
	{"connections", "Connections", "Telegram, Slack, Discord, Email"},
	{"keys", "AI Keys", "Manage API keys"},
	{"models", "Models", "Enable models, set default"},
	{"autostart", "Auto Start", "Start engine on boot"},
}

type settingsMenuModel struct {
	cursor     int
	selected   string
	exitReason tuiExitReason
	ctrlC      ctrlCGuard
}

func newSettingsMenuModel() *settingsMenuModel {
	return &settingsMenuModel{}
}

func (m *settingsMenuModel) Init() tea.Cmd {
	return nil
}

func (m *settingsMenuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			if m.cursor < len(settingsMenuItems)-1 {
				m.cursor++
			}
		case "enter":
			m.selected = settingsMenuItems[m.cursor].id
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

func (m *settingsMenuModel) View() string {
	var items string
	for i, item := range settingsMenuItems {
		label := fmt.Sprintf("%-16s %s", item.name, subtitleStyle.Render(item.desc))
		if i == m.cursor {
			items += tuiSelectedRow.Render("► "+label) + "\n"
		} else {
			items += "  " + label + "\n"
		}
	}

	footer := subtitleStyle.Render("↑↓ navigate · enter select · esc back")

	content := items + "\n" + footer

	warn := ""
	if m.ctrlC.IsArmed() {
		warn = "\n" + m.ctrlC.RenderWarning()
	}

	return "\n" + renderTUIBox("Settings", content) + warn + "\n"
}

// runSettingsLoop runs the settings menu in a loop.
func runSettingsLoop(cmd *cobra.Command) (tuiExitReason, error) {
	for {
		model := newSettingsMenuModel()
		p := tea.NewProgram(model)
		if _, err := p.Run(); err != nil {
			return exitQuit, err
		}

		if model.exitReason == exitQuit {
			return exitQuit, nil
		}
		if model.selected == "" {
			// ESC at settings menu → back to main menu
			return exitToMenu, nil
		}

		clearScreen()
		var subErr error
		switch model.selected {
		case "connections":
			subErr = runConnConfigure(cmd, nil)
		case "keys":
			subErr = runConfigWizardAt(cmd, cfgStepKeys)
		case "models":
			subErr = runConfigWizardAt(cmd, cfgStepModels)
		case "autostart":
			subErr = runConfigWizardAt(cmd, cfgStepAutoStart)
		}
		if subErr != nil {
			fmt.Println(errorStyle.Render("  ✗ " + subErr.Error()))
		}
		// Loop continues → re-show settings menu
		clearScreen()
	}
}

// runConfigWizardAt launches the config wizard starting at a specific step.
func runConfigWizardAt(cmd *cobra.Command, startStep cfgStep) error {
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ " + err.Error()))
		fmt.Println()
		return err
	}

	model := newCfgModel(client)
	model.step = startStep
	model.startStep = startStep

	p := tea.NewProgram(model)
	_, err := p.Run()
	return err
}

// greenDot is a green bullet for status display
var greenDot = lipgloss.NewStyle().Foreground(lipgloss.Color("#3fb950")).Render("●")

// grayDot is a gray bullet for stopped status
var grayDot = subtitleStyle.Render("○")
