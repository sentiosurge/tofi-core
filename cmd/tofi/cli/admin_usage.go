package cli

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

type modelUsage struct {
	Model        string  `json:"model"`
	Sessions     int     `json:"sessions"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalCost    float64 `json:"total_cost"`
}

type usageModel struct {
	data    []modelUsage
	users   []userRecord
	loading bool
	errMsg  string
	year    int
	month   int
	userIdx int // 0 = all users, 1+ = specific user
	ctrlC   ctrlCGuard
}

type usageLoadedMsg struct {
	data  []modelUsage
	users []userRecord
	err   error
}

func newUsageModel() *usageModel {
	now := time.Now()
	return &usageModel{
		loading: true,
		year:    now.Year(),
		month:   int(now.Month()),
	}
}

func (m *usageModel) Init() tea.Cmd {
	return m.loadData
}

func (m *usageModel) loadData() tea.Msg {
	client := newAPIClient()

	monthStr := fmt.Sprintf("%04d-%02d", m.year, m.month)
	path := "/api/v1/admin/usage?month=" + monthStr
	if m.userIdx > 0 && m.userIdx <= len(m.users) {
		path += "&user_id=" + m.users[m.userIdx-1].ID
	}

	var data []modelUsage
	err := client.get(path, &data)
	if err != nil {
		return usageLoadedMsg{err: err}
	}

	// Load users list (only on first load)
	var users []userRecord
	if len(m.users) == 0 {
		_ = client.get("/api/v1/admin/users", &users)
	} else {
		users = m.users
	}

	return usageLoadedMsg{data: data, users: users}
}

func (m *usageModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case usageLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.errMsg = msg.err.Error()
		} else {
			m.data = msg.data
			if msg.users != nil {
				m.users = msg.users
			}
		}
		return m, nil

	case tea.KeyMsg:
		m.errMsg = ""
		switch msg.String() {
		case "ctrl+c":
			if quit, cmd := m.ctrlC.HandleCtrlC(); quit {
				return m, tea.Quit
			} else {
				return m, cmd
			}
		case "esc", "q":
			return m, tea.Quit
		case "left", "h":
			m.month--
			if m.month < 1 {
				m.month = 12
				m.year--
			}
			m.loading = true
			return m, m.loadData
		case "right", "l":
			m.month++
			if m.month > 12 {
				m.month = 1
				m.year++
			}
			m.loading = true
			return m, m.loadData
		case "f":
			totalFilters := len(m.users) + 1
			m.userIdx = (m.userIdx + 1) % totalFilters
			m.loading = true
			return m, m.loadData
		default:
			m.ctrlC.HandleReset()
		}

	case ctrlCResetMsg:
		m.ctrlC.HandleReset()
		return m, nil
	}
	return m, nil
}

func (m *usageModel) View() string {
	monthName := time.Month(m.month).String()
	title := fmt.Sprintf("Usage — %s %d", monthName, m.year)

	if m.loading {
		content := subtitleStyle.Render("Loading...")
		return "\n" + renderTUIBox(title, content) + "\n"
	}

	// User filter display
	userFilter := "All Users"
	if m.userIdx > 0 && m.userIdx <= len(m.users) {
		userFilter = m.users[m.userIdx-1].Username
	}
	filterLine := fmt.Sprintf("  User: %s", accentStyle.Render(userFilter))

	// Table header
	header := fmt.Sprintf("  %-25s %8s %12s %12s %8s", "Model", "Sessions", "In Tokens", "Out Tokens", "Cost")
	sep := "  " + repeatStr("─", 67)

	var rows string
	var totalSessions int
	var totalIn, totalOut int64
	var totalCost float64

	if len(m.data) == 0 {
		rows = subtitleStyle.Render("  No usage data for this period") + "\n"
	} else {
		for _, d := range m.data {
			model := d.Model
			if len(model) > 25 {
				model = model[:22] + "..."
			}
			rows += fmt.Sprintf("  %-25s %8d %12s %12s %8s\n",
				model, d.Sessions,
				formatNumber(d.InputTokens),
				formatNumber(d.OutputTokens),
				formatCost(d.TotalCost))
			totalSessions += d.Sessions
			totalIn += d.InputTokens
			totalOut += d.OutputTokens
			totalCost += d.TotalCost
		}
		rows += subtitleStyle.Render(sep) + "\n"
		rows += accentStyle.Render(fmt.Sprintf("  %-25s %8d %12s %12s %8s",
			"Total", totalSessions,
			formatNumber(totalIn),
			formatNumber(totalOut),
			formatCost(totalCost))) + "\n"
	}

	errLine := ""
	if m.errMsg != "" {
		errLine = errorStyle.Render("  ✗ "+m.errMsg) + "\n"
	}

	nav := subtitleStyle.Render("  [← prev month] [→ next month]")
	footer := subtitleStyle.Render("←→ month · f filter user · esc back")

	content := filterLine + "\n" + nav + "\n\n" +
		accentStyle.Render(header) + "\n" +
		subtitleStyle.Render(sep) + "\n" +
		rows + errLine + "\n" + footer

	warn := ""
	if m.ctrlC.IsArmed() {
		warn = "\n" + m.ctrlC.RenderWarning()
	}

	return "\n" + renderTUIBox(title, content) + warn + "\n"
}

func formatNumber(n int64) string {
	if n == 0 {
		return "0"
	}
	s := fmt.Sprintf("%d", n)
	// Add comma separators
	result := ""
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result += ","
		}
		result += string(c)
	}
	return result
}

// runUsageSection runs the usage statistics TUI.
func runUsageSection(cmd *cobra.Command) error {
	model := newUsageModel()
	p := tea.NewProgram(model)
	_, err := p.Run()
	return err
}
