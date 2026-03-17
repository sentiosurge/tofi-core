package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	qrterminal "github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
)

// --- tofi connector configure ---

var connConfigureCmd = &cobra.Command{
	Use:   "configure",
	Short: "Interactive wizard to set up a connector",
	Args:  cobra.NoArgs,
	RunE:  runConnConfigure,
}

// registered in connector_cmds.go init() to control ordering

// --- Steps ---

type connStep int

const (
	connStepLoading connStep = iota // initial: loading existing connectors
	connStepExisting                // show existing connectors + "Add new"
	connStepManageAction            // actions for a selected connector
	connStepType                    // choose new connector type
	connStepToken
	connStepWebhook
	connStepEmail
	connStepBindApp
	connStepSelectApp
	connStepConfirm
	connStepCreating
	connStepVerify
	connStepManageUsers  // list authorized users + add new
	connStepUserAction   // actions for selected user
	connStepDone
)

// --- Connector type options ---

type connTypeOption struct {
	id          string
	display     string
	description string
	needsToken  bool
	needsWebhok bool
	canReceive  bool
}

var connTypeOptions = []connTypeOption{
	{id: "telegram", display: "Telegram", description: "Easiest to set up. Two-way chat + notifications", needsToken: true, canReceive: true},
	{id: "slack_app", display: "Slack App", description: "Two-way messaging via Slack app", needsToken: true, canReceive: true},
	{id: "slack_webhook", display: "Slack Webhook", description: "Send notifications to a Slack channel", needsWebhok: true, canReceive: false},
	{id: "discord_bot", display: "Discord Bot", description: "Two-way messaging via Discord bot", needsToken: true, canReceive: true},
	{id: "discord_webhook", display: "Discord Webhook", description: "Send notifications to a Discord channel", needsWebhok: true, canReceive: false},
	{id: "email", display: "Email (SMTP)", description: "Send notifications and receive replies via email", canReceive: true},
}

// --- Model ---

type existingConnector struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	Name          string `json:"name"`
	AppID         string `json:"app_id"`
	AppName       string `json:"app_name"`
	Enabled       bool   `json:"enabled"`
	ReceiverCount int    `json:"receiver_count"`
	CanReceive    bool   `json:"can_receive"`
}

type manageAction int

const (
	manageReceivers manageAction = iota
	manageScope
	manageToken
	manageDelete
	manageBack
)

type connConfigModel struct {
	step     connStep
	cursor   int
	quitting bool
	err      error

	// Existing connectors
	existing       []existingConnector
	existingLoaded bool
	selectedExisting *existingConnector
	actionCursor   int

	// Selected type
	connType connTypeOption

	// Inputs
	tokenInput   textinput.Model
	webhookInput textinput.Model
	emailInputs  [4]textinput.Model // host, port, username, password
	emailFocus   int

	// App binding
	bindApp    bool
	appCursor  int
	apps       []appListItem
	appsLoaded bool
	selectedApp *appListItem

	// Authorized users management
	users        []connUser
	usersLoaded  bool
	userCursor   int
	selectedUser *connUser
	userActionCursor int

	// Result
	createdID      string
	verifyCode     string
	botUsername     string
	verifyDone     bool
	verifyReceiver string
	actionMsg      string // status message after action
}

type connUser struct {
	ID          int64  `json:"id"`
	Identifier  string `json:"identifier"`
	DisplayName string `json:"display_name"`
	VerifiedAt  string `json:"verified_at"`
}

type appListItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func newConnConfigModel() connConfigModel {
	ti := textinput.New()
	ti.Placeholder = "paste your bot token here..."
	ti.CharLimit = 256
	ti.Width = 60
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'

	wi := textinput.New()
	wi.Placeholder = "https://hooks.slack.com/..."
	wi.CharLimit = 512
	wi.Width = 60

	var emailInputs [4]textinput.Model
	placeholders := [4]string{"smtp.gmail.com", "587", "user@example.com", "password"}
	labels := [4]int{40, 10, 40, 40}
	for i := 0; i < 4; i++ {
		ei := textinput.New()
		ei.Placeholder = placeholders[i]
		ei.CharLimit = 128
		ei.Width = labels[i]
		if i == 3 {
			ei.EchoMode = textinput.EchoPassword
			ei.EchoCharacter = '•'
		}
		emailInputs[i] = ei
	}

	return connConfigModel{
		step:         connStepLoading,
		tokenInput:   ti,
		webhookInput: wi,
		emailInputs:  emailInputs,
	}
}

func (m connConfigModel) Init() tea.Cmd {
	return m.loadExisting()
}

// --- Update ---

