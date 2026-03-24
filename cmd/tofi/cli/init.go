package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"
	"tofi-core/internal/daemon"
)

var initForce bool

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize the Tofi workspace",
	Long:  "Set up the ~/.tofi directory, configure AI provider, API key, and authentication.",
	RunE:  runInit,
}

func init() {
	initCmd.Flags().BoolVar(&initForce, "force", false, "re-initialize even if workspace exists")
	rootCmd.AddCommand(initCmd)
}

// --- Init TUI Model ---

type initStep int

const (
	stepWelcome     initStep = iota
	stepProvider
	stepAPIKey
	stepAuthMode
	stepUsername
	stepPassword
	stepConfirm
	stepStartEngine // ask whether to start engine
	stepAutoStart   // ask whether to enable auto-start on boot
	stepDone
)

type authMode int

const (
	authToken    authMode = 0
	authPassword authMode = 1
)

type provider struct {
	name    string
	envVar  string
	display string
}

var providers = []provider{
	{name: "anthropic", envVar: "ANTHROPIC_API_KEY", display: "Anthropic (Claude)"},
	{name: "openai", envVar: "OPENAI_API_KEY", display: "OpenAI (GPT)"},
	{name: "gemini", envVar: "GEMINI_API_KEY", display: "Google (Gemini)"},
	{name: "skip", envVar: "", display: "Skip (configure later)"},
}

type initModel struct {
	step     initStep
	cursor   int
	provider provider
	keyInput textinput.Model
	homeDir  string
	err      error
	quitting bool

	// Auth
	authMode       authMode
	authCursor     int
	usernameInput  textinput.Model
	passwordInput  textinput.Model
	generatedToken string

	// Post-init
	engineStarted  bool
	engineStartErr error
	autoStartSet   bool
}

func newInitModel(home string) initModel {
	ti := textinput.New()
	ti.Placeholder = "sk-..."
	ti.CharLimit = 256
	ti.Width = 60
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'

	ui := textinput.New()
	ui.Placeholder = "admin"
	ui.CharLimit = 64
	ui.Width = 40

	pi := textinput.New()
	pi.Placeholder = "password"
	pi.CharLimit = 128
	pi.Width = 40
	pi.EchoMode = textinput.EchoPassword
	pi.EchoCharacter = '•'

	return initModel{
		step:          stepWelcome,
		homeDir:       home,
		keyInput:      ti,
		usernameInput: ui,
		passwordInput: pi,
	}
}

func (m initModel) Init() tea.Cmd {
	return nil
}

func (m initModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "esc":
			m.quitting = true
			return m, tea.Quit
		case "q":
			if m.step != stepAPIKey && m.step != stepUsername && m.step != stepPassword {
				m.quitting = true
				return m, tea.Quit
			}
		}
	}

	switch m.step {
	case stepWelcome:
		return m.updateWelcome(msg)
	case stepProvider:
		return m.updateProvider(msg)
	case stepAPIKey:
		return m.updateAPIKey(msg)
	case stepAuthMode:
		return m.updateAuthMode(msg)
	case stepUsername:
		return m.updateUsername(msg)
	case stepPassword:
		return m.updatePassword(msg)
	case stepConfirm:
		return m.updateConfirm(msg)
	case stepStartEngine:
		return m.updateStartEngine(msg)
	case stepAutoStart:
		return m.updateAutoStart(msg)
	}

	return m, nil
}

func (m initModel) updateWelcome(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.String() == "enter" || keyMsg.String() == " " {
			m.step = stepProvider
		}
	}
	return m, nil
}

func (m initModel) updateProvider(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(providers)-1 {
				m.cursor++
			}
		case "enter":
			m.provider = providers[m.cursor]
			if m.provider.name == "skip" {
				// Skip provider setup, go straight to auth
				m.step = stepAuthMode
				m.authCursor = 0
				return m, nil
			}
			m.step = stepAPIKey
			return m, m.keyInput.Focus()
		}
	}
	return m, nil
}

func (m initModel) updateAPIKey(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "enter":
			key := strings.TrimSpace(m.keyInput.Value())
			if key == "" {
				return m, nil
			}
			m.step = stepAuthMode
			m.authCursor = 0
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.keyInput, cmd = m.keyInput.Update(msg)
	return m, cmd
}

