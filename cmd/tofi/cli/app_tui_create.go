package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ── Create Mode Selection ──

var createModes = []struct {
	label string
	desc  string
}{
	{"Form Wizard", "Step-by-step configuration"},
	{"Chat with AI", "Describe what you want, AI creates it"},
}

func (m *appModel) updateCreateMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(createModes)-1 {
				m.cursor++
			}
		case "enter":
			if m.cursor == 0 {
				// Form mode
				m.step = appStepCreateID
				m.idInput.Focus()
			} else {
				// Chat mode — exit TUI, launch global chat
				m.launchChat = true
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		case "esc":
			m.goToList()
			return m, m.loadApps()
		}
	}
	return m, nil
}

func (m *appModel) viewCreateMode() string {
	var s strings.Builder
	s.WriteString("How would you like to create an app?\n\n")
	for i, mode := range createModes {
		line := fmt.Sprintf("%-20s %s", mode.label, subtitleStyle.Render(mode.desc))
		if i == m.cursor {
			s.WriteString(tuiSelectedRow.Render("► "+line+" ") + "\n")
		} else {
			s.WriteString("  " + line + "\n")
		}
	}
	s.WriteString("\n" + subtitleStyle.Render("↑↓ navigate · enter select · esc back"))
	return s.String()
}

// ── Create: ID ──

func (m *appModel) updateCreateID(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter":
			val := strings.TrimSpace(m.idInput.Value())
			if val == "" {
				return m, nil // required
			}
			// Validate kebab-case
			valid := true
			for _, r := range val {
				if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
					valid = false
					break
				}
			}
			if !valid || val[0] == '-' || val[len(val)-1] == '-' {
				return m, nil // invalid, stay on this step
			}
			m.formID = val
			m.step = appStepCreateName
			m.nameInput.Focus()
			return m, nil
		case "esc":
			m.goToList()
			return m, m.loadApps()
		}
	}
	var cmd tea.Cmd
	m.idInput, cmd = m.idInput.Update(msg)
	return m, cmd
}

// ── Schedule presets ──

var schedPresets = []struct {
	label string
	value string
}{
	{"Skip (no schedule)", ""},
	{"Every day at 09:00", `[{"time":"09:00","repeat":{"type":"daily"}}]`},
	{"Every day at 18:00", `[{"time":"18:00","repeat":{"type":"daily"}}]`},
	{"Every Monday 09:00", `[{"time":"09:00","repeat":{"type":"weekly","days_of_week":[1]}}]`},
	{"Every hour", `[{"time":"*","repeat":{"type":"hourly"}}]`},
}

// ── Create: Name ──

func (m *appModel) updateCreateName(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter":
			val := strings.TrimSpace(m.nameInput.Value())
			m.formName = val // optional — empty means use ID as display name
			m.step = appStepCreateDesc
			m.descInput.Focus()
			return m, nil
		case "esc":
			m.step = appStepCreateID
			m.idInput.Focus()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.nameInput, cmd = m.nameInput.Update(msg)
	return m, cmd
}

// ── Create: Description ──

func (m *appModel) updateCreateDesc(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter":
			m.formDesc = strings.TrimSpace(m.descInput.Value())
			m.step = appStepCreatePrompt
			m.promptInput.Focus()
			return m, nil
		case "esc":
			m.step = appStepCreateName
			m.nameInput.Focus()
			return m, nil // Name 可选，回退到 Name 让用户改
		}
	}
	var cmd tea.Cmd
	m.descInput, cmd = m.descInput.Update(msg)
	return m, cmd
}

// ── Create: Prompt ──

func (m *appModel) updateCreatePrompt(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "esc":
			val := strings.TrimSpace(m.promptInput.Value())
			if val == "" {
				// Empty prompt: go back
				m.step = appStepCreateDesc
				m.descInput.Focus()
				return m, nil
			}
			// Non-empty: treat as "done with prompt"
			m.formPrompt = val
			m.step = appStepCreateModel
			m.cursor = 0
			return m, m.loadModels()
		case "ctrl+d":
			// Force submit prompt
			val := strings.TrimSpace(m.promptInput.Value())
			if val == "" {
				return m, nil
			}
			m.formPrompt = val
			m.step = appStepCreateModel
			m.cursor = 0
			return m, m.loadModels()
		}
	}
	var cmd tea.Cmd
	m.promptInput, cmd = m.promptInput.Update(msg)
	return m, cmd
}

// ── Create: Model ──

func (m *appModel) loadModels() tea.Cmd {
	return func() tea.Msg {
		var models []appModelItem
		if err := m.client.get("/api/v1/models?enabled=true", &models); err != nil {
			return appModelsLoadedMsg{models: nil}
		}
		return appModelsLoadedMsg{models: models}
	}
}