func (m connConfigModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "esc":
			switch m.step {
			case connStepToken, connStepWebhook, connStepEmail:
				m.step = connStepType
				return m, nil
			case connStepBindApp:
				m.step = m.inputStepForType()
				return m, nil
			case connStepSelectApp:
				m.step = connStepBindApp
				return m, nil
			case connStepConfirm:
				m.step = connStepBindApp
				return m, nil
			case connStepType:
				if len(m.existing) > 0 {
					m.step = connStepExisting
					m.cursor = 0
					return m, nil
				}
			case connStepManageAction:
				m.step = connStepExisting
				m.cursor = 0
				return m, nil
			case connStepManageUsers:
				m.step = connStepManageAction
				m.actionCursor = 0
				m.actionMsg = ""
				return m, nil
			case connStepUserAction:
				m.step = connStepManageUsers
				m.userCursor = 0
				return m, nil
			}
		}
	case existingLoadedMsg:
		m.existing = msg.connectors
		m.existingLoaded = true
		if len(m.existing) > 0 {
			m.step = connStepExisting
			m.cursor = 0
		} else {
			m.step = connStepType
			m.cursor = 0
		}
		return m, nil
	case actionDoneMsg:
		if msg.err != nil {
			m.actionMsg = "Error: " + msg.err.Error()
		} else {
			m.actionMsg = msg.msg
		}
		// Reload existing connectors
		return m, m.loadExisting()
	case usersLoadedMsg:
		m.users = msg.users
		m.usersLoaded = true
		m.userCursor = 0
		return m, nil
	case userActionDoneMsg:
		if msg.err != nil {
			m.actionMsg = "Error: " + msg.err.Error()
		} else {
			m.actionMsg = msg.msg
		}
		if m.step == connStepUserAction {
			// Back to user list, reload
			m.step = connStepManageUsers
			m.userCursor = 0
			m.usersLoaded = false
			return m, m.loadUsers()
		}
		return m, nil
	case appsLoadedMsg:
		m.apps = msg.apps
		m.appsLoaded = true
		return m, nil
	case connCreatedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.step = connStepDone
			return m, tea.Quit
		}
		m.createdID = msg.id
		if m.connType.canReceive {
			m.step = connStepVerify
			return m, m.startVerify()
		}
		m.step = connStepDone
		return m, tea.Quit
	case verifyStartedMsg:
		if msg.err != nil {
			// Verify failed, but connector was created
			m.step = connStepDone
			return m, tea.Quit
		}
		m.verifyCode = msg.code
		m.botUsername = msg.botUsername
		return m, m.pollVerify()
	case verifyPollMsg:
		if msg.done {
			m.verifyDone = true
			m.verifyReceiver = msg.receiver
			if m.verifyReceiver != "" {
				go sendWelcomeMessage(m.createdID)
			}
			if m.selectedExisting != nil {
				// Back to manage users after adding
				m.actionMsg = ""
				if m.verifyReceiver != "" {
					m.actionMsg = "User added: " + m.verifyReceiver
				}
				m.step = connStepManageUsers
				m.userCursor = 0
				m.usersLoaded = false
				return m, m.loadUsers()
			}
			m.step = connStepDone
			return m, tea.Quit
		}
		if msg.attempts >= 60 {
			m.step = connStepDone
			return m, tea.Quit
		}
		return m, m.pollVerifyAfter(msg.attempts)
	}

	switch m.step {
	case connStepExisting:
		return m.updateExisting(msg)
	case connStepManageAction:
		return m.updateManageAction(msg)
	case connStepType:
		return m.updateType(msg)
	case connStepToken:
		return m.updateToken(msg)
	case connStepWebhook:
		return m.updateWebhook(msg)
	case connStepEmail:
		return m.updateEmail(msg)
	case connStepBindApp:
		return m.updateBindApp(msg)
	case connStepSelectApp:
		return m.updateSelectApp(msg)
	case connStepConfirm:
		return m.updateConfirm(msg)
	case connStepVerify:
		return m.updateVerify(msg)
	case connStepManageUsers:
		return m.updateManageUsers(msg)
	case connStepUserAction:
		return m.updateUserAction(msg)
	}

	return m, nil
}

func (m connConfigModel) inputStepForType() connStep {
	if m.connType.needsToken {
		return connStepToken
	}
	if m.connType.needsWebhok {
		return connStepWebhook
	}
	return connStepEmail
}

// --- Step updates ---

func (m connConfigModel) updateExisting(msg tea.Msg) (tea.Model, tea.Cmd) {
	// List: existing connectors + "Add new connector" at the end
	total := len(m.existing) + 1 // +1 for "Add new"
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
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
			if m.cursor == len(m.existing) {
				// "Add new connector"
				m.step = connStepType
				m.cursor = 0
				m.actionMsg = ""
				return m, nil
			}
			// Selected existing connector
			c := m.existing[m.cursor]
			m.selectedExisting = &c
			m.step = connStepManageAction
			m.actionCursor = 0
			m.actionMsg = ""
			return m, nil
		}
	}
	return m, nil
}

func (m connConfigModel) manageActions() []struct {
	action manageAction
	label  string
} {
	actions := []struct {
		action manageAction
		label  string
	}{
		{manageReceivers, "Manage authorized users"},
		{manageScope, "Change scope (global / app)"},
	}
	if m.selectedExisting != nil {
		// Token-based types
		switch {
		case strings.HasPrefix(m.selectedExisting.Type, "telegram"),
			strings.HasPrefix(m.selectedExisting.Type, "slack_app"),
			strings.HasPrefix(m.selectedExisting.Type, "discord_bot"):
			actions = append(actions, struct {
				action manageAction
				label  string
			}{manageToken, "Update token"})
		}
	}
	actions = append(actions,
		struct {
			action manageAction
			label  string
		}{manageDelete, "Delete connector"},
		struct {
			action manageAction
			label  string
		}{manageBack, "Back"},
	)
	return actions
}

