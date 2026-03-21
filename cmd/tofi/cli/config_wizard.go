package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"tofi-core/internal/daemon"
)

// --- tofi config wizard (interactive TUI) ---

var configWizardCmd = &cobra.Command{
	Use:    "wizard",
	Hidden: true,
	Short:  "Interactive configuration wizard",
	Args:   cobra.NoArgs,
	RunE:   runConfigWizard,
}

func runConfigWizard(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}
	p := tea.NewProgram(newCfgModel(client))
	_, err := p.Run()
	return err
}

// --- Steps ---

type cfgStep int

const (
	cfgStepMenu          cfgStep = iota // main menu
	cfgStepKeys                         // view keys
	cfgStepAddKeyProvider               // choose provider for new key
	cfgStepAddKeyInput                  // enter key value
	cfgStepDeleteKey                    // choose key to delete
	cfgStepModels                       // unified: enable/disable models + set default
	cfgStepAutoStart                    // toggle auto-start on boot
	cfgStepDone                         // result message
)

// --- Menu items ---

type cfgMenuItem struct {
	id          string
	display     string
	description string
}

var cfgMenuItems = []cfgMenuItem{
	{id: "keys", display: "AI Keys", description: "View and manage API keys"},
	{id: "models", display: "Models", description: "Enable models and set default"},
	{id: "autostart", display: "Auto Start", description: "Start engine on boot"},
}

var cfgProviderOrder = []string{"openai", "anthropic", "gemini", "deepseek"}

// --- Model ---

const cfgVisibleItems = 16 // max items visible in scrollable lists

type cfgModel struct {
	client     *apiClient
	step       cfgStep
	cursor     int
	viewOffset int // scroll offset for long lists
	quitting   bool

	// Keys
	keys      cfgKeysResult
	keysLoaded bool

	// Add key
	addProvider    string
	addProviders   []string // filtered: providers without keys yet
	keyInput       textinput.Model

	// Delete key
	deleteItems []cfgKeyItem
	deleteCursor int

	// Models
	models          []cfgModelInfo
	modelsLoaded    bool
	currentModel    string
	keyedProviders  map[string]bool // providers that have an API key

	// Enabled models
	enabledModels    map[string]bool
	enabledLoaded    bool
	enabledList      []enabledListItem   // flat list: provider headers + model rows

	// Navigation
	returnStep cfgStep // where to go after key setup (from enabled models flow)
	startStep  cfgStep // initial step (for direct navigation from Settings)

	// Result
	resultMsg string
}

type cfgKeysResult struct {
	System []map[string]string `json:"system"`
	User   []map[string]string `json:"user"`
	Env    []map[string]string `json:"env"`
}

type cfgKeyItem struct {
	provider string
	masked   string
	scope    string
	source   string // env var name if from env
}

type cfgModelInfo struct {
	Name          string  `json:"name"`
	Provider      string  `json:"provider"`
	ContextWindow int     `json:"context_window"`
	InputCost     float64 `json:"input_cost_per_1m"`
	OutputCost    float64 `json:"output_cost_per_1m"`
}

// enabledListItem represents a row in the enabled models flat list
type enabledListItem struct {
	isProvider bool         // true = provider header row, false = model row
	provider   string       // provider name
	hasKey     bool         // provider has API key configured
	model      cfgModelInfo // only valid when isProvider=false
}

// --- Messages ---

type cfgKeysLoadedMsg struct{ result cfgKeysResult }
type cfgModelsLoadedMsg struct {
	models         []cfgModelInfo
	current        string
	enabled        []string
	keyedProviders map[string]bool
}
type cfgActionDoneMsg struct{ msg string }
type cfgActionErrMsg struct{ err error }

func newCfgModel(client *apiClient) cfgModel {
	ti := textinput.New()
	ti.Placeholder = "paste your API key here..."
	ti.CharLimit = 256
	ti.Width = 50
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'

	return cfgModel{
		client:         client,
		step:           cfgStepMenu,
		keyInput:       ti,
		enabledModels:  make(map[string]bool),
		keyedProviders: make(map[string]bool),
	}
}

func (m cfgModel) Init() tea.Cmd {
	// When launched at a specific step (from Settings menu), load data
	switch m.startStep {
	case cfgStepKeys:
		return m.loadKeys()
	case cfgStepModels:
		return m.loadModels()
	}
	return nil
}

