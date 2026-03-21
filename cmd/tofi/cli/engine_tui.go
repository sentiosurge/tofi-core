package cli

import (
	"fmt"
	"time"

	"tofi-core/internal/daemon"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

var engineCmd = &cobra.Command{
	Use:   "engine",
	Short: "Engine status and control",
	RunE: func(cmd *cobra.Command, args []string) error {
		reason, err := runEngineSection(cmd)
		if err != nil {
			return err
		}
		if reason == exitToMenu {
			return runMainMenuLoop(cmd)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(engineCmd)
}

// --- Engine TUI ---

type engineAction struct {
	id   string
	name string
}

type engineModel struct {
	running    bool
	pid        int
	uptime     string
	port       int
	actions    []engineAction
	cursor     int
	statusMsg  string // transient status like "Restarting..."
	exitReason tuiExitReason
	ctrlCOnce  bool
}

type engineCtrlCResetMsg struct{}
type engineActionStartMsg struct{ label string }
type engineActionDoneMsg struct{ msg string }
type engineActionErrMsg struct{ err error }

func newEngineModel() *engineModel {
	m := &engineModel{port: startPort}
	m.refreshStatus()
	return m
}

func (m *engineModel) refreshStatus() {
	status := daemon.GetStatus(homeDir, m.port)
	m.running = status.Running
	m.pid = status.PID
	m.uptime = status.Uptime
	m.cursor = 0

	if m.running {
		m.actions = []engineAction{
			{"restart", "Restart"},
			{"stop", "Stop"},
		}
	} else {
		m.actions = []engineAction{
			{"start", "Start"},
		}
	}
}

func (m *engineModel) Init() tea.Cmd {
	return nil
}

func (m *engineModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.ctrlCOnce {
				m.exitReason = exitQuit
				return m, tea.Quit
			}
			m.ctrlCOnce = true
			return m, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return engineCtrlCResetMsg{} })
		case "esc":
			m.exitReason = exitToMenu
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.actions)-1 {
				m.cursor++
			}
		case "enter":
			if m.statusMsg != "" {
				return m, nil // busy
			}
			action := m.actions[m.cursor].id
			return m, m.executeAction(action)
		}
		if msg.String() != "ctrl+c" && m.ctrlCOnce {
			m.ctrlCOnce = false
		}
	case engineCtrlCResetMsg:
		m.ctrlCOnce = false
		return m, nil
	case engineActionStartMsg:
		m.statusMsg = msg.label
		return m, nil
	case engineActionDoneMsg:
		m.statusMsg = ""
		m.refreshStatus()
		return m, nil
	case engineActionErrMsg:
		m.statusMsg = fmt.Sprintf("Error: %v", msg.err)
		m.refreshStatus()
		return m, nil
	}
	return m, nil
}

func (m *engineModel) executeAction(action string) tea.Cmd {
	// Label to show while the action is in progress
	var label string
	switch action {
	case "start":
		label = "Starting..."
	case "stop":
		label = "Stopping..."
	case "restart":
		label = "Restarting..."
	}

	port := m.port
	running := m.running

	// Use tea.Sequence to first set status msg (in Update), then do the work
	return tea.Sequence(
		func() tea.Msg { return engineActionStartMsg{label: label} },
		func() tea.Msg {
			// This runs in a goroutine — do NOT access m here
			switch action {
			case "start":
				if _, err := daemon.Start(homeDir, port, false); err != nil {
					return engineActionErrMsg{err: err}
				}
				return engineActionDoneMsg{msg: "Engine started"}
			case "stop":
				if err := daemon.Stop(homeDir, false); err != nil {
					return engineActionErrMsg{err: err}
				}
				return engineActionDoneMsg{msg: "Engine stopped"}
			case "restart":
				if running {
					if err := daemon.Stop(homeDir, false); err != nil {
						return engineActionErrMsg{err: err}
					}
				}
				if _, err := daemon.Start(homeDir, port, false); err != nil {
					return engineActionErrMsg{err: err}
				}
				return engineActionDoneMsg{msg: "Engine restarted"}
			}
			return engineActionDoneMsg{}
		},
	)
}

func (m *engineModel) View() string {
	var status string

	if m.running {
		status = greenDot + " " + successStyle.Render("Running") +
			subtitleStyle.Render(fmt.Sprintf(" on :%d", m.port)) + "\n"
		status += subtitleStyle.Render(fmt.Sprintf("PID:     %d", m.pid)) + "\n"
		if m.uptime != "" {
			status += subtitleStyle.Render(fmt.Sprintf("Uptime:  %s", m.uptime)) + "\n"
		}
	} else {
		status = grayDot + " " + subtitleStyle.Render("Stopped") + "\n"
	}

	// Transient status message
	if m.statusMsg != "" {
		status += "\n" + accentStyle.Render(m.statusMsg) + "\n"
	}

	// Actions
	var actions string
	for i, a := range m.actions {
		if i == m.cursor {
			actions += tuiSelectedRow.Render("► "+a.name) + "\n"
		} else {
			actions += "  " + a.name + "\n"
		}
	}

	footer := subtitleStyle.Render("↑↓ navigate · enter select · esc back")

	content := status + "\n" + actions + "\n" + footer

	warn := ""
	if m.ctrlCOnce {
		warn = "\n" + errorStyle.Render("Press Ctrl+C again to quit")
	}

	return "\n" + renderTUIBox("Engine", content) + warn + "\n"
}

// runEngineSection runs the engine TUI and returns its exit reason.
func runEngineSection(cmd *cobra.Command) (tuiExitReason, error) {
	model := newEngineModel()
	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		return exitQuit, err
	}
	return model.exitReason, nil
}