func (m connConfigModel) updateManageAction(msg tea.Msg) (tea.Model, tea.Cmd) {
	actions := m.manageActions()
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "up", "k":
			if m.actionCursor > 0 {
				m.actionCursor--
			}
		case "down", "j":
			if m.actionCursor < len(actions)-1 {
				m.actionCursor++
			}
		case "enter":
			selected := actions[m.actionCursor]
			switch selected.action {
			case manageReceivers:
				// Show users list
				m.createdID = m.selectedExisting.ID
				m.connType = connTypeOptionByID(m.selectedExisting.Type)
				m.step = connStepManageUsers
				m.userCursor = 0
				m.usersLoaded = false
				return m, m.loadUsers()
			case manageScope:
				// Bind/unbind app — reuse bind flow
				m.createdID = m.selectedExisting.ID
				m.step = connStepBindApp
				m.cursor = 0
				return m, nil
			case manageToken:
				// Re-enter token
				m.connType = connTypeOptionByID(m.selectedExisting.Type)
				m.step = connStepToken
				m.tokenInput.SetValue("")
				return m, m.tokenInput.Focus()
			case manageDelete:
				return m, m.deleteSelectedConnector()
			case manageBack:
				m.step = connStepExisting
				m.cursor = 0
				return m, nil
			}
		}
	}
	return m, nil
}

func connTypeOptionByID(id string) connTypeOption {
	for _, opt := range connTypeOptions {
		if opt.id == id {
			return opt
		}
	}
	return connTypeOptions[0]
}

func (m connConfigModel) updateType(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(connTypeOptions)-1 {
				m.cursor++
			}
		case "enter":
			m.connType = connTypeOptions[m.cursor]
			switch {
			case m.connType.needsToken:
				m.step = connStepToken
				return m, m.tokenInput.Focus()
			case m.connType.needsWebhok:
				m.step = connStepWebhook
				return m, m.webhookInput.Focus()
			default:
				m.step = connStepEmail
				return m, m.emailInputs[0].Focus()
			}
		}
	}
	return m, nil
}

func (m connConfigModel) updateToken(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.String() == "enter" {
			val := strings.TrimSpace(m.tokenInput.Value())
			if val == "" {
				return m, nil
			}
			m.step = connStepBindApp
			m.cursor = 0
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.tokenInput, cmd = m.tokenInput.Update(msg)
	return m, cmd
}

func (m connConfigModel) updateWebhook(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.String() == "enter" {
			val := strings.TrimSpace(m.webhookInput.Value())
			if val == "" {
				return m, nil
			}
			m.step = connStepBindApp
			m.cursor = 0
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.webhookInput, cmd = m.webhookInput.Update(msg)
	return m, cmd
}

func (m connConfigModel) updateEmail(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter":
			if m.emailFocus < 3 {
				m.emailInputs[m.emailFocus].Blur()
				m.emailFocus++
				return m, m.emailInputs[m.emailFocus].Focus()
			}
			// All filled
			m.step = connStepBindApp
			m.cursor = 0
			return m, nil
		case "shift+tab":
			if m.emailFocus > 0 {
				m.emailInputs[m.emailFocus].Blur()
				m.emailFocus--
				return m, m.emailInputs[m.emailFocus].Focus()
			}
		}
	}
	var cmd tea.Cmd
	m.emailInputs[m.emailFocus], cmd = m.emailInputs[m.emailFocus].Update(msg)
	return m, cmd
}

func (m connConfigModel) updateBindApp(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < 1 {
				m.cursor++
			}
		case "enter":
			if m.cursor == 0 {
				// Global (no app binding)
				m.bindApp = false
				m.selectedApp = nil
				m.step = connStepConfirm
				return m, nil
			}
			// Bind to app — load app list
			m.bindApp = true
			m.step = connStepSelectApp
			m.appCursor = 0
			if !m.appsLoaded {
				return m, m.loadApps()
			}
			return m, nil
		}
	}
	return m, nil
}

func (m connConfigModel) updateSelectApp(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.appsLoaded {
		return m, nil
	}
	if len(m.apps) == 0 {
		// No apps, go to confirm without binding
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			if keyMsg.String() == "enter" {
				m.bindApp = false
				m.selectedApp = nil
				m.step = connStepConfirm
				return m, nil
			}
		}
		return m, nil
	}

	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "up", "k":
			if m.appCursor > 0 {
				m.appCursor--
			}
		case "down", "j":
			if m.appCursor < len(m.apps)-1 {
				m.appCursor++
			}
		case "enter":
			app := m.apps[m.appCursor]
			m.selectedApp = &app
			m.step = connStepConfirm
			return m, nil
		}
	}
	return m, nil
}

func (m connConfigModel) updateConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter", "y", "Y":
			m.step = connStepCreating
			return m, m.createConnector()
		case "n", "N":
			m.step = connStepType
			m.cursor = 0
			m.tokenInput.SetValue("")
			m.webhookInput.SetValue("")
			m.selectedApp = nil
			return m, nil
		}
	}
	return m, nil
}