// --- Update ---

func (m cfgModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "esc":
			return m.handleEsc()
		}

	case cfgKeysLoadedMsg:
		m.keys = msg.result
		m.keysLoaded = true
		return m, nil

	case cfgModelsLoadedMsg:
		m.models = msg.models
		m.currentModel = msg.current
		m.keyedProviders = msg.keyedProviders
		for _, name := range msg.enabled {
			m.enabledModels[name] = true
		}
		// Build flat list for models page (provider headers + model rows)
		m.enabledList = nil
		lastProv := ""
		for _, model := range m.models {
			if model.Provider != lastProv {
				m.enabledList = append(m.enabledList, enabledListItem{
					isProvider: true,
					provider:   model.Provider,
					hasKey:     m.keyedProviders[model.Provider],
				})
				lastProv = model.Provider
			}
			m.enabledList = append(m.enabledList, enabledListItem{
				isProvider: false,
				provider:   model.Provider,
				hasKey:     m.keyedProviders[model.Provider],
				model:      model,
			})
		}
		m.modelsLoaded = true
		m.enabledLoaded = true
		return m, nil

	case cfgActionDoneMsg:
		m.resultMsg = msg.msg
		m.step = cfgStepDone
		return m, nil

	case cfgActionErrMsg:
		m.resultMsg = "Error: " + msg.err.Error()
		m.step = cfgStepDone
		return m, nil
	}

	switch m.step {
	case cfgStepMenu:
		return m.updateMenu(msg)
	case cfgStepKeys:
		return m.updateKeys(msg)
	case cfgStepAddKeyProvider:
		return m.updateAddKeyProvider(msg)
	case cfgStepAddKeyInput:
		return m.updateAddKeyInput(msg)
	case cfgStepDeleteKey:
		return m.updateDeleteKey(msg)
	case cfgStepModels:
		return m.updateModels(msg)
	case cfgStepAutoStart:
		return m.updateAutoStart(msg)
	case cfgStepDone:
		return m.updateDone(msg)
	}

	return m, nil
}

func (m cfgModel) handleEsc() (tea.Model, tea.Cmd) {
	switch m.step {
	case cfgStepMenu:
		m.quitting = true
		return m, tea.Quit
	case cfgStepKeys, cfgStepModels, cfgStepAutoStart:
		// If launched directly at this step (from Settings), ESC exits
		if m.startStep == m.step {
			m.quitting = true
			return m, tea.Quit
		}
		m.step = cfgStepMenu
		m.cursor = 0
		return m, nil
	case cfgStepAddKeyProvider, cfgStepDeleteKey:
		m.step = cfgStepKeys
		m.cursor = 0
		return m, nil
	case cfgStepAddKeyInput:
		if m.returnStep != 0 {
			// Came from enabled models flow — go back there
			m.step = m.returnStep
			m.returnStep = 0
			m.cursor = 0
			m.viewOffset = 0
			m.enabledModels = make(map[string]bool)
			return m, m.loadModels()
		}
		m.step = cfgStepAddKeyProvider
		m.cursor = 0
		m.keyInput.SetValue("")
		return m, nil
	case cfgStepDone:
		m.step = cfgStepMenu
		m.cursor = 0
		return m, nil
	}
	m.quitting = true
	return m, tea.Quit
}

func (m cfgModel) updateMenu(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(cfgMenuItems)-1 {
				m.cursor++
			}
		case "enter":
			switch cfgMenuItems[m.cursor].id {
			case "keys":
				m.step = cfgStepKeys
				m.cursor = 0
				m.keysLoaded = false
				return m, m.loadKeys()
			case "models":
				m.step = cfgStepModels
				m.cursor = 0
				m.viewOffset = 0
				m.modelsLoaded = false
				m.enabledLoaded = false
				m.enabledModels = make(map[string]bool)
				return m, m.loadModels()
			case "autostart":
				m.step = cfgStepAutoStart
				m.cursor = 0
				return m, nil
			}
		}
	}
	return m, nil
}