func (m initModel) updateAuthMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "up", "k":
			if m.authCursor > 0 {
				m.authCursor--
			}
		case "down", "j":
			if m.authCursor < 1 {
				m.authCursor++
			}
		case "enter":
			m.authMode = authMode(m.authCursor)
			if m.authMode == authToken {
				// Auto-generate token
				tokenBytes := make([]byte, 32)
				rand.Read(tokenBytes)
				m.generatedToken = hex.EncodeToString(tokenBytes)
				m.step = stepConfirm
			} else {
				m.step = stepUsername
				return m, m.usernameInput.Focus()
			}
		}
	}
	return m, nil
}

func (m initModel) updateUsername(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "enter":
			name := strings.TrimSpace(m.usernameInput.Value())
			if name == "" {
				return m, nil
			}
			m.step = stepPassword
			return m, m.passwordInput.Focus()
		}
	}

	var cmd tea.Cmd
	m.usernameInput, cmd = m.usernameInput.Update(msg)
	return m, cmd
}

func (m initModel) updatePassword(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "enter":
			pw := m.passwordInput.Value()
			if pw == "" {
				return m, nil
			}
			m.step = stepConfirm
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.passwordInput, cmd = m.passwordInput.Update(msg)
	return m, cmd
}

func (m initModel) updateConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter", "y", "Y":
			m.err = m.writeConfig()
			if m.err != nil {
				m.step = stepDone
				return m, tea.Quit
			}
			m.step = stepStartEngine
			return m, nil
		case "n", "N", "esc":
			m.step = stepProvider
			m.cursor = 0
			m.keyInput.SetValue("")
			m.usernameInput.SetValue("")
			m.passwordInput.SetValue("")
			m.generatedToken = ""
			return m, nil
		}
	}
	return m, nil
}

func (m initModel) updateStartEngine(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "y", "Y", "enter":
			pid, err := daemon.Start(m.homeDir, daemon.GetDefaultPort(), false)
			if err != nil {
				m.engineStartErr = err
			} else {
				m.engineStarted = true
				_ = pid
			}
			m.step = stepAutoStart
			return m, nil
		case "n", "N":
			m.step = stepAutoStart
			return m, nil
		}
	}
	return m, nil
}