func (m connConfigModel) updateManageUsers(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.usersLoaded {
		return m, nil
	}
	// items: existing users + "Add new user" + "Back"
	total := len(m.users) + 2
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "up", "k":
			if m.userCursor > 0 {
				m.userCursor--
			}
		case "down", "j":
			if m.userCursor < total-1 {
				m.userCursor++
			}
		case "enter":
			if m.userCursor < len(m.users) {
				// Selected a user
				u := m.users[m.userCursor]
				m.selectedUser = &u
				m.step = connStepUserAction
				m.userActionCursor = 0
				m.actionMsg = ""
				return m, nil
			} else if m.userCursor == len(m.users) {
				// "Add new user" → verify flow
				m.step = connStepVerify
				m.verifyCode = ""
				m.verifyDone = false
				return m, m.startVerify()
			} else {
				// "Back"
				m.step = connStepManageAction
				m.actionCursor = 0
				return m, nil
			}
		}
	}
	return m, nil
}

func (m connConfigModel) updateUserAction(msg tea.Msg) (tea.Model, tea.Cmd) {
	// 0: Send test message, 1: Remove user, 2: Back
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "up", "k":
			if m.userActionCursor > 0 {
				m.userActionCursor--
			}
		case "down", "j":
			if m.userActionCursor < 2 {
				m.userActionCursor++
			}
		case "enter":
			switch m.userActionCursor {
			case 0: // Send test
				return m, m.testUser(m.selectedUser.ID)
			case 1: // Remove
				return m, m.deleteUser(m.selectedUser.ID)
			case 2: // Back
				m.step = connStepManageUsers
				m.userCursor = 0
				return m, nil
			}
		}
	}
	return m, nil
}

func (m connConfigModel) updateVerify(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.String() == "enter" {
			if m.selectedExisting != nil {
				// Back to manage users
				m.step = connStepManageUsers
				m.userCursor = 0
				m.usersLoaded = false
				return m, m.loadUsers()
			}
			// Skip verification (new connector flow)
			m.step = connStepDone
			return m, tea.Quit
		}
	}
	return m, nil
}

// --- Commands (async) ---

type existingLoadedMsg struct {
	connectors []existingConnector
}

type actionDoneMsg struct {
	msg string
	err error
}

type usersLoadedMsg struct {
	users []connUser
}

type userActionDoneMsg struct {
	msg string
	err error
}

type appsLoadedMsg struct {
	apps []appListItem
}

type connCreatedMsg struct {
	id  string
	err error
}

type verifyStartedMsg struct {
	code        string
	botUsername string
	err         error
}

type verifyPollMsg struct {
	done     bool
	receiver string
	attempts int
}

func (m connConfigModel) loadExisting() tea.Cmd {
	return func() tea.Msg {
		client := newAPIClient()
		var connectors []existingConnector
		if err := client.get("/api/v1/connectors", &connectors); err != nil {
			return existingLoadedMsg{connectors: nil}
		}
		return existingLoadedMsg{connectors: connectors}
	}
}

func (m connConfigModel) deleteSelectedConnector() tea.Cmd {
	return func() tea.Msg {
		client := newAPIClient()
		if err := client.delete("/api/v1/connectors/" + m.selectedExisting.ID); err != nil {
			return actionDoneMsg{err: err}
		}
		return actionDoneMsg{msg: "Connector deleted"}
	}
}

func (m connConfigModel) loadUsers() tea.Cmd {
	return func() tea.Msg {
		client := newAPIClient()
		var users []connUser
		if err := client.get(fmt.Sprintf("/api/v1/connectors/%s/receivers", m.createdID), &users); err != nil {
			return usersLoadedMsg{users: nil}
		}
		return usersLoadedMsg{users: users}
	}
}

func (m connConfigModel) deleteUser(userID int64) tea.Cmd {
	return func() tea.Msg {
		client := newAPIClient()
		if err := client.delete(fmt.Sprintf("/api/v1/connectors/%s/receivers/%d", m.createdID, userID)); err != nil {
			return userActionDoneMsg{err: err}
		}
		return userActionDoneMsg{msg: "User removed"}
	}
}

func (m connConfigModel) testUser(userID int64) tea.Cmd {
	return func() tea.Msg {
		client := newAPIClient()
		body := map[string]any{"receiver_id": userID}
		bodyJSON, _ := json.Marshal(body)
		var result struct {
			OK bool `json:"ok"`
		}
		if err := client.post(fmt.Sprintf("/api/v1/connectors/%s/test", m.createdID), bytes.NewReader(bodyJSON), &result); err != nil {
			return userActionDoneMsg{err: err}
		}
		return userActionDoneMsg{msg: "Test message sent"}
	}
}

func (m connConfigModel) loadApps() tea.Cmd {
	return func() tea.Msg {
		client := newAPIClient()
		var apps []appListItem
		if err := client.get("/api/v1/apps", &apps); err != nil {
			return appsLoadedMsg{apps: nil}
		}
		return appsLoadedMsg{apps: apps}
	}
}