func (m cfgModel) updateKeys(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		// keys view has 2 actions: Add / Delete
		items := 2
		switch keyMsg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < items-1 {
				m.cursor++
			}
		case "enter":
			switch m.cursor {
			case 0: // Add new key
				m.addProviders = m.buildAddProviders()
				if len(m.addProviders) == 0 {
					m.resultMsg = "All providers already have keys configured"
					m.step = cfgStepDone
					return m, nil
				}
				m.step = cfgStepAddKeyProvider
				m.cursor = 0
				return m, nil
			case 1: // Delete a key
				m.deleteItems = m.buildDeleteItems()
				if len(m.deleteItems) == 0 {
					m.resultMsg = "No deletable keys (only database keys can be deleted)"
					m.step = cfgStepDone
					return m, nil
				}
				m.step = cfgStepDeleteKey
				m.deleteCursor = 0
				return m, nil
			}
		}
	}
	return m, nil
}

func (m cfgModel) updateAddKeyProvider(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.addProviders)-1 {
				m.cursor++
			}
		case "enter":
			m.addProvider = m.addProviders[m.cursor]
			m.step = cfgStepAddKeyInput
			m.keyInput.SetValue("")
			m.keyInput.Focus()
			return m, textinput.Blink
		}
	}
	return m, nil
}

func (m cfgModel) updateAddKeyInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter":
			key := m.keyInput.Value()
			if key == "" {
				return m, nil
			}
			m.keyInput.Blur()
			return m, m.saveKey(m.addProvider, key)
		}
	}
	var cmd tea.Cmd
	m.keyInput, cmd = m.keyInput.Update(msg)
	return m, cmd
}

func (m cfgModel) updateDeleteKey(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "up", "k":
			if m.deleteCursor > 0 {
				m.deleteCursor--
			}
		case "down", "j":
			if m.deleteCursor < len(m.deleteItems)-1 {
				m.deleteCursor++
			}
		case "enter":
			item := m.deleteItems[m.deleteCursor]
			return m, m.deleteKey(item.provider, item.scope)
		}
	}
	return m, nil
}

func (m cfgModel) updateModels(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.enabledLoaded {
		return m, nil
	}
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.adjustViewOffset()
			}
		case "down", "j":
			if m.cursor < len(m.enabledList)-1 {
				m.cursor++
				m.adjustViewOffset()
			}
		case " ":
			// Toggle enable/disable
			if m.cursor >= len(m.enabledList) {
				return m, nil
			}
			item := m.enabledList[m.cursor]
			if !item.hasKey {
				// No key — redirect to add key
				m.addProvider = item.provider
				m.returnStep = cfgStepModels
				m.step = cfgStepAddKeyInput
				m.keyInput.SetValue("")
				m.keyInput.Focus()
				return m, textinput.Blink
			}
			if item.isProvider {
				// Toggle all models under this provider
				prov := item.provider
				allEnabled := true
				for _, li := range m.enabledList {
					if !li.isProvider && li.provider == prov {
						if !m.enabledModels[li.model.Name] {
							allEnabled = false
							break
						}
					}
				}
				for _, li := range m.enabledList {
					if !li.isProvider && li.provider == prov {
						m.enabledModels[li.model.Name] = !allEnabled
					}
				}
			} else {
				m.enabledModels[item.model.Name] = !m.enabledModels[item.model.Name]
			}
			return m, nil
		case "d":
			// Set/clear default model
			if m.cursor >= len(m.enabledList) {
				return m, nil
			}
			item := m.enabledList[m.cursor]
			if item.isProvider || !item.hasKey {
				return m, nil
			}
			if m.currentModel == item.model.Name {
				// Already default — clear it
				m.currentModel = ""
			} else {
				// Set as default (auto-enable)
				m.currentModel = item.model.Name
				m.enabledModels[item.model.Name] = true
			}
			return m, nil
		case "enter":
			// Save both enabled models and preferred model
			return m, m.saveModelsAndDefault()
		}
	}
	return m, nil
}

// adjustViewOffset keeps cursor within the visible window
func (m cfgModel) updateAutoStart(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		// Two items: Enable (0) and Disable (1)
		items := 2
		switch keyMsg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < items-1 {
				m.cursor++
			}
		case "enter":
			if m.cursor == 0 {
				// Enable
				if err := daemon.EnableAutoStart(homeDir); err != nil {
					m.resultMsg = "Error: " + err.Error()
				} else {
					m.resultMsg = "Auto-start enabled"
				}
			} else {
				// Disable
				if err := daemon.DisableAutoStart(homeDir); err != nil {
					m.resultMsg = "Error: " + err.Error()
				} else {
					m.resultMsg = "Auto-start disabled"
				}
			}
			m.step = cfgStepDone
			return m, nil
		}
	}
	return m, nil
}

