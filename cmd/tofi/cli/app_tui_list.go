package cli

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── App List ──

func (m *appModel) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	total := len(m.apps)
	switch keyMsg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.adjustOffset(total)
		}
	case "down", "j":
		if m.cursor < total-1 {
			m.cursor++
			m.adjustOffset(total)
		}
	case "n", "N":
		m.goToCreate()
		return m, nil
	case "enter":
		if total > 0 {
			app := m.apps[m.cursor]
			m.selectedApp = &app
			m.selectedIdx = m.cursor
			m.buildDetailActions()
			m.step = appStepDetail
			m.cursor = 0
		}
	case "esc", "q":
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

func (m *appModel) viewList() string {
	var s strings.Builder

	if !m.appsLoaded {
		s.WriteString(subtitleStyle.Render("Loading..."))
		return s.String()
	}

	if len(m.apps) == 0 {
		s.WriteString(subtitleStyle.Render("No apps yet.") + "\n\n")
		s.WriteString("Press " + accentStyle.Render("N") + " to create your first app.\n\n")
		s.WriteString(subtitleStyle.Render("n create · esc quit"))
		return s.String()
	}

	s.WriteString(subtitleStyle.Render(fmt.Sprintf("%d apps", len(m.apps))) + "\n\n")

	end := m.offset + appVisibleItems
	if end > len(m.apps) {
		end = len(m.apps)
	}

	for i := m.offset; i < end; i++ {
		app := m.apps[i]
		dot := "○"
		dotStyle := subtitleStyle
		if app.IsActive {
			dot = "●"
			dotStyle = successStyle
		}

		name := app.Name
		if len(name) > 20 {
			name = name[:17] + "..."
		}

		model := app.Model
		if model == "" {
			model = "(default)"
		}
		if len(model) > 18 {
			model = model[:15] + "..."
		}

		line := fmt.Sprintf("%s %-20s %s", dotStyle.Render(dot), name, subtitleStyle.Render(model))

		if i == m.cursor {
			s.WriteString(tuiSelectedRow.Render("► "+line+" ") + "\n")
		} else {
			s.WriteString("  " + line + "\n")
		}

		// Show description on next line for selected item
		if i == m.cursor && app.Description != "" {
			desc := app.Description
			if len(desc) > 55 {
				desc = desc[:52] + "..."
			}
			s.WriteString("    " + subtitleStyle.Render(desc) + "\n")
		}
	}

	s.WriteString("\n" + subtitleStyle.Render("↑↓ navigate · enter details · n create · esc quit"))
	return s.String()
}

// ── App Detail ──

func (m *appModel) buildDetailActions() {
	if m.selectedApp == nil {
		return
	}
	m.actions = []appAction{
		{id: "run", label: "Run Now", desc: "Trigger a manual run"},
		{id: "sessions", label: "Sessions", desc: "View past sessions"},
		{id: "edit", label: "Edit", desc: "Chat with AI to modify"},
	}
	if m.selectedApp.IsActive {
		m.actions = append(m.actions, appAction{id: "deactivate", label: "Deactivate", desc: "Stop scheduled runs"})
	} else {
		m.actions = append(m.actions, appAction{id: "activate", label: "Activate", desc: "Enable scheduled runs"})
	}
	m.actions = append(m.actions, appAction{id: "delete", label: "Delete", desc: "Remove this app"})
}

func (m *appModel) updateDetail(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.actions)-1 {
			m.cursor++
		}
	case "esc":
		m.goToList()
		return m, m.loadApps()
	case "enter":
		if m.cursor < len(m.actions) {
			return m.handleDetailAction(m.actions[m.cursor].id)
		}
	}
	return m, nil
}

func (m *appModel) handleDetailAction(action string) (tea.Model, tea.Cmd) {
	app := m.selectedApp
	if app == nil {
		return m, nil
	}

	switch action {
	case "run":
		return m, m.runApp(app.ID, app.Name)
	case "sessions":
		m.step = appStepSessions
		m.cursor = 0
		m.offset = 0
		return m, m.loadSessions(app.ID)
	case "edit":
		// Exit TUI, launch AI chat scoped to this app for editing
		scope := "agent:app-" + app.ID
		m.launchChat = true
		m.launchChatScope = scope
		m.launchChatSkills = []string{"apps", "app-create", "app-list", "app-inspect", "app-manage"}
		m.launchChatMessage = "Show me the current config of app \"" + app.ID + "\" and ask what I'd like to change."
		m.quitting = true
		return m, tea.Quit
	case "activate":
		return m, m.activateApp(app.ID, app.Name, true)
	case "deactivate":
		return m, m.activateApp(app.ID, app.Name, false)
	case "delete":
		return m, m.deleteApp(app.ID, app.Name)
	}
	return m, nil
}

func (m *appModel) viewDetail() string {
	app := m.selectedApp
	if app == nil {
		return subtitleStyle.Render("No app selected")
	}

	var s strings.Builder

	// Info section
	status := subtitleStyle.Render("○ inactive")
	if app.IsActive {
		status = successStyle.Render("● active")
	}
	model := app.Model
	if model == "" {
		model = "(default)"
	}

	s.WriteString(fmt.Sprintf("Status: %s    Model: %s\n", status, accentStyle.Render(model)))

	if app.ScheduleRules != "" && app.ScheduleRules != "[]" {
		s.WriteString(fmt.Sprintf("Schedule: %s\n", subtitleStyle.Render(app.ScheduleRules)))
	}
	if app.Description != "" {
		s.WriteString(subtitleStyle.Render(app.Description) + "\n")
	}
	s.WriteString("\n")

	// Actions
	for i, act := range m.actions {
		line := fmt.Sprintf("%-16s %s", act.label, subtitleStyle.Render(act.desc))
		if i == m.cursor {
			s.WriteString(tuiSelectedRow.Render("► "+line+" ") + "\n")
		} else {
			s.WriteString("  " + line + "\n")
		}
	}

	s.WriteString("\n" + subtitleStyle.Render("↑↓ navigate · enter select · esc back"))
	return s.String()
}

