package cli

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// --- Steps ---

type appStep int

const (
	appStepList         appStep = iota // App list (default)
	appStepDetail                      // App detail + action menu
	appStepSessions                    // App's past sessions
	appStepCreateMode                  // Create: choose Form or Chat
	appStepCreateID                    // Create: ID input (kebab-case, required)
	appStepCreateName                  // Create: display name input
	appStepCreateDesc                  // Create: description (skippable)
	appStepCreatePrompt                // Create: prompt (required)
	appStepCreateModel                 // Create: select model
	appStepCreateSkills                // Create: select skills (multi)
	appStepCreateSchedule              // Create: schedule (skippable)
	appStepCreateConfirm               // Create: confirm summary
	appStepDone                        // Result message
)

// --- Messages ---

type appLoadedMsg struct {
	apps []appRecord
}
type appErrMsg struct {
	err error
}
type appActionDoneMsg struct {
	msg string
}
type appModelsLoadedMsg struct {
	models []appModelItem
}
type appSkillsLoadedMsg struct {
	skills []appSkillItem
}
type appSessionsLoadedMsg struct {
	sessions []appSessionItem
}
type appCtrlCResetMsg struct{}

// --- Data types ---

type appRecord struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Prompt        string `json:"prompt"`
	Model         string `json:"model"`
	Skills        string `json:"skills"`
	ScheduleRules string `json:"schedule_rules"`
	IsActive      bool   `json:"is_active"`
}

type appModelItem struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

type appSkillItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type appSessionItem struct {
	ID           string  `json:"ID"`
	Title        string  `json:"Title"`
	MessageCount int     `json:"MessageCount"`
	TotalCost    float64 `json:"TotalCost"`
	UpdatedAt    string  `json:"UpdatedAt"`
}

// --- Detail action menu ---

type appAction struct {
	id    string
	label string
	desc  string
}

// --- Model ---

const appVisibleItems = 14

type appModel struct {
	client   *apiClient
	step     appStep
	cursor   int
	offset   int // scroll offset
	quitting   bool
	exitReason tuiExitReason

	// List
	apps      []appRecord
	appsLoaded bool

	// Detail
	selectedIdx int
	selectedApp *appRecord
	actions     []appAction

	// Sessions
	sessions []appSessionItem

	// Create form
	idInput     textinput.Model
	nameInput   textinput.Model
	descInput   textinput.Model
	promptInput textarea.Model
	formID      string
	formName    string
	formDesc    string
	formPrompt  string
	formModel   string
	formSkills  []string
	formSched   string

	// Model/skill selection
	models       []appModelItem
	skills       []appSkillItem
	skillChecked map[string]bool

	// Schedule presets
	schedCursor int

	// Result
	resultMsg string
	resultOK  bool

	// Ctrl+C double-press
	ctrlCOnce bool

	// Post-exit action: launch chat
	launchChat        bool     // true = exit TUI and enter chat
	launchChatScope   string   // e.g. "" for global, "agent:app-xxx" for app scope
	launchChatSession string   // optional session ID to resume
	launchChatMessage string   // initial message to auto-send
	launchChatSkills  []string // skills to pre-load into the session
}

func newAppModel(client *apiClient) *appModel {
	ii := textinput.New()
	ii.Placeholder = "daily-weather"
	ii.CharLimit = 64
	ii.Width = 50

	ni := textinput.New()
	ni.Placeholder = "Display name (Enter to skip, defaults to ID)"
	ni.CharLimit = 100
	ni.Width = 50

	di := textinput.New()
	di.Placeholder = "What does this app do? (Enter to skip)"
	di.CharLimit = 200
	di.Width = 50

	pi := textarea.New()
	pi.Placeholder = "Enter the AI instruction..."
	pi.SetWidth(56)
	pi.SetHeight(5)
	pi.CharLimit = 4000

	return &appModel{
		client:       client,
		step:         appStepList,
		idInput:      ii,
		nameInput:    ni,
		descInput:    di,
		promptInput:  pi,
		skillChecked: make(map[string]bool),
	}
}

func (m *appModel) Init() tea.Cmd {
	return m.loadApps()
}