func (m *cfgModel) adjustViewOffset() {
	if m.cursor < m.viewOffset {
		m.viewOffset = m.cursor
	}
	if m.cursor >= m.viewOffset+cfgVisibleItems {
		m.viewOffset = m.cursor - cfgVisibleItems + 1
	}
}

func (m cfgModel) updateDone(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter", " ":
			if m.returnStep != 0 {
				// Return to previous flow (e.g. enabled models after key setup)
				step := m.returnStep
				m.returnStep = 0
				m.step = step
				m.cursor = 0
				m.viewOffset = 0
				m.enabledModels = make(map[string]bool)
				return m, m.loadModels()
			}
			m.step = cfgStepMenu
			m.cursor = 0
			return m, nil
		}
	}
	return m, nil
}

// --- View ---

func (m cfgModel) View() string {
	if m.quitting {
		return ""
	}

	var s strings.Builder

	switch m.step {
	case cfgStepMenu:
		m.viewMenu(&s)
	case cfgStepKeys:
		m.viewKeys(&s)
	case cfgStepAddKeyProvider:
		m.viewAddKeyProvider(&s)
	case cfgStepAddKeyInput:
		m.viewAddKeyInput(&s)
	case cfgStepDeleteKey:
		m.viewDeleteKey(&s)
	case cfgStepModels:
		m.viewModels(&s)
	case cfgStepAutoStart:
		m.viewAutoStart(&s)
	case cfgStepDone:
		m.viewDone(&s)
	}

	return "\n" + renderTUIBox("Config", s.String()) + "\n"
}

func (m cfgModel) viewMenu(s *strings.Builder) {
	s.WriteString(subtitleStyle.Render("What would you like to configure?") + "\n\n")

	for i, item := range cfgMenuItems {
		if i == m.cursor {
			s.WriteString(tuiSelectedRow.Render("► "+item.display+"   "+item.description+" ") + "\n")
		} else {
			nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc"))
			s.WriteString("  " + nameStyle.Render(item.display) + "   " + subtitleStyle.Render(item.description) + "\n")
		}
	}

	s.WriteString("\n" + subtitleStyle.Render("↑↓ navigate · enter select · esc quit") + "\n")
}

func (m cfgModel) viewKeys(s *strings.Builder) {
	s.WriteString(titleStyle.Render("  AI Keys") + "\n\n")

	if !m.keysLoaded {
		s.WriteString(subtitleStyle.Render("  Loading...") + "\n")
		return
	}

	border := lipgloss.NewStyle().Foreground(lipgloss.Color("#30363d"))
	hasKeys := false

	if len(m.keys.System) > 0 {
		hasKeys = true
		s.WriteString(subtitleStyle.Render("  System") + "\n")
		for _, k := range m.keys.System {
			s.WriteString(fmt.Sprintf("  %s  %-12s %s\n",
				border.Render("│"),
				accentStyle.Render(k["provider"]),
				subtitleStyle.Render(k["masked_key"])))
		}
		s.WriteString("\n")
	}

	if len(m.keys.User) > 0 {
		hasKeys = true
		s.WriteString(subtitleStyle.Render("  User") + "\n")
		for _, k := range m.keys.User {
			s.WriteString(fmt.Sprintf("  %s  %-12s %s\n",
				border.Render("│"),
				accentStyle.Render(k["provider"]),
				subtitleStyle.Render(k["masked_key"])))
		}
		s.WriteString("\n")
	}

	if len(m.keys.Env) > 0 {
		hasKeys = true
		s.WriteString(subtitleStyle.Render("  Environment") + "\n")
		for _, k := range m.keys.Env {
			s.WriteString(fmt.Sprintf("  %s  %-12s %s  %s\n",
				border.Render("│"),
				accentStyle.Render(k["provider"]),
				subtitleStyle.Render(k["masked_key"]),
				subtitleStyle.Render("("+k["source"]+")")))
		}
		s.WriteString("\n")
	}

	if !hasKeys {
		s.WriteString(subtitleStyle.Render("  No API keys configured.") + "\n\n")
	}

	// Actions
	actions := []string{"Add new key", "Delete a key"}
	for i, a := range actions {
		if i == m.cursor {
			s.WriteString(tuiSelectedRow.Render("► "+a+" ") + "\n")
		} else {
			s.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc")).Render(a) + "\n")
		}
	}

	s.WriteString("\n" + subtitleStyle.Render("  ↑↓ navigate · enter select · esc back") + "\n")
}

