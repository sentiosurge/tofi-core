package cli

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

// --- Users List ---

type userRecord struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

type usersView int

const (
	usersViewList usersView = iota
	usersViewCreate
	usersViewConfirmDelete
)

type usersModel struct {
	users      []userRecord
	cursor     int
	view       usersView
	loading    bool
	errMsg     string
	successMsg string

	// Create form
	createUser  textinput.Model
	createPass  textinput.Model
	createRole  string // "user" or "admin"
	createFocus int    // 0=user, 1=pass, 2=role

	// Delete confirm
	deleteTarget *userRecord

	ctrlC ctrlCGuard
}

type adminUsersLoadedMsg struct {
	users []userRecord
	err   error
}
type adminUserCreatedMsg struct{ err error }
type adminUserDeletedMsg struct{ err error }

func newUsersModel() *usersModel {
	u := textinput.New()
	u.Placeholder = "username"
	u.CharLimit = 64

	p := textinput.New()
	p.Placeholder = "password (min 6 chars)"
	p.EchoMode = textinput.EchoPassword
	p.CharLimit = 128

	return &usersModel{
		loading:    true,
		createUser: u,
		createPass: p,
		createRole: "user",
	}
}

func (m *usersModel) Init() tea.Cmd {
	return m.loadUsers
}

func (m *usersModel) loadUsers() tea.Msg {
	client := newAPIClient()
	var users []userRecord
	err := client.get("/api/v1/admin/users", &users)
	return adminUsersLoadedMsg{users: users, err: err}
}

func (m *usersModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case adminUsersLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.errMsg = msg.err.Error()
		} else {
			m.users = msg.users
			if m.cursor >= len(m.users) {
				m.cursor = max(0, len(m.users)-1)
			}
		}
		return m, nil

	case adminUserCreatedMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.successMsg = "User created"
		m.view = usersViewList
		m.loading = true
		return m, m.loadUsers

	case adminUserDeletedMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.view = usersViewList
			return m, nil
		}
		m.successMsg = "User deleted"
		m.view = usersViewList
		m.loading = true
		return m, m.loadUsers

	case tea.KeyMsg:
		m.errMsg = ""
		m.successMsg = ""

		switch m.view {
		case usersViewList:
			return m.updateList(msg)
		case usersViewCreate:
			return m.updateCreate(msg)
		case usersViewConfirmDelete:
			return m.updateConfirmDelete(msg)
		}

	case ctrlCResetMsg:
		m.ctrlC.HandleReset()
		return m, nil
	}

	// Pass through for textinput updates
	if m.view == usersViewCreate {
		var cmd tea.Cmd
		if m.createFocus == 0 {
			m.createUser, cmd = m.createUser.Update(msg)
		} else if m.createFocus == 1 {
			m.createPass, cmd = m.createPass.Update(msg)
		}
		return m, cmd
	}

	return m, nil
}

func (m *usersModel) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if quit, cmd := m.ctrlC.HandleCtrlC(); quit {
			return m, tea.Quit
		} else {
			return m, cmd
		}
	case "esc", "q":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.users)-1 {
			m.cursor++
		}
	case "n":
		m.view = usersViewCreate
		m.createUser.SetValue("")
		m.createPass.SetValue("")
		m.createRole = "user"
		m.createFocus = 0
		m.createUser.Focus()
		m.createPass.Blur()
		return m, textinput.Blink
	case "d":
		if len(m.users) > 0 {
			m.deleteTarget = &m.users[m.cursor]
			m.view = usersViewConfirmDelete
		}
	default:
		m.ctrlC.HandleReset()
	}
	return m, nil
}

func (m *usersModel) updateCreate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc":
		m.view = usersViewList
		return m, nil
	case "tab":
		m.createFocus = (m.createFocus + 1) % 3
		m.createUser.Blur()
		m.createPass.Blur()
		if m.createFocus == 0 {
			m.createUser.Focus()
		} else if m.createFocus == 1 {
			m.createPass.Focus()
		}
		return m, nil
	case "enter":
		if m.createFocus == 2 {
			// Toggle role
			if m.createRole == "user" {
				m.createRole = "admin"
			} else {
				m.createRole = "user"
			}
			return m, nil
		}
		if m.createFocus < 2 {
			m.createFocus++
			m.createUser.Blur()
			m.createPass.Blur()
			if m.createFocus == 1 {
				m.createPass.Focus()
			}
			return m, nil
		}
	case "ctrl+s":
		return m, m.submitCreate()
	}

	// Pass character input to focused textinput
	var cmd tea.Cmd
	if m.createFocus == 0 {
		m.createUser, cmd = m.createUser.Update(msg)
	} else if m.createFocus == 1 {
		m.createPass, cmd = m.createPass.Update(msg)
	}
	return m, cmd
}

