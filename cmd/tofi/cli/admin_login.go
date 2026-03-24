package cli

import (
	"bytes"
	"encoding/json"
	"fmt"

	"tofi-core/internal/daemon"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type loginField int

const (
	loginFieldUser loginField = iota
	loginFieldPass
)

type loginModel struct {
	username textinput.Model
	password textinput.Model
	focus    loginField
	errMsg   string
	done     bool
	token    string
	role     string
}

type loginResultMsg struct {
	token    string
	username string
	role     string
	err      error
}

func newLoginModel() *loginModel {
	u := textinput.New()
	u.Placeholder = "username"
	u.Focus()
	u.CharLimit = 64

	p := textinput.New()
	p.Placeholder = "password"
	p.EchoMode = textinput.EchoPassword
	p.CharLimit = 128

	return &loginModel{username: u, password: p}
}

func (m *loginModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m *loginModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		m.errMsg = ""
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "tab", "shift+tab":
			if m.focus == loginFieldUser {
				m.focus = loginFieldPass
				m.username.Blur()
				m.password.Focus()
			} else {
				m.focus = loginFieldUser
				m.password.Blur()
				m.username.Focus()
			}
			return m, nil
		case "enter":
			if m.focus == loginFieldUser {
				m.focus = loginFieldPass
				m.username.Blur()
				m.password.Focus()
				return m, nil
			}
			// Submit
			user := m.username.Value()
			pass := m.password.Value()
			if user == "" || pass == "" {
				m.errMsg = "Username and password are required"
				return m, nil
			}
			return m, m.doLogin(user, pass)
		}
	case loginResultMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.password.SetValue("")
			return m, nil
		}
		m.done = true
		m.token = msg.token
		m.role = msg.role
		return m, tea.Quit
	}

	var cmd tea.Cmd
	if m.focus == loginFieldUser {
		m.username, cmd = m.username.Update(msg)
	} else {
		m.password, cmd = m.password.Update(msg)
	}
	return m, cmd
}

func (m *loginModel) doLogin(user, pass string) tea.Cmd {
	return func() tea.Msg {
		client := &apiClient{
			baseURL: fmt.Sprintf("http://localhost:%d", startPort),
			http:    newAPIClient().http,
		}
		body, _ := json.Marshal(map[string]string{
			"username": user,
			"password": pass,
		})
		data, status, err := client.postRaw("/api/v1/auth/login", bytes.NewReader(body))
		if err != nil {
			return loginResultMsg{err: fmt.Errorf("Cannot connect to engine")}
		}
		if status != 200 {
			return loginResultMsg{err: fmt.Errorf("Invalid username or password")}
		}
		var resp map[string]string
		if err := json.Unmarshal(data, &resp); err != nil {
			return loginResultMsg{err: fmt.Errorf("Invalid response from server")}
		}
		return loginResultMsg{
			token:    resp["token"],
			username: resp["username"],
			role:     resp["role"],
		}
	}
}

func (m *loginModel) View() string {
	header := logoText + "  " + subtitleStyle.Render("Login")

	fields := ""
	fields += "  Username: " + m.username.View() + "\n"
	fields += "  Password: " + m.password.View() + "\n"

	errLine := ""
	if m.errMsg != "" {
		errLine = "\n" + errorStyle.Render("  ✗ "+m.errMsg)
	}

	footer := subtitleStyle.Render("Tab switch field · Enter submit · Ctrl+C quit")

	content := header + "\n\n" + fields + errLine + "\n\n" + footer
	return "\n" + tuiBoxStyle.Render(content) + "\n"
}

// runAdminLogin prompts for admin credentials if needed.
// Returns true if authenticated (or login was skipped), false if user quit.
func runAdminLogin() bool {
	// Check if daemon is running
	if !daemon.CheckHealth(startPort) {
		fmt.Println(errorStyle.Render("\n  ✗ Engine is not running — start it with: tofi start\n"))
		return false
	}

	// Check auth mode — token mode skips login
	client := newAPIClient()
	if client.token != "" {
		// Already have a token (token mode or jwt_secret present) — skip login
		return true
	}

	// Password mode with no jwt_secret — need interactive login
	for {
		model := newLoginModel()
		p := tea.NewProgram(model)
		if _, err := p.Run(); err != nil {
			return false
		}

		if model.done {
			sessionToken = model.token
			return true
		}

		// User pressed Ctrl+C
		return false
	}
}