func (m cfgModel) viewAddKeyProvider(s *strings.Builder) {
	s.WriteString(titleStyle.Render("  Add Key") + subtitleStyle.Render("  Select provider") + "\n\n")

	for i, p := range m.addProviders {
		if i == m.cursor {
			s.WriteString(tuiSelectedRow.Render("► "+p+" ") + "\n")
		} else {
			s.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc")).Render(p) + "\n")
		}
	}

	s.WriteString("\n" + subtitleStyle.Render("  ↑↓ navigate · enter select · esc back") + "\n")
}

func (m cfgModel) viewAddKeyInput(s *strings.Builder) {
	s.WriteString(titleStyle.Render("  Add Key") + subtitleStyle.Render("  "+m.addProvider) + "\n\n")
	s.WriteString("  " + m.keyInput.View() + "\n\n")
	s.WriteString(subtitleStyle.Render("  enter save · esc back") + "\n")
}

func (m cfgModel) viewDeleteKey(s *strings.Builder) {
	s.WriteString(titleStyle.Render("  Delete Key") + subtitleStyle.Render("  Select key to delete") + "\n\n")

	for i, item := range m.deleteItems {
		label := fmt.Sprintf("%s (%s)  %s", item.provider, item.scope, item.masked)
		if i == m.deleteCursor {
			s.WriteString(tuiSelectedRow.Render("► "+label+" ") + "\n")
		} else {
			s.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc")).Render(item.provider+" ("+item.scope+")") + "  " + subtitleStyle.Render(item.masked) + "\n")
		}
	}

	s.WriteString("\n" + subtitleStyle.Render("  ↑↓ navigate · enter delete · esc back") + "\n")
}

func (m cfgModel) viewModels(s *strings.Builder) {
	s.WriteString(titleStyle.Render("  Models") + "\n")
	if m.currentModel != "" {
		s.WriteString(subtitleStyle.Render("  Default: ") + accentStyle.Render(m.currentModel) + "\n")
	} else {
		s.WriteString(subtitleStyle.Render("  Default: none (same as last time)") + "\n")
	}
	s.WriteString("\n")

	if !m.enabledLoaded {
		s.WriteString(subtitleStyle.Render("  Loading models...") + "\n")
		return
	}

	if len(m.enabledList) == 0 {
		s.WriteString(subtitleStyle.Render("  No models available. Add an API key first.") + "\n")
		return
	}

	// Scroll indicator: top
	if m.viewOffset > 0 {
		s.WriteString(subtitleStyle.Render("  ▲ more") + "\n")
	}

	end := m.viewOffset + cfgVisibleItems
	if end > len(m.enabledList) {
		end = len(m.enabledList)
	}

	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#484f58"))
	defaultMark := lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922")).Render(" ★")

	for i := m.viewOffset; i < end; i++ {
		item := m.enabledList[i]
		selected := i == m.cursor

		if item.isProvider {
			// Provider header row
			provLabel := strings.Title(item.provider)
			if selected {
				hint := "select all"
				if !item.hasKey {
					hint = "set up key"
				}
				s.WriteString(tuiSelectedRow.Render("► "+provLabel+"   "+hint+" ") + "\n")
			} else {
				if item.hasKey {
					s.WriteString("  " + subtitleStyle.Render(provLabel) + "\n")
				} else {
					s.WriteString("  " + dimStyle.Render(provLabel+"  (no key)") + "\n")
				}
			}
		} else if !item.hasKey {
			// No key model row
			if selected {
				s.WriteString(tuiSelectedRow.Render("►  [-] "+item.model.Name+" ") + "\n")
				s.WriteString("  " + accentStyle.Render("Press space to set up API key first") + "\n")
			} else {
				s.WriteString("  " + dimStyle.Render("[-] "+item.model.Name) + "\n")
			}
		} else {
			// Normal model row
			check := "[ ]"
			if m.enabledModels[item.model.Name] {
				check = "[✓]"
			}
			isDefault := item.model.Name == m.currentModel
			star := ""
			if isDefault {
				star = " ★"
			}
			// ► is 2 columns wide in terminal
			// selected:   "►  " (4 cols) + "[✓] model"
			// unselected: "    " (4 cols) + "[✓] model"
			if selected {
				s.WriteString(tuiSelectedRow.Render("► "+check+" "+item.model.Name+star+" ") + "\n")
			} else {
				checkStyle := subtitleStyle
				nameStyle := subtitleStyle
				if m.enabledModels[item.model.Name] {
					checkStyle = successStyle
					nameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc"))
				}
				line := "  " + checkStyle.Render(check) + " " + nameStyle.Render(item.model.Name)
				if isDefault {
					line += defaultMark
				}
				s.WriteString(line + "\n")
			}
		}
	}

	// Scroll indicator: bottom
	if end < len(m.enabledList) {
		s.WriteString(subtitleStyle.Render("  ▼ more") + "\n")
	}

	s.WriteString("\n" + subtitleStyle.Render("  space toggle · d set default · enter save · esc back") + "\n")
}