func (m *appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			if m.ctrlCOnce {
				m.quitting = true
				m.exitReason = exitQuit
				return m, tea.Quit
			}
			m.ctrlCOnce = true
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return appCtrlCResetMsg{} })
		}
		// Any other key resets ctrl+c state
		if m.ctrlCOnce {
			m.ctrlCOnce = false
		}
	case appCtrlCResetMsg:
		m.ctrlCOnce = false
		return m, nil
	case appLoadedMsg:
		m.apps = msg.apps
		m.appsLoaded = true
		return m, nil
	case appErrMsg:
		m.resultMsg = fmt.Sprintf("Error: %v", msg.err)
		m.resultOK = false
		m.step = appStepDone
		return m, nil
	case appActionDoneMsg:
		m.resultMsg = msg.msg
		m.resultOK = true
		m.step = appStepDone
		return m, nil
	case appModelsLoadedMsg:
		m.models = msg.models
		return m, nil
	case appSkillsLoadedMsg:
		m.skills = msg.skills
		return m, nil
	case appSessionsLoadedMsg:
		m.sessions = msg.sessions
		return m, nil
	}

	switch m.step {
	case appStepList:
		return m.updateList(msg)
	case appStepDetail:
		return m.updateDetail(msg)
	case appStepSessions:
		return m.updateSessions(msg)
	case appStepCreateMode:
		return m.updateCreateMode(msg)
	case appStepCreateID:
		return m.updateCreateID(msg)
	case appStepCreateName:
		return m.updateCreateName(msg)
	case appStepCreateDesc:
		return m.updateCreateDesc(msg)
	case appStepCreatePrompt:
		return m.updateCreatePrompt(msg)
	case appStepCreateModel:
		return m.updateCreateModel(msg)
	case appStepCreateSkills:
		return m.updateCreateSkills(msg)
	case appStepCreateSchedule:
		return m.updateCreateSchedule(msg)
	case appStepCreateConfirm:
		return m.updateCreateConfirm(msg)
	case appStepDone:
		return m.updateDone(msg)
	}

	return m, nil
}

func (m *appModel) View() string {
	if m.quitting {
		return ""
	}

	warn := m.ctrlCWarning()

	switch m.step {
	case appStepList:
		return "\n" + renderTUIBox("Apps", m.viewList()) + warn + "\n"
	case appStepDetail:
		name := "App"
		if m.selectedApp != nil {
			name = "App · " + m.selectedApp.Name
		}
		return "\n" + renderTUIBox(name, m.viewDetail()) + warn + "\n"
	case appStepSessions:
		name := "Sessions"
		if m.selectedApp != nil {
			name = m.selectedApp.Name + " · Sessions"
		}
		return "\n" + renderTUIBox(name, m.viewSessions()) + warn + "\n"
	case appStepCreateMode:
		return "\n" + renderTUIBox("Create App", m.viewCreateMode()) + warn + "\n"
	case appStepCreateID, appStepCreateName, appStepCreateDesc, appStepCreatePrompt,
		appStepCreateModel, appStepCreateSkills, appStepCreateSchedule,
		appStepCreateConfirm:
		return "\n" + renderTUIBox("Create App", m.viewCreate()) + warn + "\n"
	case appStepDone:
		return "\n" + renderTUIBox("Apps", m.viewDone()) + warn + "\n"
	}

	return ""
}

// --- API commands ---

func (m *appModel) loadApps() tea.Cmd {
	return func() tea.Msg {
		var apps []appRecord
		if err := m.client.get("/api/v1/apps", &apps); err != nil {
			return appErrMsg{err: err}
		}
		if apps == nil {
			apps = []appRecord{}
		}
		return appLoadedMsg{apps: apps}
	}
}

// --- Navigation helpers ---

func (m *appModel) goToList() {
	m.step = appStepList
	m.cursor = 0
	m.offset = 0
}

func (m *appModel) goToCreate() {
	m.step = appStepCreateMode
	m.cursor = 0
	m.formID = ""
	m.formName = ""
	m.formDesc = ""
	m.formPrompt = ""
	m.formModel = ""
	m.formSkills = nil
	m.formSched = ""
	m.skillChecked = make(map[string]bool)
	m.idInput.SetValue("")
	m.nameInput.SetValue("")
	m.descInput.SetValue("")
	m.promptInput.SetValue("")
}

func (m *appModel) ctrlCWarning() string {
	if m.ctrlCOnce {
		return "\n" + errorStyle.Render("Press Ctrl+C again to quit")
	}
	return ""
}

func (m *appModel) adjustOffset(total int) {
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+appVisibleItems {
		m.offset = m.cursor - appVisibleItems + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}