// ── Sessions ──

func (m *appModel) loadSessions(appID string) tea.Cmd {
	return func() tea.Msg {
		// Compute scope for this app
		prefix := appID
		if len(prefix) > 8 {
			prefix = prefix[:8]
		}
		scope := "agent:app-" + prefix

		var sessions []appSessionItem
		if err := m.client.get("/api/v1/chat/sessions?scope="+scope+"&limit=20", &sessions); err != nil {
			return appSessionsLoadedMsg{sessions: nil}
		}
		return appSessionsLoadedMsg{sessions: sessions}
	}
}

func (m *appModel) updateSessions(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	total := len(m.sessions)
	switch keyMsg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.adjustOffset(total)
		}
	case "down", "j":
		if m.cursor < total-1 {
			m.cursor++
			m.adjustOffset(total)
		}
	case "esc":
		m.step = appStepDetail
		m.cursor = 0
		m.buildDetailActions()
		return m, nil
	case "enter":
		if total > 0 {
			sess := m.sessions[m.cursor]
			// Exit TUI, launch chat with this session
			scope := ""
			if m.selectedApp != nil {
				prefix := m.selectedApp.ID
				if len(prefix) > 8 {
					prefix = prefix[:8]
				}
				scope = "agent:app-" + prefix
			}
			m.launchChat = true
			m.launchChatScope = scope
			m.launchChatSession = sess.ID
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *appModel) viewSessions() string {
	var s strings.Builder

	if m.sessions == nil {
		s.WriteString(subtitleStyle.Render("Loading..."))
		return s.String()
	}

	if len(m.sessions) == 0 {
		s.WriteString(subtitleStyle.Render("No sessions yet.") + "\n\n")
		s.WriteString(subtitleStyle.Render("esc back"))
		return s.String()
	}

	end := m.offset + appVisibleItems
	if end > len(m.sessions) {
		end = len(m.sessions)
	}

	for i := m.offset; i < end; i++ {
		sess := m.sessions[i]
		title := sess.Title
		if title == "" {
			title = sess.ID
		}
		if len(title) > 30 {
			title = title[:27] + "..."
		}

		ts := formatTimeShort(sess.UpdatedAt)
		meta := fmt.Sprintf("%d msgs  $%.4f", sess.MessageCount, sess.TotalCost)
		line := fmt.Sprintf("%-12s %-30s %s", subtitleStyle.Render(ts), title, subtitleStyle.Render(meta))

		if i == m.cursor {
			s.WriteString(tuiSelectedRow.Render("► "+line+" ") + "\n")
		} else {
			s.WriteString("  " + line + "\n")
		}
	}

	s.WriteString("\n" + subtitleStyle.Render("↑↓ navigate · enter view · esc back"))
	return s.String()
}

// ── API actions ──

func (m *appModel) runApp(appID, name string) tea.Cmd {
	return func() tea.Msg {
		if err := m.client.post(fmt.Sprintf("/api/v1/apps/%s/run", appID), nil, nil); err != nil {
			return appErrMsg{err: fmt.Errorf("failed to run %s: %w", name, err)}
		}
		return appActionDoneMsg{msg: fmt.Sprintf("✓ Run triggered for %s", name)}
	}
}

func (m *appModel) activateApp(appID, name string, active bool) tea.Cmd {
	return func() tea.Msg {
		endpoint := "activate"
		if !active {
			endpoint = "deactivate"
		}
		if err := m.client.post(fmt.Sprintf("/api/v1/apps/%s/%s", appID, endpoint), nil, nil); err != nil {
			return appErrMsg{err: fmt.Errorf("failed: %w", err)}
		}
		verb := "activated"
		if !active {
			verb = "deactivated"
		}
		return appActionDoneMsg{msg: fmt.Sprintf("✓ %s %s", name, verb)}
	}
}

func (m *appModel) deleteApp(appID, name string) tea.Cmd {
	return func() tea.Msg {
		if err := m.client.delete(fmt.Sprintf("/api/v1/apps/%s", appID)); err != nil {
			return appErrMsg{err: fmt.Errorf("failed to delete %s: %w", name, err)}
		}
		return appActionDoneMsg{msg: fmt.Sprintf("✓ %s deleted", name)}
	}
}

// ── Done ──

func (m *appModel) updateDone(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.String() {
	case "enter", "esc":
		m.goToList()
		return m, m.loadApps()
	}
	return m, nil
}

func (m *appModel) viewDone() string {
	var s strings.Builder
	if m.resultOK {
		s.WriteString(successStyle.Render(m.resultMsg) + "\n")
	} else {
		s.WriteString(errorStyle.Render(m.resultMsg) + "\n")
	}
	s.WriteString("\n" + subtitleStyle.Render("enter continue · esc back"))
	return s.String()
}

// Unused import guard
var _ = lipgloss.Color("")