func (m *appModel) updateCreateModel(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		total := len(m.models)
		switch keyMsg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < total-1 {
				m.cursor++
			}
		case "enter":
			if total > 0 {
				m.formModel = m.models[m.cursor].Name
			}
			m.step = appStepCreateSkills
			m.cursor = 0
			return m, m.loadSkills()
		case "esc":
			m.step = appStepCreatePrompt
			m.promptInput.Focus()
			return m, nil
		}
	}
	return m, nil
}

// ── Create: Skills ──

func (m *appModel) loadSkills() tea.Cmd {
	return func() tea.Msg {
		var skills []appSkillItem
		if err := m.client.get("/api/v1/skills", &skills); err != nil {
			return appSkillsLoadedMsg{skills: nil}
		}
		return appSkillsLoadedMsg{skills: skills}
	}
}

func (m *appModel) updateCreateSkills(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		total := len(m.skills)
		switch keyMsg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < total-1 {
				m.cursor++
			}
		case " ":
			if total > 0 {
				name := m.skills[m.cursor].Name
				m.skillChecked[name] = !m.skillChecked[name]
			}
		case "enter":
			// Collect selected skills
			m.formSkills = nil
			for _, sk := range m.skills {
				if m.skillChecked[sk.Name] {
					m.formSkills = append(m.formSkills, sk.Name)
				}
			}
			m.step = appStepCreateSchedule
			m.cursor = 0
			m.schedCursor = 0
			return m, nil
		case "esc":
			m.step = appStepCreateModel
			m.cursor = 0
			return m, nil
		}
	}
	return m, nil
}

// ── Create: Schedule ──

func (m *appModel) updateCreateSchedule(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		total := len(schedPresets)
		switch keyMsg.String() {
		case "up", "k":
			if m.schedCursor > 0 {
				m.schedCursor--
			}
		case "down", "j":
			if m.schedCursor < total-1 {
				m.schedCursor++
			}
		case "enter":
			m.formSched = schedPresets[m.schedCursor].value
			m.step = appStepCreateConfirm
			return m, nil
		case "esc":
			m.step = appStepCreateSkills
			m.cursor = 0
			return m, nil
		}
	}
	return m, nil
}

// ── Create: Confirm ──

func (m *appModel) updateCreateConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter", "y":
			return m, m.submitCreate()
		case "esc", "n":
			m.step = appStepCreateSchedule
			return m, nil
		}
	}
	return m, nil
}

func (m *appModel) submitCreate() tea.Cmd {
	return func() tea.Msg {
		displayName := m.formName
		if displayName == "" {
			displayName = m.formID
		}
		body := map[string]any{
			"id":          m.formID,
			"name":        displayName,
			"description": m.formDesc,
			"prompt":      m.formPrompt,
		}
		if m.formModel != "" {
			body["model"] = m.formModel
		}
		if len(m.formSkills) > 0 {
			body["skills"] = m.formSkills
		}
		if m.formSched != "" {
			body["schedule_rules"] = json.RawMessage(m.formSched)
		}

		// If editing existing app, use PUT
		if m.selectedApp != nil && m.selectedApp.ID != "" {
			jsonBody, _ := json.Marshal(body)
			if err := m.client.put(fmt.Sprintf("/api/v1/apps/%s", m.selectedApp.ID), bytes.NewReader(jsonBody), nil); err != nil {
				return appErrMsg{err: fmt.Errorf("update failed: %w", err)}
			}
			return appActionDoneMsg{msg: fmt.Sprintf("✓ %s updated", m.formName)}
		}

		// New app: POST
		jsonBody, _ := json.Marshal(body)
		var result struct {
			Name string `json:"name"`
		}
		if err := m.client.post("/api/v1/apps", bytes.NewReader(jsonBody), &result); err != nil {
			return appErrMsg{err: fmt.Errorf("create failed: %w", err)}
		}
		name := result.Name
		if name == "" {
			name = m.formName
		}
		return appActionDoneMsg{msg: fmt.Sprintf("✓ App created: %s", name)}
	}
}

// ── Create: View ──