func (m cfgModel) viewAutoStart(s *strings.Builder) {
	s.WriteString(titleStyle.Render("  Auto Start") + "\n")
	enabled := daemon.IsAutoStartEnabled(homeDir)
	if enabled {
		s.WriteString(subtitleStyle.Render("  Current: ") + successStyle.Render("enabled") + "\n")
	} else {
		s.WriteString(subtitleStyle.Render("  Current: ") + subtitleStyle.Render("disabled") + "\n")
	}
	s.WriteString("\n")

	options := []struct {
		label string
		desc  string
	}{
		{"Enable", "Start engine automatically on boot"},
		{"Disable", "Manual start only"},
	}

	for i, opt := range options {
		if i == m.cursor {
			s.WriteString(tuiSelectedRow.Render("► "+opt.label+"   "+opt.desc+" ") + "\n")
		} else {
			nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc"))
			s.WriteString("  " + nameStyle.Render(opt.label) + "   " + subtitleStyle.Render(opt.desc) + "\n")
		}
	}

	s.WriteString("\n" + subtitleStyle.Render("  ↑↓ navigate · enter select · esc back") + "\n")
}

func (m cfgModel) viewDone(s *strings.Builder) {
	if strings.HasPrefix(m.resultMsg, "Error:") {
		s.WriteString("  " + errorStyle.Render("✗ ") + m.resultMsg[6:] + "\n")
	} else {
		s.WriteString("  " + successStyle.Render("✓ ") + m.resultMsg + "\n")
	}
	s.WriteString("\n" + subtitleStyle.Render("  enter continue · esc menu") + "\n")
}

// --- API Commands ---

func (m cfgModel) loadKeys() tea.Cmd {
	return func() tea.Msg {
		var result cfgKeysResult
		if err := m.client.get("/api/v1/settings/ai-keys", &result); err != nil {
			return cfgActionErrMsg{err: err}
		}
		return cfgKeysLoadedMsg{result: result}
	}
}

func (m cfgModel) loadModels() tea.Cmd {
	return func() tea.Msg {
		var models []cfgModelInfo
		_ = m.client.get("/api/v1/models", &models)

		var pref struct {
			Model string `json:"model"`
		}
		_ = m.client.get("/api/v1/settings/preferred-model", &pref)

		var enabledResp struct {
			Models []string `json:"models"`
		}
		_ = m.client.get("/api/v1/settings/enabled-models", &enabledResp)

		// Fetch keys to determine which providers are configured
		var keys cfgKeysResult
		_ = m.client.get("/api/v1/settings/ai-keys", &keys)

		keyed := make(map[string]bool)
		for _, k := range keys.System {
			keyed[k["provider"]] = true
		}
		for _, k := range keys.User {
			keyed[k["provider"]] = true
		}
		for _, k := range keys.Env {
			keyed[k["provider"]] = true
		}

		// Sort models by provider order: openai → anthropic → gemini → deepseek
		providerOrder := map[string]int{
			"openai": 0, "anthropic": 1, "gemini": 2, "deepseek": 3,
		}
		sort.SliceStable(models, func(i, j int) bool {
			oi, ok1 := providerOrder[models[i].Provider]
			oj, ok2 := providerOrder[models[j].Provider]
			if !ok1 {
				oi = 99
			}
			if !ok2 {
				oj = 99
			}
			return oi < oj
		})

		return cfgModelsLoadedMsg{
			models:         models,
			current:        pref.Model,
			enabled:        enabledResp.Models,
			keyedProviders: keyed,
		}
	}
}