func (m *usersModel) submitCreate() tea.Cmd {
	user := m.createUser.Value()
	pass := m.createPass.Value()
	if user == "" || len(pass) < 6 {
		m.errMsg = "Username required, password min 6 chars"
		return nil
	}
	role := m.createRole
	return func() tea.Msg {
		client := newAPIClient()
		body, _ := json.Marshal(map[string]string{
			"username": user,
			"password": pass,
			"role":     role,
		})
		var resp map[string]string
		err := client.post("/api/v1/admin/users", bytes.NewReader(body), &resp)
		return adminUserCreatedMsg{err: err}
	}
}

func (m *usersModel) updateConfirmDelete(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		target := m.deleteTarget
		return m, func() tea.Msg {
			client := newAPIClient()
			err := client.delete("/api/v1/admin/users/" + target.ID)
			return adminUserDeletedMsg{err: err}
		}
	default:
		m.view = usersViewList
		m.deleteTarget = nil
	}
	return m, nil
}

func (m *usersModel) View() string {
	switch m.view {
	case usersViewCreate:
		return m.viewCreate()
	case usersViewConfirmDelete:
		return m.viewConfirmDelete()
	default:
		return m.viewList()
	}
}

func (m *usersModel) viewList() string {
	if m.loading {
		content := subtitleStyle.Render("Loading users...")
		return "\n" + renderTUIBox("Users", content) + "\n"
	}

	var table string
	header := fmt.Sprintf("  %-20s %-8s %s", "Username", "Role", "Created")
	table += accentStyle.Render(header) + "\n"
	table += subtitleStyle.Render("  "+repeatStr("─", 52)) + "\n"

	if len(m.users) == 0 {
		table += subtitleStyle.Render("  No users found") + "\n"
	}

	for i, u := range m.users {
		created := u.CreatedAt
		if len(created) > 10 {
			created = created[:10]
		}
		line := fmt.Sprintf("%-20s %-8s %s", u.Username, u.Role, created)
		if i == m.cursor {
			table += tuiSelectedRow.Render("► " + line) + "\n"
		} else {
			table += "  " + line + "\n"
		}
	}

	status := ""
	if m.errMsg != "" {
		status = "\n" + errorStyle.Render("  ✗ "+m.errMsg)
	}
	if m.successMsg != "" {
		status = "\n" + successStyle.Render("  ✓ "+m.successMsg)
	}

	footer := subtitleStyle.Render("n new · d delete · esc back")
	content := table + status + "\n\n" + footer

	warn := ""
	if m.ctrlC.IsArmed() {
		warn = "\n" + m.ctrlC.RenderWarning()
	}

	return "\n" + renderTUIBox("Users", content) + warn + "\n"
}

func (m *usersModel) viewCreate() string {
	roleDisplay := m.createRole
	if m.createFocus == 2 {
		roleDisplay = tuiSelectedRow.Render(" " + roleDisplay + " ")
	}

	fields := ""
	fields += "  Username: " + m.createUser.View() + "\n"
	fields += "  Password: " + m.createPass.View() + "\n"
	fields += "  Role:     " + roleDisplay + "\n"

	errLine := ""
	if m.errMsg != "" {
		errLine = "\n" + errorStyle.Render("  ✗ "+m.errMsg)
	}

	footer := subtitleStyle.Render("Tab next field · Enter toggle role · Ctrl+S save · Esc cancel")
	content := fields + errLine + "\n\n" + footer

	return "\n" + renderTUIBox("Create User", content) + "\n"
}

func (m *usersModel) viewConfirmDelete() string {
	msg := fmt.Sprintf("  Delete user %s? (y/N)", accentStyle.Render(m.deleteTarget.Username))
	content := msg + "\n"

	return "\n" + renderTUIBox("Delete User", content) + "\n"
}

func repeatStr(s string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}

// runUsersSection runs the users management TUI.
func runUsersSection(cmd *cobra.Command) error {
	model := newUsersModel()
	p := tea.NewProgram(model)
	_, err := p.Run()
	return err
}