func (m *appModel) viewCreate() string {
	var s strings.Builder

	steps := []string{"ID", "Name", "Description", "Prompt", "Model", "Skills", "Schedule", "Confirm"}
	stepIdx := int(m.step) - int(appStepCreateID) // appStepCreateID is the first form step
	if stepIdx >= 0 && stepIdx < len(steps) {
		progress := fmt.Sprintf("Step %d/%d: %s", stepIdx+1, len(steps), steps[stepIdx])
		s.WriteString(subtitleStyle.Render(progress) + "\n\n")
	}

	switch m.step {
	case appStepCreateID:
		s.WriteString("App ID " + subtitleStyle.Render("(required, kebab-case: lowercase + hyphens)") + "\n\n")
		s.WriteString(m.idInput.View() + "\n\n")
		s.WriteString(subtitleStyle.Render("e.g. daily-weather, news-digest") + "\n\n")
		s.WriteString(subtitleStyle.Render("enter next · esc cancel"))

	case appStepCreateName:
		s.WriteString("Display name " + subtitleStyle.Render("(enter to skip, defaults to ID)") + "\n\n")
		s.WriteString(m.nameInput.View() + "\n\n")
		s.WriteString(subtitleStyle.Render("enter next · esc back"))

	case appStepCreateDesc:
		s.WriteString("Description " + subtitleStyle.Render("(enter to skip)") + "\n\n")
		s.WriteString(m.descInput.View() + "\n\n")
		s.WriteString(subtitleStyle.Render("enter next · esc back"))

	case appStepCreatePrompt:
		s.WriteString("Prompt / Instructions " + subtitleStyle.Render("(required)") + "\n\n")
		s.WriteString(m.promptInput.View() + "\n\n")
		s.WriteString(subtitleStyle.Render("ctrl+d submit · esc back/submit"))

	case appStepCreateModel:
		s.WriteString("Select model " + subtitleStyle.Render("(enter to confirm)") + "\n\n")
		if len(m.models) == 0 {
			s.WriteString(subtitleStyle.Render("Loading models...") + "\n")
		} else {
			for i, mod := range m.models {
				label := fmt.Sprintf("%-30s %s", mod.Name, subtitleStyle.Render(mod.Provider))
				if i == m.cursor {
					s.WriteString(tuiSelectedRow.Render("► "+label+" ") + "\n")
				} else {
					s.WriteString("  " + label + "\n")
				}
			}
		}
		s.WriteString("\n" + subtitleStyle.Render("↑↓ navigate · enter select · esc back"))

	case appStepCreateSkills:
		s.WriteString("Select skills " + subtitleStyle.Render("(space toggle, enter confirm)") + "\n\n")
		if len(m.skills) == 0 {
			s.WriteString(subtitleStyle.Render("No skills installed. Press enter to skip.") + "\n")
		} else {
			for i, sk := range m.skills {
				check := "□"
				if m.skillChecked[sk.Name] {
					check = "✓"
				}
				label := fmt.Sprintf("%s %-25s %s", check, sk.Name, subtitleStyle.Render(sk.Description))
				if len(label) > 60 {
					label = label[:57] + "..."
				}
				if i == m.cursor {
					s.WriteString(tuiSelectedRow.Render("► "+label+" ") + "\n")
				} else {
					s.WriteString("  " + label + "\n")
				}
			}
		}
		s.WriteString("\n" + subtitleStyle.Render("space toggle · enter confirm · esc back"))

	case appStepCreateSchedule:
		s.WriteString("Schedule " + subtitleStyle.Render("(when to run automatically)") + "\n\n")
		for i, preset := range schedPresets {
			if i == m.schedCursor {
				s.WriteString(tuiSelectedRow.Render("► "+preset.label+" ") + "\n")
			} else {
				s.WriteString("  " + preset.label + "\n")
			}
		}
		s.WriteString("\n" + subtitleStyle.Render("↑↓ navigate · enter select · esc back"))

	case appStepCreateConfirm:
		s.WriteString("Confirm\n\n")
		s.WriteString(fmt.Sprintf("  ID:       %s\n", accentStyle.Render(m.formID)))
		displayName := m.formName
		if displayName == "" {
			displayName = m.formID
		}
		s.WriteString(fmt.Sprintf("  Name:     %s\n", displayName))
		if m.formDesc != "" {
			s.WriteString(fmt.Sprintf("  Desc:     %s\n", m.formDesc))
		}
		prompt := m.formPrompt
		if len(prompt) > 50 {
			prompt = prompt[:47] + "..."
		}
		s.WriteString(fmt.Sprintf("  Prompt:   %s\n", prompt))
		if m.formModel != "" {
			s.WriteString(fmt.Sprintf("  Model:    %s\n", m.formModel))
		}
		if len(m.formSkills) > 0 {
			s.WriteString(fmt.Sprintf("  Skills:   %s\n", strings.Join(m.formSkills, ", ")))
		}
		if m.formSched != "" {
			for _, p := range schedPresets {
				if p.value == m.formSched {
					s.WriteString(fmt.Sprintf("  Schedule: %s\n", p.label))
					break
				}
			}
		}
		action := "Create"
		if m.selectedApp != nil {
			action = "Update"
		}
		s.WriteString(fmt.Sprintf("\n  %s? ", action) + accentStyle.Render("enter confirm") + " · " + subtitleStyle.Render("esc back"))
	}

	return s.String()
}