func (m connConfigModel) createConnector() tea.Cmd {
	return func() tea.Msg {
		client := newAPIClient()
		config := map[string]string{}
		switch {
		case m.connType.needsToken:
			config["bot_token"] = strings.TrimSpace(m.tokenInput.Value())
		case m.connType.needsWebhok:
			config["webhook_url"] = strings.TrimSpace(m.webhookInput.Value())
		default:
			config["smtp_host"] = strings.TrimSpace(m.emailInputs[0].Value())
			config["smtp_port"] = strings.TrimSpace(m.emailInputs[1].Value())
			config["username"] = strings.TrimSpace(m.emailInputs[2].Value())
			config["password"] = strings.TrimSpace(m.emailInputs[3].Value())
		}

		configJSON, _ := json.Marshal(config)
		appID := ""
		if m.selectedApp != nil {
			appID = m.selectedApp.ID
		}

		body := map[string]any{
			"type":   m.connType.id,
			"app_id": appID,
			"config": json.RawMessage(configJSON),
		}
		bodyJSON, _ := json.Marshal(body)

		var result struct {
			ID string `json:"id"`
		}
		if err := client.post("/api/v1/connectors", bytes.NewReader(bodyJSON), &result); err != nil {
			return connCreatedMsg{err: err}
		}
		return connCreatedMsg{id: result.ID}
	}
}

func (m connConfigModel) startVerify() tea.Cmd {
	return func() tea.Msg {
		client := newAPIClient()
		var result struct {
			Code        string `json:"code"`
			BotUsername string `json:"bot_username"`
		}
		if err := client.post(fmt.Sprintf("/api/v1/connectors/%s/verify", m.createdID), nil, &result); err != nil {
			return verifyStartedMsg{err: err}
		}
		return verifyStartedMsg{code: result.Code, botUsername: result.BotUsername}
	}
}

func (m connConfigModel) pollVerify() tea.Cmd {
	return m.pollVerifyAfter(0)
}

func (m connConfigModel) pollVerifyAfter(attempts int) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(5 * time.Second)
		client := newAPIClient()

		var status struct {
			Verifying bool `json:"verifying"`
		}
		if err := client.get(fmt.Sprintf("/api/v1/connectors/%s/verify-status", m.createdID), &status); err != nil {
			return verifyPollMsg{done: false, attempts: attempts + 1}
		}
		if !status.Verifying {
			// Check receivers
			var receivers []struct {
				DisplayName string `json:"display_name"`
			}
			if err := client.get(fmt.Sprintf("/api/v1/connectors/%s/receivers", m.createdID), &receivers); err == nil && len(receivers) > 0 {
				return verifyPollMsg{done: true, receiver: receivers[len(receivers)-1].DisplayName}
			}
			return verifyPollMsg{done: true}
		}
		return verifyPollMsg{done: false, attempts: attempts + 1}
	}
}

// --- View ---