func (m initModel) updateAutoStart(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "y", "Y", "enter":
			if err := daemon.EnableAutoStart(m.homeDir); err == nil {
				m.autoStartSet = true
			}
			m.step = stepDone
			return m, tea.Quit
		case "n", "N":
			m.step = stepDone
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m initModel) View() string {
	if m.quitting {
		return subtitleStyle.Render("  Cancelled.\n")
	}

	var s strings.Builder

	switch m.step {
	case stepWelcome:
		s.WriteString(titleStyle.Render("  Welcome to Tofi!") + "\n\n")
		s.WriteString(subtitleStyle.Render("  Let's set up your workspace, AI provider, and authentication.") + "\n")
		s.WriteString(subtitleStyle.Render("  Home directory: ") + accentStyle.Render(m.homeDir) + "\n\n")
		s.WriteString(subtitleStyle.Render("  Press ") + accentStyle.Render("Enter") + subtitleStyle.Render(" to continue"))

	case stepProvider:
		s.WriteString(titleStyle.Render("  Choose your AI provider") + "\n\n")
		for i, p := range providers {
			cursor := "  "
			style := subtitleStyle
			if i == m.cursor {
				cursor = accentStyle.Render("❯ ")
				style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc"))
			}
			s.WriteString("  " + cursor + style.Render(p.display) + "\n")
		}
		s.WriteString("\n" + subtitleStyle.Render("  ↑/↓ navigate · Enter select"))

	case stepAPIKey:
		s.WriteString(titleStyle.Render("  Enter your "+m.provider.display+" API Key") + "\n\n")
		s.WriteString("  " + m.keyInput.View() + "\n\n")
		s.WriteString(subtitleStyle.Render("  Your key is stored locally in ") + accentStyle.Render(m.homeDir+"/config.yaml") + "\n")
		s.WriteString(subtitleStyle.Render("  Enter to confirm · Ctrl+C to cancel"))

	case stepAuthMode:
		s.WriteString(titleStyle.Render("  Choose authentication mode") + "\n\n")

		authOptions := []struct {
			name string
			desc string
		}{
			{"Token (recommended)", "Auto-generate a token for local use. No password needed."},
			{"Username & Password", "Create an admin account. Required for web console login."},
		}

		for i, opt := range authOptions {
			cursor := "  "
			nameStyle := subtitleStyle
			if i == m.authCursor {
				cursor = accentStyle.Render("❯ ")
				nameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc"))
			}
			s.WriteString("  " + cursor + nameStyle.Render(opt.name) + "\n")
			s.WriteString("      " + subtitleStyle.Render(opt.desc) + "\n\n")
		}
		s.WriteString(subtitleStyle.Render("  ↑/↓ navigate · Enter select"))

	case stepUsername:
		s.WriteString(titleStyle.Render("  Create admin account") + "\n\n")
		s.WriteString(subtitleStyle.Render("  Username: ") + "\n")
		s.WriteString("  " + m.usernameInput.View() + "\n\n")
		s.WriteString(subtitleStyle.Render("  Enter to continue · Ctrl+C to cancel"))

	case stepPassword:
		s.WriteString(titleStyle.Render("  Create admin account") + "\n\n")
		s.WriteString(subtitleStyle.Render("  Username: ") + accentStyle.Render(m.usernameInput.Value()) + "\n")
		s.WriteString(subtitleStyle.Render("  Password: ") + "\n")
		s.WriteString("  " + m.passwordInput.View() + "\n\n")
		s.WriteString(subtitleStyle.Render("  Enter to confirm · Ctrl+C to cancel"))

	case stepConfirm:
		// Build confirm content
		var lines []string
		if m.provider.name != "skip" {
			masked := maskKey(m.keyInput.Value())
			lines = append(lines,
				subtitleStyle.Render("Provider  ")+accentStyle.Render(m.provider.display),
				subtitleStyle.Render("API Key   ")+accentStyle.Render(masked),
			)
		} else {
			lines = append(lines,
				subtitleStyle.Render("Provider  ")+subtitleStyle.Render("skipped"),
			)
		}

		if m.authMode == authToken {
			tokenMasked := m.generatedToken[:8] + "••••••••" + m.generatedToken[len(m.generatedToken)-8:]
			lines = append(lines,
				subtitleStyle.Render("Auth      ") + accentStyle.Render("Token (auto-generated)"),
				subtitleStyle.Render("Token     ") + accentStyle.Render(tokenMasked),
			)
		} else {
			lines = append(lines,
				subtitleStyle.Render("Auth      ") + accentStyle.Render("Username & Password"),
				subtitleStyle.Render("Username  ") + accentStyle.Render(m.usernameInput.Value()),
				subtitleStyle.Render("Password  ") + accentStyle.Render(strings.Repeat("•", len(m.passwordInput.Value()))),
			)
		}

		lines = append(lines, subtitleStyle.Render("Home      ") + accentStyle.Render(m.homeDir))

		box := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#30363d")).
			Padding(1, 2).
			Width(56)

		s.WriteString(titleStyle.Render("  Confirm setup") + "\n\n")
		s.WriteString(box.Render(strings.Join(lines, "\n")) + "\n\n")
		s.WriteString("  " + accentStyle.Render("Y") + subtitleStyle.Render("/Enter confirm · ") + accentStyle.Render("N") + subtitleStyle.Render(" go back"))

	case stepStartEngine:
		s.WriteString(successStyle.Render("  ✓ Workspace initialized!") + "\n\n")
		if m.authMode == authToken {
			s.WriteString("  " + subtitleStyle.Render("Your access token:") + "\n")
			s.WriteString("  " + accentStyle.Render(m.generatedToken) + "\n\n")
			s.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922")).Render(
				"  Save this token! It won't be shown again.") + "\n\n")
		}
		s.WriteString(titleStyle.Render("  Start engine now?") + "\n\n")
		s.WriteString("  " + accentStyle.Render("Y") + subtitleStyle.Render("/Enter start · ") + accentStyle.Render("N") + subtitleStyle.Render(" skip"))

	case stepAutoStart:
		if m.engineStarted {
			s.WriteString(successStyle.Render("  ✓ Engine started") + "\n\n")
		} else if m.engineStartErr != nil {
			s.WriteString(errorStyle.Render("  ✗ Engine start failed: "+m.engineStartErr.Error()) + "\n\n")
		}
		s.WriteString(titleStyle.Render("  Enable auto-start on boot?") + "\n\n")
		s.WriteString("  " + accentStyle.Render("Y") + subtitleStyle.Render("/Enter enable · ") + accentStyle.Render("N") + subtitleStyle.Render(" skip"))

	case stepDone:
		if m.err != nil {
			s.WriteString(errorStyle.Render("  ✗ Setup failed: "+m.err.Error()) + "\n")
		} else {
			s.WriteString(successStyle.Render("  ✓ Setup complete!") + "\n\n")

			if m.engineStarted {
				s.WriteString("  " + successStyle.Render("Engine: running") + "\n")
			} else {
				s.WriteString("  " + subtitleStyle.Render("Engine: not started") + "\n")
				s.WriteString("  " + subtitleStyle.Render("  Run ") + accentStyle.Render("tofi start") + subtitleStyle.Render(" to launch") + "\n")
			}

			if m.autoStartSet {
				s.WriteString("  " + successStyle.Render("Auto-start: enabled") + "\n")
			} else {
				s.WriteString("  " + subtitleStyle.Render("Auto-start: disabled") + "\n")
			}
		}
	}

	return "\n" + renderTUIBox("Init", s.String()) + "\n"
}