func (m cfgModel) saveKey(provider, key string) tea.Cmd {
	return func() tea.Msg {
		body := map[string]string{
			"provider": provider,
			"api_key":  key,
			"scope":    "system",
		}
		jsonBody, _ := json.Marshal(body)
		if err := m.client.post("/api/v1/settings/ai-keys", bytes.NewReader(jsonBody), nil); err != nil {
			return cfgActionErrMsg{err: err}
		}
		return cfgActionDoneMsg{msg: provider + " key saved"}
	}
}

func (m cfgModel) deleteKey(provider, scope string) tea.Cmd {
	return func() tea.Msg {
		path := fmt.Sprintf("/api/v1/settings/ai-keys/%s?scope=%s", provider, scope)
		if err := m.client.delete(path); err != nil {
			return cfgActionErrMsg{err: err}
		}
		return cfgActionDoneMsg{msg: provider + " key deleted"}
	}
}

func (m cfgModel) setPreferredModel(name string) tea.Cmd {
	return func() tea.Msg {
		body := map[string]string{"model": name}
		jsonBody, _ := json.Marshal(body)
		if err := m.client.post("/api/v1/settings/preferred-model", bytes.NewReader(jsonBody), nil); err != nil {
			return cfgActionErrMsg{err: err}
		}
		return cfgActionDoneMsg{msg: "Preferred model: " + name}
	}
}

func (m cfgModel) saveEnabledModels() tea.Cmd {
	return func() tea.Msg {
		var enabled []string
		for name, on := range m.enabledModels {
			if on {
				enabled = append(enabled, name)
			}
		}
		body := map[string][]string{"models": enabled}
		jsonBody, _ := json.Marshal(body)
		if err := m.client.post("/api/v1/settings/enabled-models", bytes.NewReader(jsonBody), nil); err != nil {
			return cfgActionErrMsg{err: err}
		}
		return cfgActionDoneMsg{msg: fmt.Sprintf("%d models enabled", len(enabled))}
	}
}

func (m cfgModel) saveModelsAndDefault() tea.Cmd {
	return func() tea.Msg {
		// Save enabled models
		var enabled []string
		for name, on := range m.enabledModels {
			if on {
				enabled = append(enabled, name)
			}
		}
		body1 := map[string][]string{"models": enabled}
		jsonBody1, _ := json.Marshal(body1)
		if err := m.client.post("/api/v1/settings/enabled-models", bytes.NewReader(jsonBody1), nil); err != nil {
			return cfgActionErrMsg{err: err}
		}

		// Save preferred model
		body2 := map[string]string{"model": m.currentModel}
		jsonBody2, _ := json.Marshal(body2)
		if err := m.client.post("/api/v1/settings/preferred-model", bytes.NewReader(jsonBody2), nil); err != nil {
			return cfgActionErrMsg{err: err}
		}

		msg := fmt.Sprintf("%d models enabled", len(enabled))
		if m.currentModel != "" {
			msg += ", default: " + m.currentModel
		}
		return cfgActionDoneMsg{msg: msg}
	}
}

// --- Helpers ---

func (m cfgModel) buildAddProviders() []string {
	// Build set of providers that already have keys
	hasKey := make(map[string]bool)
	for _, k := range m.keys.System {
		hasKey[k["provider"]] = true
	}
	for _, k := range m.keys.User {
		hasKey[k["provider"]] = true
	}
	for _, k := range m.keys.Env {
		hasKey[k["provider"]] = true
	}
	// Also include keyedProviders from models loading
	for p := range m.keyedProviders {
		hasKey[p] = true
	}
	// Return providers without keys, in fixed order
	var available []string
	for _, p := range cfgProviderOrder {
		if !hasKey[p] {
			available = append(available, p)
		}
	}
	return available
}

func (m cfgModel) buildDeleteItems() []cfgKeyItem {
	var items []cfgKeyItem
	for _, k := range m.keys.System {
		items = append(items, cfgKeyItem{
			provider: k["provider"],
			masked:   k["masked_key"],
			scope:    "system",
		})
	}
	for _, k := range m.keys.User {
		items = append(items, cfgKeyItem{
			provider: k["provider"],
			masked:   k["masked_key"],
			scope:    "user",
		})
	}
	// Env keys can't be deleted via API
	return items
}