func (m connConfigModel) View() string {
	if m.quitting {
		return subtitleStyle.Render("  Cancelled.\n")
	}

	var s strings.Builder
	s.WriteString("\n")
	s.WriteString(titleStyle.Render("  Connector Setup") + "\n\n")

	switch m.step {
	case connStepLoading:
		s.WriteString(subtitleStyle.Render("  Loading connectors...") + "\n")

	case connStepExisting:
		s.WriteString(subtitleStyle.Render("  Your connectors:") + "\n\n")
		if m.actionMsg != "" {
			s.WriteString(successStyle.Render("  "+m.actionMsg) + "\n\n")
		}
		for i, c := range m.existing {
			cursor := "  "
			nameStyle := subtitleStyle
			if i == m.cursor {
				cursor = accentStyle.Render("❯ ")
				nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc"))
			}
			icon := connectorIcon(c.Type)
			scope := subtitleStyle.Render("global")
			if c.AppName != "" {
				scope = lipgloss.NewStyle().Foreground(lipgloss.Color("#d2a8ff")).Render("app: " + c.AppName)
			} else if c.AppID != "" {
				scope = lipgloss.NewStyle().Foreground(lipgloss.Color("#d2a8ff")).Render("app: " + c.AppID[:8])
			}
			receivers := subtitleStyle.Render(fmt.Sprintf("%d users", c.ReceiverCount))
			s.WriteString("  " + cursor + icon + " " + nameStyle.Render(c.Type) + "  " + scope + "  " + receivers + "\n")
		}
		// "Add new" option
		addIdx := len(m.existing)
		addCursor := "  "
		addStyle := subtitleStyle
		if m.cursor == addIdx {
			addCursor = accentStyle.Render("❯ ")
			addStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7ee787"))
		} else {
			addStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#7ee787"))
		}
		s.WriteString("\n  " + addCursor + addStyle.Render("+ Add new connector") + "\n")
		s.WriteString("\n" + subtitleStyle.Render("  ↑/↓ navigate · Enter select · Ctrl+C exit"))

	case connStepManageAction:
		c := m.selectedExisting
		icon := connectorIcon(c.Type)
		scope := "global"
		if c.AppName != "" {
			scope = "app: " + c.AppName
		} else if c.AppID != "" {
			scope = "app: " + c.AppID[:8]
		}
		s.WriteString(subtitleStyle.Render("  Managing: ") + icon + " " + accentStyle.Render(c.Type) + " " + subtitleStyle.Render("("+scope+")") + "\n")
		s.WriteString(subtitleStyle.Render("  ID: "+c.ID) + "\n\n")

		actions := m.manageActions()
		for i, a := range actions {
			cursor := "  "
			nameStyle := subtitleStyle
			if i == m.actionCursor {
				cursor = accentStyle.Render("❯ ")
				nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc"))
			}
			label := a.label
			if a.action == manageDelete {
				if i == m.actionCursor {
					nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff7b72"))
				} else {
					nameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff7b72"))
				}
			}
			s.WriteString("  " + cursor + nameStyle.Render(label) + "\n")
		}
		s.WriteString("\n" + subtitleStyle.Render("  ↑/↓ navigate · Enter select · Esc back"))

	case connStepType:
		s.WriteString(subtitleStyle.Render("  Choose connector type:") + "\n\n")
		for i, opt := range connTypeOptions {
			cursor := "  "
			nameStyle := subtitleStyle
			if i == m.cursor {
				cursor = accentStyle.Render("❯ ")
				nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc"))
			}
			icon := connectorIcon(opt.id)
			s.WriteString("  " + cursor + icon + " " + nameStyle.Render(opt.display) + "\n")
			mode := subtitleStyle.Render("[notify-only]")
			if opt.canReceive {
				mode = lipgloss.NewStyle().Foreground(lipgloss.Color("#7ee787")).Render("[interactive]")
			}
			s.WriteString("       " + mode + " " + subtitleStyle.Render(opt.description) + "\n\n")
		}
		escHint := ""
		if len(m.existing) > 0 {
			escHint = " · Esc back"
		}
		s.WriteString(subtitleStyle.Render("  ↑/↓ navigate · Enter select" + escHint + " · Ctrl+C cancel"))

	case connStepToken:
		s.WriteString(subtitleStyle.Render("  Enter your "+m.connType.display+" token:") + "\n\n")
		s.WriteString("  " + m.tokenInput.View() + "\n\n")
		// Per-type hints
		hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#d2a8ff"))
		switch m.connType.id {
		case "telegram":
			s.WriteString(hintStyle.Render("  How to get a token:") + "\n")
			s.WriteString(subtitleStyle.Render("  1. Open Telegram and search for ") + accentStyle.Render("@BotFather") + "\n")
			s.WriteString(subtitleStyle.Render("  2. Send ") + accentStyle.Render("/newbot") + subtitleStyle.Render(" and follow the prompts") + "\n")
			s.WriteString(subtitleStyle.Render("  3. Copy the token BotFather gives you") + "\n\n")
		case "slack_app":
			s.WriteString(hintStyle.Render("  How to get a token:") + "\n")
			s.WriteString(subtitleStyle.Render("  1. Go to ") + accentStyle.Render("api.slack.com/apps") + subtitleStyle.Render(" and create an app") + "\n")
			s.WriteString(subtitleStyle.Render("  2. Under OAuth & Permissions, copy the Bot User OAuth Token (xoxb-...)") + "\n\n")
		case "discord_bot":
			s.WriteString(hintStyle.Render("  How to get a token:") + "\n")
			s.WriteString(subtitleStyle.Render("  1. Go to ") + accentStyle.Render("discord.com/developers/applications") + "\n")
			s.WriteString(subtitleStyle.Render("  2. Create an app, go to Bot tab, and copy the token") + "\n\n")
		}
		s.WriteString(subtitleStyle.Render("  Enter to continue · Esc go back"))

	case connStepWebhook:
		s.WriteString(subtitleStyle.Render("  Enter your "+m.connType.display+" webhook URL:") + "\n\n")
		s.WriteString("  " + m.webhookInput.View() + "\n\n")
		s.WriteString(subtitleStyle.Render("  Enter to continue · Esc go back"))

	case connStepEmail:
		labels := [4]string{"SMTP Host", "SMTP Port", "Username", "Password"}
		s.WriteString(subtitleStyle.Render("  Configure email (SMTP):") + "\n\n")
		for i, label := range labels {
			prefix := "  "
			if i == m.emailFocus {
				prefix = accentStyle.Render("❯ ")
			}
			s.WriteString("  " + prefix + subtitleStyle.Render(label+": ") + m.emailInputs[i].View() + "\n")
		}
		s.WriteString("\n" + subtitleStyle.Render("  Enter next field · Esc go back"))

	case connStepBindApp:
		s.WriteString(subtitleStyle.Render("  Bind this connector to an app?") + "\n\n")
		options := []struct {
			name string
			desc string
		}{
			{"One bot for all (recommended)", "Available to all apps, serves as the main Tofi channel"},
			{"Bind to a specific app", "Dedicated to one app, conversations scoped to that app"},
		}
		for i, opt := range options {
			cursor := "  "
			nameStyle := subtitleStyle
			if i == m.cursor {
				cursor = accentStyle.Render("❯ ")
				nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc"))
			}
			s.WriteString("  " + cursor + nameStyle.Render(opt.name) + "\n")
			s.WriteString("       " + subtitleStyle.Render(opt.desc) + "\n\n")
		}
		s.WriteString(subtitleStyle.Render("  ↑/↓ navigate · Enter select · Esc go back"))

	case connStepSelectApp:
		if !m.appsLoaded {
			s.WriteString(subtitleStyle.Render("  Loading apps...") + "\n")
		} else if len(m.apps) == 0 {
			s.WriteString(subtitleStyle.Render("  No apps found. Create one first with: ") + accentStyle.Render("tofi app create") + "\n\n")
			s.WriteString(subtitleStyle.Render("  Press Enter to continue as global connector."))
		} else {
			s.WriteString(subtitleStyle.Render("  Select an app:") + "\n\n")
			for i, app := range m.apps {
				cursor := "  "
				nameStyle := subtitleStyle
				if i == m.appCursor {
					cursor = accentStyle.Render("❯ ")
					nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc"))
				}
				name := app.Name
				if name == "" {
					name = app.ID[:8]
				}
				s.WriteString("  " + cursor + nameStyle.Render(name) + "\n")
			}
			s.WriteString("\n" + subtitleStyle.Render("  ↑/↓ navigate · Enter select · Esc go back"))
		}

	case connStepConfirm:
		s.WriteString(subtitleStyle.Render("  Review your configuration:") + "\n\n")

		icon := connectorIcon(m.connType.id)
		var lines []string
		lines = append(lines, subtitleStyle.Render("Type      ")+icon+" "+accentStyle.Render(m.connType.display))

		switch {
		case m.connType.needsToken:
			lines = append(lines, subtitleStyle.Render("Token     ")+accentStyle.Render(maskKey(m.tokenInput.Value())))
		case m.connType.needsWebhok:
			url := m.webhookInput.Value()
			if len(url) > 40 {
				url = url[:37] + "..."
			}
			lines = append(lines, subtitleStyle.Render("Webhook   ")+accentStyle.Render(url))
		default:
			lines = append(lines, subtitleStyle.Render("SMTP      ")+accentStyle.Render(m.emailInputs[0].Value()+":"+m.emailInputs[1].Value()))
			lines = append(lines, subtitleStyle.Render("User      ")+accentStyle.Render(m.emailInputs[2].Value()))
		}

		scope := "Global"
		if m.selectedApp != nil {
			name := m.selectedApp.Name
			if name == "" {
				name = m.selectedApp.ID[:8]
			}
			scope = "App: " + name
		}
		lines = append(lines, subtitleStyle.Render("Scope     ")+accentStyle.Render(scope))

		box := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#30363d")).
			Padding(1, 2).
			Width(56)

		s.WriteString(box.Render(strings.Join(lines, "\n")) + "\n\n")
		s.WriteString("  " + accentStyle.Render("Y") + subtitleStyle.Render("/Enter confirm · ") + accentStyle.Render("N") + subtitleStyle.Render(" start over"))

	case connStepCreating:
		s.WriteString(subtitleStyle.Render("  Creating connector...") + "\n")

	case connStepVerify:
		if m.selectedExisting != nil {
			s.WriteString(subtitleStyle.Render("  Add authorized user:") + "\n\n")
		} else {
			s.WriteString(successStyle.Render("  ✓ Connector created") + " " + subtitleStyle.Render("ID: "+m.createdID) + "\n\n")
			s.WriteString(subtitleStyle.Render("  Connect your first user:") + "\n\n")
		}
		if m.verifyCode == "" {
			s.WriteString(subtitleStyle.Render("  Starting verification...") + "\n")
		} else if m.connType.id == "telegram" && m.botUsername != "" {
			// Telegram: show QR code with deep link
			deepLink := fmt.Sprintf("https://t.me/%s?start=%s", m.botUsername, m.verifyCode)
			s.WriteString(subtitleStyle.Render("  Scan this QR code to connect:") + "\n\n")
			s.WriteString(renderQR(deepLink))
			s.WriteString("\n")
			s.WriteString(subtitleStyle.Render("  Or open: ") + accentStyle.Render(deepLink) + "\n\n")
			s.WriteString(subtitleStyle.Render("  Waiting for verification...") + "\n\n")
			s.WriteString(subtitleStyle.Render("  Press ") + accentStyle.Render("Enter") + subtitleStyle.Render(" to skip (configure later)"))
		} else {
			codeStyle := lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#ff7b72")).
				Background(lipgloss.Color("#21262d")).
				Padding(0, 2)
			s.WriteString("  " + codeStyle.Render(m.verifyCode) + "\n\n")
			if m.botUsername != "" {
				s.WriteString(subtitleStyle.Render("  Send this code to ") +
					accentStyle.Render("@"+m.botUsername) + "\n")
			} else {
				s.WriteString(subtitleStyle.Render("  Send this code to your bot") + "\n")
			}
			s.WriteString(subtitleStyle.Render("  Waiting for verification...") + "\n\n")
			s.WriteString(subtitleStyle.Render("  Press ") + accentStyle.Render("Enter") + subtitleStyle.Render(" to skip (configure later)"))
		}

	case connStepManageUsers:
		s.WriteString(subtitleStyle.Render("  Authorized users:") + "\n\n")
		if m.actionMsg != "" {
			s.WriteString(successStyle.Render("  "+m.actionMsg) + "\n\n")
		}
		if !m.usersLoaded {
			s.WriteString(subtitleStyle.Render("  Loading...") + "\n")
		} else {
			if len(m.users) == 0 {
				s.WriteString(subtitleStyle.Render("  No authorized users yet.") + "\n\n")
			} else {
				for i, u := range m.users {
					cursor := "  "
					nameStyle := subtitleStyle
					if i == m.userCursor {
						cursor = accentStyle.Render("❯ ")
						nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc"))
					}
					name := u.DisplayName
					if name == "" {
						name = u.Identifier
					}
					verified := ""
					if u.VerifiedAt != "" {
						verified = subtitleStyle.Render(" · verified " + formatTimeShort(u.VerifiedAt))
					}
					s.WriteString("  " + cursor + nameStyle.Render(name) + verified + "\n")
				}
			}
			// "Add new user"
			addIdx := len(m.users)
			addCursor := "  "
			addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7ee787"))
			if m.userCursor == addIdx {
				addCursor = accentStyle.Render("❯ ")
				addStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7ee787"))
			}
			s.WriteString("\n  " + addCursor + addStyle.Render("+ Add new user") + "\n")
			// "Back"
			backIdx := len(m.users) + 1
			backCursor := "  "
			backStyle := subtitleStyle
			if m.userCursor == backIdx {
				backCursor = accentStyle.Render("❯ ")
				backStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc"))
			}
			s.WriteString("  " + backCursor + backStyle.Render("Back") + "\n")
			s.WriteString("\n" + subtitleStyle.Render("  ↑/↓ navigate · Enter select · Esc back"))
		}

	case connStepUserAction:
		name := m.selectedUser.DisplayName
		if name == "" {
			name = m.selectedUser.Identifier
		}
		s.WriteString(subtitleStyle.Render("  User: ") + accentStyle.Render(name) + "\n\n")
		if m.actionMsg != "" {
			s.WriteString(successStyle.Render("  "+m.actionMsg) + "\n\n")
		}
		userActions := []struct {
			label string
			color string
		}{
			{"Send test message", "#7ee787"},
			{"Remove user", "#ff7b72"},
			{"Back", ""},
		}
		for i, a := range userActions {
			cursor := "  "
			nameStyle := subtitleStyle
			if i == m.userActionCursor {
				cursor = accentStyle.Render("❯ ")
				if a.color != "" {
					nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(a.color))
				} else {
					nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc"))
				}
			} else if a.color != "" {
				nameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(a.color))
			}
			s.WriteString("  " + cursor + nameStyle.Render(a.label) + "\n")
		}
		s.WriteString("\n" + subtitleStyle.Render("  ↑/↓ navigate · Enter select · Esc back"))

	case connStepDone:
		if m.err != nil {
			s.WriteString(errorStyle.Render("  ✗ Failed: "+m.err.Error()) + "\n")
		} else {
			s.WriteString(successStyle.Render("  ✓ Connector created!") + "\n")
			s.WriteString(subtitleStyle.Render("    ID: ") + accentStyle.Render(m.createdID) + "\n")
			s.WriteString(subtitleStyle.Render("    Type: ") + accentStyle.Render(m.connType.display) + "\n")
			if m.selectedApp != nil {
				name := m.selectedApp.Name
				if name == "" {
					name = m.selectedApp.ID[:8]
				}
				s.WriteString(subtitleStyle.Render("    App: ") + accentStyle.Render(name) + "\n")
			}
			if m.verifyDone && m.verifyReceiver != "" {
				s.WriteString(successStyle.Render("    ✓ Verified: ") + accentStyle.Render(m.verifyReceiver) + "\n")
			} else if m.connType.canReceive && !m.verifyDone {
				s.WriteString("\n" + subtitleStyle.Render("    Verification timed out or skipped.") + "\n")
				s.WriteString(subtitleStyle.Render("    Add receivers later: ") + accentStyle.Render("tofi connector verify "+m.createdID) + "\n")
			}
			s.WriteString("\n" + subtitleStyle.Render("  Next steps:") + "\n")
			if !m.verifyDone && m.connType.canReceive {
				s.WriteString("    " + accentStyle.Render("tofi connector verify "+m.createdID) + subtitleStyle.Render(" — add receivers") + "\n")
			}
			s.WriteString("    " + accentStyle.Render("tofi connector test "+m.createdID) + subtitleStyle.Render(" — send a test message") + "\n")
			s.WriteString("    " + accentStyle.Render("tofi connector list") + subtitleStyle.Render(" — view all connectors") + "\n")
		}
	}

	s.WriteString("\n")
	return s.String()
}

// --- QR rendering ---

func renderQR(url string) string {
	var buf bytes.Buffer
	qrterminal.GenerateHalfBlock(url, qrterminal.L, &buf)
	// Indent each line
	lines := strings.Split(buf.String(), "\n")
	var result strings.Builder
	for _, line := range lines {
		if line != "" {
			result.WriteString("  " + line + "\n")
		}
	}
	return result.String()
}

// --- Send welcome message on verify success ---

func sendWelcomeMessage(connectorID string) {
	client := newAPIClient()
	body := map[string]any{
		"message": "Welcome to Tofi! Your connector is set up and ready to go.",
	}
	bodyJSON, _ := json.Marshal(body)
	var result struct {
		OK bool `json:"ok"`
	}
	// Best-effort, ignore errors
	_ = client.post(fmt.Sprintf("/api/v1/connectors/%s/test", connectorID), bytes.NewReader(bodyJSON), &result)
}

// --- Runner ---

func runConnConfigure(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	p := tea.NewProgram(newConnConfigModel())
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	m := finalModel.(connConfigModel)
	if m.quitting {
		return nil
	}
	return m.err
}