func maskKey(key string) string {
	if len(key) <= 10 {
		return strings.Repeat("•", len(key))
	}
	return key[:6] + "••••••••" + key[len(key)-4:]
}

// writeConfig creates the workspace directories and writes config.yaml.
func (m initModel) writeConfig() error {
	dirs := []string{
		m.homeDir,
		filepath.Join(m.homeDir, "users"),
		filepath.Join(m.homeDir, "skills"),
		filepath.Join(m.homeDir, "logs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d, err)
		}
	}

	configPath := filepath.Join(m.homeDir, "config.yaml")

	// Generate a random JWT secret for CLI <-> daemon auth
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return fmt.Errorf("failed to generate JWT secret: %w", err)
	}
	jwtSecret := hex.EncodeToString(secretBytes)

	// Build auth section
	var authSection string
	if m.authMode == authToken {
		authSection = fmt.Sprintf(`# Authentication: token mode (no password required)
auth_mode: token
access_token: %s
`, m.generatedToken)
	} else {
		// Hash the password
		hash, err := bcrypt.GenerateFromPassword([]byte(m.passwordInput.Value()), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("failed to hash password: %w", err)
		}
		authSection = fmt.Sprintf(`# Authentication: username & password mode
auth_mode: password
admin_username: %s
admin_password_hash: %s
`, m.usernameInput.Value(), string(hash))
	}

	// Build provider section
	var providerSection string
	if m.provider.name != "skip" {
		providerSection = fmt.Sprintf(`# AI Provider
provider: %s
api_key: %s
`, m.provider.name, m.keyInput.Value())
	} else {
		providerSection = `# AI Provider (not configured — run tofi init --force to set up)
# provider:
# api_key:
`
	}

	content := fmt.Sprintf(`# Tofi Engine Configuration
# Generated by: tofi init

port: 8321

%s
%s
# Auto-generated secret for internal JWT signing (do not share)
jwt_secret: %s
`, providerSection, authSection, jwtSecret)

	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

func runInit(cmd *cobra.Command, args []string) error {
	// Check if already initialized
	configPath := filepath.Join(homeDir, "config.yaml")
	if _, err := os.Stat(configPath); err == nil && !initForce {
		fmt.Println()
		fmt.Println(subtitleStyle.Render("  Workspace already initialized at ") + accentStyle.Render(homeDir))
		fmt.Println(subtitleStyle.Render("  Run ") + accentStyle.Render("tofi init --force") + subtitleStyle.Render(" to re-initialize"))
		fmt.Println()
		return nil
	}

	// Force reinit: check if engine is running and ask to stop
	if initForce {
		status := daemon.GetStatus(homeDir, daemon.GetDefaultPort())
		if status.Running {
			fmt.Println()
			fmt.Println(lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922")).Render(
				"  Detected running engine (PID "+fmt.Sprintf("%d", status.PID)+")"))
			fmt.Println()
			fmt.Print(subtitleStyle.Render("  Stop engine and continue re-init? ") + accentStyle.Render("(Y/n) "))
			var answer string
			fmt.Scanln(&answer)
			if answer != "" && answer != "y" && answer != "Y" {
				fmt.Println(subtitleStyle.Render("  Cancelled."))
				return nil
			}

			fmt.Print(subtitleStyle.Render("  This will reset workspace data. Are you sure? ") + accentStyle.Render("(Y/n) "))
			fmt.Scanln(&answer)
			if answer != "" && answer != "y" && answer != "Y" {
				fmt.Println(subtitleStyle.Render("  Cancelled."))
				return nil
			}

			// Disable auto-start first
			daemon.DisableAutoStart(homeDir)

			// Stop the engine
			if err := daemon.Stop(homeDir, false); err != nil {
				fmt.Println(errorStyle.Render("  ✗ Failed to stop engine: " + err.Error()))
				return err
			}
			fmt.Println(successStyle.Render("  ✓ Engine stopped"))
			fmt.Println()
		}
	}

	p := tea.NewProgram(newInitModel(homeDir))
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	m := finalModel.(initModel)
	if m.quitting {
		return nil
	}

	return m.err
}
