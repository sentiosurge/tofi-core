package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	chatSessionID string
	chatAgentName string
)

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Interactive AI chat",
	Long:  "Start an interactive chat session with the Tofi engine. Supports /model, /skills, /history commands.",
	RunE:  runChat,
}

func init() {
	chatCmd.Flags().StringVar(&chatSessionID, "session", "", "resume a specific session by ID")
	chatCmd.Flags().StringVar(&chatAgentName, "agent", "", "chat with a specific agent")
	rootCmd.AddCommand(chatCmd)
}

// --- SSE data types ---

type sseChunk struct{ Delta string `json:"delta"` }
type sseToolCall struct {
	Tool       string `json:"tool"`
	DurationMs int    `json:"duration_ms"`
}
type sseDone struct {
	Result              string  `json:"result"`
	Model               string  `json:"model"`
	TotalInputTokens    int     `json:"total_input_tokens"`
	TotalOutputTokens   int     `json:"total_output_tokens"`
	TotalCost           float64 `json:"total_cost"`
	ContextUsagePercent int     `json:"context_usage_percent"`
}
type sseError struct{ Error string `json:"error"` }

// --- API response types ---

type sessionInfo struct {
	ID      string   `json:"id"`
	Scope   string   `json:"scope"`
	Model   string   `json:"model"`
	Skills  []string `json:"skills"`
	Title   string   `json:"title"`
	Created string   `json:"created"`
}

type sessionIndex struct {
	ID           string  `json:"ID"`
	Scope        string  `json:"Scope"`
	Title        string  `json:"Title"`
	Model        string  `json:"Model"`
	MessageCount int     `json:"MessageCount"`
	TotalCost    float64 `json:"TotalCost"`
	UpdatedAt    string  `json:"UpdatedAt"`
}

type sessionMessage struct {
	Role    string `json:"Role"`
	Content string `json:"Content"`
}

// --- Bubble Tea messages ---

type streamChunkMsg struct{ delta string }
type streamToolMsg struct {
	tool       string
	durationMs int
}
type streamDoneMsg struct {
	result       string
	model        string
	inputTokens  int
	outputTokens int
	cost         float64
	contextPct   int
}
type streamErrorMsg struct{ err string }
type streamCompactMsg struct{}

// --- Slash command definitions ---

type slashCmd struct {
	cmd  string
	desc string
}

var slashCommands = []slashCmd{
	{"/help", "Show available commands"},
	{"/model", "Switch or view model"},
	{"/skills", "Manage skills"},
	{"/new", "Start new session"},
	{"/history", "List past sessions"},
	{"/switch", "Switch to session"},
}

// --- Border color ---

var borderColor = lipgloss.Color("#30363d")

func bdr(s string) string {
	return lipgloss.NewStyle().Foreground(borderColor).Render(s)
}

// --- Full-screen Chat Model ---

type chatModel struct {
	viewport  viewport.Model
	textarea  textarea.Model
	content   strings.Builder // finalized rendered content
	streamBuf strings.Builder // current streaming text

	program *tea.Program

	streaming   bool
	client      *apiClient
	sessionID   string
	scope       string
	model       string
	skills      []string
	contextPct        int
	width             int
	height            int
	totalInputTokens  int
	totalOutputTokens int
	totalCost         float64
	ready             bool

	// Interactive selection mode
	selectMode     bool
	selectTitle    string
	selectItems    []selectItem
	selectCursor   int
	selectSelected int // index of selected item (-1 = none)
	selectAction   func(item selectItem)

	// Deferred init rendering (ensureSession runs before TUI has width)
	initPending  bool
	initLines    []string           // raw info lines (not width-dependent)
	initMessages []sessionMessage   // raw messages to render after WindowSizeMsg

	// Slash command completion
	completionMode   bool
	completionCmds   []slashCmd
	completionCursor int
}

type selectItem struct {
	id    string
	label string
	meta  string
}

func newChatModel(client *apiClient) *chatModel {
	ta := textarea.New()
	ta.Placeholder = "Type a message... (Enter to send, /help for commands)"
	ta.Focus()
	ta.CharLimit = 4096
	ta.SetHeight(2)
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetEnabled(false)
	// Remove default left border — the outer frame handles borders
	ta.Prompt = " "
	ta.FocusedStyle.Prompt = lipgloss.NewStyle()
	ta.BlurredStyle.Prompt = lipgloss.NewStyle()
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()

	scope := ""
	if chatAgentName != "" {
		scope = "agent:" + chatAgentName
	}

	return &chatModel{
		textarea: ta,
		client:   client,
		scope:    scope,
	}
}

// headerText returns the header title based on scope.
func (m *chatModel) headerText() string {
	if agentName, ok := strings.CutPrefix(m.scope, "agent:"); ok {
		return "TOFI Chat — " + agentName
	}
	return "TOFI Chat"
}

// innerWidth returns the content width inside the border frame.
// Layout: │ <content> │ → border(1) + pad(1) + content(iw) + pad(1) + border(1) = width
func (m *chatModel) innerWidth() int {
	return max(20, m.width-4)
}

// Fixed lines in the frame (everything except viewport).
// top_border(1) + separator(1) + status(1) + separator(1) + textarea_lines + bottom_border(1)
func (m *chatModel) fixedLines() int {
	return 5 + m.textarea.Height()
}

func (m *chatModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m *chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		iw := m.innerWidth()
		m.textarea.SetWidth(iw)

		vpHeight := msg.Height - m.fixedLines()
		if vpHeight < 3 {
			vpHeight = 3
		}

		if !m.ready {
			m.viewport = viewport.New(iw, vpHeight)
			m.ready = true
			m.displaySessionInit()
			m.viewport.SetContent(m.viewportContent())
			m.viewport.GotoBottom()
		} else {
			m.viewport.Width = iw
			m.viewport.Height = vpHeight
			m.viewport.SetContent(m.viewportContent())
		}
		return m, nil

	case tea.KeyMsg:
		// Interactive selection mode intercepts all keys
		if m.selectMode {
			return m.updateSelectMode(msg)
		}

		// Slash command completion mode
		if m.completionMode {
			return m.updateCompletionMode(msg)
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if !m.streaming {
				return m, tea.Quit
			}
			return m, nil
		case "enter":
			if m.streaming {
				return m, nil
			}
			input := strings.TrimSpace(m.textarea.Value())
			if input == "" {
				return m, nil
			}
			m.textarea.Reset()

			if strings.HasPrefix(input, "/") {
				m.handleSlashCommand(input)
				return m, nil
			}

			m.appendContent(m.renderUserMsg(input))
			m.appendContent("")

			m.streaming = true
			m.refreshViewport()

			go m.streamChatMessage(input)
			return m, nil

		case "pgup", "pgdown", "home", "end":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}

		if !m.streaming {
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(msg)
			m.checkCompletionTrigger()
			return m, cmd
		}
		return m, nil

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case streamChunkMsg:
		m.streamBuf.WriteString(msg.delta)
		m.refreshViewport()
		return m, nil

	case streamToolMsg:
		m.finalizeStreamBlock()
		m.appendContent(m.renderToolCall(msg.tool, msg.durationMs))
		return m, nil

	case streamCompactMsg:
		m.appendContent(lipgloss.NewStyle().
			Foreground(lipgloss.Color("#d29922")).
			Italic(true).
			PaddingLeft(1).
			Render("⟳ Context compacted"))
		return m, nil

	case streamDoneMsg:
		m.finalizeStreamBlock()
		m.streaming = false
		m.totalInputTokens += msg.inputTokens
		m.totalOutputTokens += msg.outputTokens
		m.totalCost += msg.cost
		m.contextPct = msg.contextPct
		if msg.model != "" && msg.model != "unknown" {
			m.model = msg.model
		}

		totalTok := msg.inputTokens + msg.outputTokens
		m.appendContent(subtitleStyle.Render(fmt.Sprintf(" %s · %d tokens · $%.4f",
			msg.model, totalTok, msg.cost)))
		m.appendContent("") // single blank line after turn
		return m, nil

	case streamErrorMsg:
		m.finalizeStreamBlock()
		m.streaming = false
		m.appendContent(m.renderError(msg.err))
		m.appendContent("")
		return m, nil
	}

	if !m.streaming {
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return m, cmd
	}
	return m, nil
}

// --- View: border-framed layout ---
//
// ╭─ TOFI Chat ──────────────────────────╮
// │ viewport (messages area, scrollable)  │
// ├───────────────────────────────────────┤
// │ /help · Esc quit          [badges]    │
// ├───────────────────────────────────────┤
// │ > input                               │
// ╰───────────────────────────────────────╯

func (m *chatModel) View() string {
	if !m.ready {
		return "\n  Loading..."
	}

	iw := m.innerWidth()
	var out strings.Builder

	// Top border with title
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff7b72")).Render(" " + m.headerText() + " ")
	titleW := lipgloss.Width(title)
	fillW := max(0, iw-titleW)
	out.WriteString(bdr("╭─") + title + bdr(strings.Repeat("─", fillW)+"─╮") + "\n")

	if m.selectMode {
		// Render selection overlay in viewport area
		selLines := m.renderSelectList(iw, m.viewport.Height)
		for _, line := range selLines {
			out.WriteString(m.padBorderLine(line, iw) + "\n")
		}
	} else {
		// Viewport content with side borders
		vpLines := strings.Split(m.viewport.View(), "\n")

		// Overlay completion list at bottom of viewport
		if m.completionMode && len(m.completionCmds) > 0 {
			compLines := m.renderCompletionList(iw)
			overlayStart := len(vpLines) - len(compLines)
			if overlayStart < 0 {
				overlayStart = 0
			}
			for i, cl := range compLines {
				idx := overlayStart + i
				if idx < len(vpLines) {
					vpLines[idx] = cl
				}
			}
		}

		for _, line := range vpLines {
			out.WriteString(m.padBorderLine(line, iw) + "\n")
		}
	}

	// Separator
	out.WriteString(bdr("├─" + strings.Repeat("─", iw) + "─┤") + "\n")

	// Status bar
	out.WriteString(m.padBorderLine(m.renderStatusBar(iw), iw) + "\n")

	// Separator
	out.WriteString(bdr("├─" + strings.Repeat("─", iw) + "─┤") + "\n")

	// Textarea / select hint
	if m.selectMode {
		hint := subtitleStyle.Render("↑↓ Navigate") + "  " +
			accentStyle.Render("Space") + subtitleStyle.Render(" Select") + "  " +
			accentStyle.Render("Enter") + subtitleStyle.Render(" Confirm") + "  " +
			accentStyle.Render("Esc") + subtitleStyle.Render(" Cancel")
		out.WriteString(m.padBorderLine(" "+hint, iw) + "\n")
		// Fill remaining textarea lines
		for i := 1; i < m.textarea.Height(); i++ {
			out.WriteString(m.padBorderLine("", iw) + "\n")
		}
	} else {
		taLines := strings.Split(m.textarea.View(), "\n")
		for _, line := range taLines {
			out.WriteString(m.padBorderLine(line, iw) + "\n")
		}
	}

	// Bottom border
	out.WriteString(bdr("╰─" + strings.Repeat("─", iw) + "─╯"))

	return out.String()
}

// padBorderLine wraps a content line with │ borders and right-pads to fill width.
func (m *chatModel) padBorderLine(content string, iw int) string {
	cw := lipgloss.Width(content)
	gap := max(0, iw-cw)
	return bdr("│") + " " + content + strings.Repeat(" ", gap) + " " + bdr("│")
}

func (m *chatModel) renderStatusBar(iw int) string {
	left := subtitleStyle.Render("/help · Esc quit")

	var badges []string

	// Context usage
	if m.contextPct > 0 {
		ctxColor := "#3fb950"
		if m.contextPct > 80 {
			ctxColor = "#f85149"
		} else if m.contextPct > 60 {
			ctxColor = "#d29922"
		}
		badges = append(badges, lipgloss.NewStyle().
			Background(lipgloss.Color(ctxColor)).
			Foreground(lipgloss.Color("#ffffff")).
			Padding(0, 1).
			Render(fmt.Sprintf("ctx:%d%%", m.contextPct)))
	}

	// Total tokens + cost (persistent)
	totalTok := m.totalInputTokens + m.totalOutputTokens
	if totalTok > 0 {
		inText := formatTokens(m.totalInputTokens)
		outText := formatTokens(m.totalOutputTokens)
		badges = append(badges, lipgloss.NewStyle().
			Background(lipgloss.Color("#30363d")).
			Foreground(lipgloss.Color("#8b949e")).
			Padding(0, 1).
			Render(fmt.Sprintf("↑%s ↓%s · $%.4f", inText, outText, m.totalCost)))
	}

	// Skills
	if len(m.skills) > 0 {
		badges = append(badges, lipgloss.NewStyle().
			Background(lipgloss.Color("#238636")).
			Foreground(lipgloss.Color("#ffffff")).
			Padding(0, 1).
			Render(strings.Join(m.skills, ",")))
	}

	// Model
	if m.model != "" {
		badges = append(badges, lipgloss.NewStyle().
			Background(lipgloss.Color("#1f6feb")).
			Foreground(lipgloss.Color("#ffffff")).
			Padding(0, 1).
			Render(m.model))
	}

	rightStr := strings.Join(badges, " ")
	gap := max(0, iw-lipgloss.Width(left)-lipgloss.Width(rightStr))
	return left + strings.Repeat(" ", gap) + rightStr
}

// --- Interactive selection mode ---

func (m *chatModel) enterSelectMode(title string, items []selectItem, action func(selectItem)) {
	m.selectMode = true
	m.selectTitle = title
	m.selectItems = items
	m.selectCursor = 0
	m.selectSelected = -1
	m.selectAction = action
}

func (m *chatModel) exitSelectMode() {
	m.selectMode = false
	m.selectItems = nil
	m.selectAction = nil
	m.selectSelected = -1
}

func (m *chatModel) updateSelectMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.exitSelectMode()
		return m, nil
	case "up", "k":
		if m.selectCursor > 0 {
			m.selectCursor--
		}
		return m, nil
	case "down", "j":
		if m.selectCursor < len(m.selectItems)-1 {
			m.selectCursor++
		}
		return m, nil
	case " ": // Space = select/toggle current item
		if len(m.selectItems) > 0 {
			if m.selectSelected == m.selectCursor {
				m.selectSelected = -1 // deselect
			} else {
				m.selectSelected = m.selectCursor
			}
		}
		return m, nil
	case "enter": // Enter = confirm selected item
		if m.selectSelected >= 0 && m.selectSelected < len(m.selectItems) {
			item := m.selectItems[m.selectSelected]
			action := m.selectAction
			m.exitSelectMode()
			if action != nil {
				action(item)
			}
		}
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m *chatModel) renderSelectList(iw int, maxLines int) []string {
	lines := make([]string, 0, maxLines)

	// Title
	titleLine := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#d2a8ff")).PaddingLeft(1).Render(m.selectTitle)
	lines = append(lines, "", titleLine, "")

	// Visible items
	visibleSlots := maxLines - 4 // title(1) + blank(2) + hint(1)
	if visibleSlots < 1 {
		visibleSlots = 1
	}

	// Scroll window around cursor
	startIdx := 0
	if m.selectCursor >= visibleSlots {
		startIdx = m.selectCursor - visibleSlots + 1
	}
	endIdx := startIdx + visibleSlots
	if endIdx > len(m.selectItems) {
		endIdx = len(m.selectItems)
	}

	for i := startIdx; i < endIdx; i++ {
		item := m.selectItems[i]
		var line string

		isSelected := i == m.selectSelected
		isCursor := i == m.selectCursor

		if isCursor {
			// Cursor row — highlighted background, full width
			indicator := " ► "
			if isSelected {
				indicator = " ✓ "
			}
			plain := indicator + item.label
			if item.meta != "" {
				plain += "  " + item.meta
			}
			plainW := lipgloss.Width(plain)
			pad := max(0, iw-plainW)
			line = lipgloss.NewStyle().
				Background(lipgloss.Color("#1f6feb")).
				Foreground(lipgloss.Color("#ffffff")).
				Bold(true).
				Render(plain + strings.Repeat(" ", pad))
		} else {
			indicator := subtitleStyle.Render(" ○ ")
			if isSelected {
				indicator = successStyle.Render(" ✓ ")
			}
			label := accentStyle.Render(item.label)
			meta := ""
			if item.meta != "" {
				meta = subtitleStyle.Render("  " + item.meta)
			}
			line = indicator + label + meta
		}

		lines = append(lines, line)
	}

	// Pad remaining lines
	for len(lines) < maxLines {
		lines = append(lines, "")
	}

	return lines
}

// --- Slash command completion ---

func (m *chatModel) checkCompletionTrigger() {
	val := m.textarea.Value()
	if strings.HasPrefix(val, "/") {
		m.completionMode = true
		m.updateCompletionFilter()
	}
}

func (m *chatModel) updateCompletionFilter() {
	val := strings.ToLower(strings.TrimSpace(m.textarea.Value()))
	if !strings.HasPrefix(val, "/") {
		m.completionMode = false
		return
	}

	var filtered []slashCmd
	for _, cmd := range slashCommands {
		if strings.HasPrefix(cmd.cmd, val) {
			filtered = append(filtered, cmd)
		}
	}
	m.completionCmds = filtered
	if m.completionCursor >= len(filtered) {
		m.completionCursor = max(0, len(filtered)-1)
	}
	if len(filtered) == 0 {
		m.completionMode = false
	}
}

func (m *chatModel) updateCompletionMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up":
		if m.completionCursor > 0 {
			m.completionCursor--
		}
		return m, nil
	case "down":
		if m.completionCursor < len(m.completionCmds)-1 {
			m.completionCursor++
		}
		return m, nil
	case "enter", "tab":
		if len(m.completionCmds) > 0 && m.completionCursor < len(m.completionCmds) {
			selected := m.completionCmds[m.completionCursor]
			m.textarea.SetValue(selected.cmd)
		}
		m.completionMode = false
		return m, nil
	case "esc":
		m.completionMode = false
		m.textarea.Reset()
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	default:
		// Pass to textarea, then re-filter
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		m.updateCompletionFilter()
		return m, cmd
	}
}

func (m *chatModel) renderCompletionList(iw int) []string {
	lines := make([]string, 0, len(m.completionCmds)+1)
	lines = append(lines, "") // blank separator above

	for i, cmd := range m.completionCmds {
		if i == m.completionCursor {
			// Highlighted row — full-width blue background
			plain := fmt.Sprintf(" ► %-12s %s", cmd.cmd, cmd.desc)
			pw := lipgloss.Width(plain)
			pad := max(0, iw-pw)
			line := lipgloss.NewStyle().
				Background(lipgloss.Color("#1f6feb")).
				Foreground(lipgloss.Color("#ffffff")).
				Bold(true).
				Render(plain + strings.Repeat(" ", pad))
			lines = append(lines, line)
		} else {
			name := accentStyle.Render(fmt.Sprintf("%-12s", cmd.cmd))
			desc := subtitleStyle.Render(cmd.desc)
			lines = append(lines, "   "+name+" "+desc)
		}
	}

	return lines
}

// --- Content management ---

func (m *chatModel) viewportContent() string {
	base := m.content.String()
	if m.streaming {
		base += m.renderStreamingBlock()
	}
	return base
}

func (m *chatModel) renderStreamingBlock() string {
	iw := m.innerWidth()
	if m.streamBuf.Len() == 0 {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8b949e")).
			Italic(true).
			PaddingLeft(1).
			Render("thinking...") + "\n"
	}
	label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#3fb950")).PaddingLeft(1).Render("Tofi")
	text := lipgloss.NewStyle().Width(iw).PaddingLeft(1).Render(m.streamBuf.String())
	return label + "\n" + text + "\n"
}

func (m *chatModel) finalizeStreamBlock() {
	if m.streamBuf.Len() == 0 {
		return
	}
	iw := m.innerWidth()
	label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#3fb950")).PaddingLeft(1).Render("Tofi")
	text := lipgloss.NewStyle().Width(iw).PaddingLeft(1).Render(m.streamBuf.String())
	m.content.WriteString(label + "\n" + text + "\n")
	m.streamBuf.Reset()
}

func (m *chatModel) appendContent(line string) {
	m.content.WriteString(line + "\n")
	m.refreshViewport()
}

func (m *chatModel) refreshViewport() {
	if !m.ready {
		return
	}
	m.viewport.SetContent(m.viewportContent())
	m.viewport.GotoBottom()
}

// --- Rendering helpers ---

func (m *chatModel) renderUserMsg(content string) string {
	iw := m.innerWidth()
	label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#58a6ff")).PaddingLeft(1).Render("You")
	text := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc")).Width(iw).PaddingLeft(1).Render(content)
	return label + "\n" + text
}

func (m *chatModel) renderAssistantMsg(content string) string {
	iw := m.innerWidth()
	label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#3fb950")).PaddingLeft(1).Render("Tofi")
	text := lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e")).Width(iw).PaddingLeft(1).Render(content)
	return label + "\n" + text
}

func (m *chatModel) renderToolCall(tool string, durationMs int) string {
	icon := lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922")).Render("⚡")
	name := lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922")).Bold(true).Render(tool)
	dur := ""
	if durationMs > 0 {
		dur = subtitleStyle.Render(fmt.Sprintf(" (%dms)", durationMs))
	}
	return " " + icon + " " + name + dur
}

func (m *chatModel) renderError(errMsg string) string {
	return " " + errorStyle.Render("✗ "+errMsg)
}

func (m *chatModel) renderSuccess(msg string) string {
	return " " + successStyle.Render("✓") + " " + msg
}

func (m *chatModel) renderInfo(msg string) string {
	return " " + subtitleStyle.Render(msg)
}

// --- Session management ---

func (m *chatModel) ensureSession() error {
	m.initPending = true
	m.initLines = nil
	m.initMessages = nil

	if chatSessionID != "" {
		m.sessionID = chatSessionID
		if m.scope == "" {
			var sessions []sessionIndex
			if err := m.client.get("/api/v1/chat/sessions", &sessions); err == nil {
				for _, s := range sessions {
					if s.ID == chatSessionID {
						m.scope = s.Scope
						break
					}
				}
			}
		}
		return m.loadSessionInfo()
	}

	var sessions []sessionIndex
	path := "/api/v1/chat/sessions?scope=" + m.scope
	if err := m.client.get(path, &sessions); err == nil && len(sessions) > 0 {
		m.sessionID = sessions[0].ID
		m.model = sessions[0].Model
		if m.scope == "" && sessions[0].Scope != "" {
			m.scope = sessions[0].Scope
		}
		title := sessions[0].Title
		if title == "" {
			title = sessions[0].ID
		}
		m.initLines = append(m.initLines, "Resuming: "+title)
		m.loadAndShowHistory()
		return nil
	}

	return m.createNewSession()
}

func (m *chatModel) createNewSession() error {
	reqBody := map[string]any{"scope": m.scope}
	if m.model != "" {
		reqBody["model"] = m.model
	}
	if len(m.skills) > 0 {
		reqBody["skills"] = m.skills
	}

	bodyBytes, _ := json.Marshal(reqBody)
	var resp sessionInfo
	if err := m.client.post("/api/v1/chat/sessions", bytes.NewReader(bodyBytes), &resp); err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	m.sessionID = resp.ID
	if resp.Model != "" {
		m.model = resp.Model
	}

	if m.initPending {
		// Store for deferred rendering
		m.initLines = append(m.initLines, "new:"+resp.ID)
	} else {
		m.appendContent(m.renderSuccess("New session: " + accentStyle.Render(resp.ID)))
		m.appendContent("")
	}
	return nil
}

func (m *chatModel) loadSessionInfo() error {
	var resp struct {
		ID       string           `json:"ID"`
		Model    string           `json:"Model"`
		Skills   string           `json:"Skills"`
		Title    string           `json:"Title"`
		Usage    struct {
			InputTokens  int64   `json:"InputTokens"`
			OutputTokens int64   `json:"OutputTokens"`
			Cost         float64 `json:"Cost"`
		} `json:"Usage"`
		Messages []sessionMessage `json:"Messages"`
	}
	if err := m.client.get("/api/v1/chat/sessions/"+m.sessionID, &resp); err != nil {
		return fmt.Errorf("session not found: %w", err)
	}

	if resp.Model != "" {
		m.model = resp.Model
	}
	if resp.Skills != "" {
		m.skills = strings.Split(resp.Skills, ",")
	}
	m.totalInputTokens = int(resp.Usage.InputTokens)
	m.totalOutputTokens = int(resp.Usage.OutputTokens)
	m.totalCost = resp.Usage.Cost

	if m.initPending {
		if resp.Title != "" {
			m.initLines = append(m.initLines, "Resuming: "+resp.Title)
		}
		m.initLines = append(m.initLines, "Session: "+m.sessionID)
		m.initMessages = resp.Messages
	} else {
		if resp.Title != "" {
			m.appendContent(m.renderInfo("Resuming: " + resp.Title))
		}
		m.appendContent(m.renderInfo("Session: " + m.sessionID))
		m.appendContent("")
		m.showRecentMessages(resp.Messages)
	}
	return nil
}

func (m *chatModel) loadAndShowHistory() {
	var resp struct {
		Usage struct {
			InputTokens  int64   `json:"InputTokens"`
			OutputTokens int64   `json:"OutputTokens"`
			Cost         float64 `json:"Cost"`
		} `json:"Usage"`
		Messages []sessionMessage `json:"Messages"`
	}
	if err := m.client.get("/api/v1/chat/sessions/"+m.sessionID, &resp); err != nil {
		return
	}
	m.totalInputTokens = int(resp.Usage.InputTokens)
	m.totalOutputTokens = int(resp.Usage.OutputTokens)
	m.totalCost = resp.Usage.Cost
	if m.initPending {
		m.initMessages = resp.Messages
	} else {
		m.showRecentMessages(resp.Messages)
	}
}

// displaySessionInit renders the deferred init content now that m.width is correct.
func (m *chatModel) displaySessionInit() {
	if !m.initPending {
		return
	}
	m.initPending = false

	for _, line := range m.initLines {
		if strings.HasPrefix(line, "new:") {
			sid := strings.TrimPrefix(line, "new:")
			m.appendContent(m.renderSuccess("New session: " + accentStyle.Render(sid)))
		} else {
			m.appendContent(m.renderInfo(line))
		}
	}
	m.appendContent("")
	m.initLines = nil

	if len(m.initMessages) > 0 {
		m.showRecentMessages(m.initMessages)
		m.initMessages = nil
	}
}

func (m *chatModel) showRecentMessages(messages []sessionMessage) {
	if len(messages) == 0 {
		return
	}

	start := len(messages) - 6
	if start < 0 {
		start = 0
	}
	if start > 0 {
		m.appendContent(m.renderInfo(fmt.Sprintf("... %d earlier messages", start)))
		m.appendContent("")
	}

	for _, msg := range messages[start:] {
		content := strings.TrimSpace(msg.Content)
		// Collapse multiple blank lines to single newline for compact history display
		for strings.Contains(content, "\n\n") {
			content = strings.ReplaceAll(content, "\n\n", "\n")
		}

		switch msg.Role {
		case "user":
			m.appendContent(m.renderUserMsg(content))
			m.appendContent("")
		case "assistant":
			m.appendContent(m.renderAssistantMsg(content))
			m.appendContent("")
		}
	}
}

// --- Slash commands ---

func (m *chatModel) handleSlashCommand(input string) {
	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/help":
		m.appendContent("")
		m.appendContent(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#d2a8ff")).PaddingLeft(1).Render("Commands:"))
		m.appendContent("")
		m.appendContent(" " + accentStyle.Render("/model <name>") + subtitleStyle.Render("     Switch model"))
		m.appendContent(" " + accentStyle.Render("/model") + subtitleStyle.Render("             Show current model"))
		m.appendContent(" " + accentStyle.Render("/skills <s1,s2>") + subtitleStyle.Render("   Enable skills"))
		m.appendContent(" " + accentStyle.Render("/skills off") + subtitleStyle.Render("        Disable all skills"))
		m.appendContent(" " + accentStyle.Render("/skills") + subtitleStyle.Render("            Show active skills"))
		m.appendContent(" " + accentStyle.Render("/new") + subtitleStyle.Render("               Start new session"))
		m.appendContent(" " + accentStyle.Render("/history") + subtitleStyle.Render("           List past sessions"))
		m.appendContent(" " + accentStyle.Render("/switch <id>") + subtitleStyle.Render("      Switch to session"))
		m.appendContent(" " + accentStyle.Render("/help") + subtitleStyle.Render("              Show this help"))
		m.appendContent("")

	case "/model":
		if len(parts) > 1 {
			m.model = parts[1]
			m.patchSession(map[string]any{"model": m.model})
			m.appendContent(m.renderSuccess("Model: " + accentStyle.Render(m.model)))
			m.appendContent("")
		} else {
			m.showModelSelect()
		}

	case "/skills":
		if len(parts) > 1 {
			if parts[1] == "off" || parts[1] == "none" || parts[1] == "clear" {
				m.skills = nil
				m.patchSession(map[string]any{"skills": []string{}})
				m.appendContent(m.renderSuccess("All skills disabled"))
			} else {
				m.skills = nil
				for _, s := range strings.Split(parts[1], ",") {
					s = strings.TrimSpace(s)
					if s != "" {
						m.skills = append(m.skills, s)
					}
				}
				m.patchSession(map[string]any{"skills": m.skills})
				m.appendContent(m.renderSuccess("Skills: " + accentStyle.Render(strings.Join(m.skills, ", "))))
			}
		} else {
			if len(m.skills) > 0 {
				m.appendContent(m.renderInfo("Active: " + accentStyle.Render(strings.Join(m.skills, ", ")) +
					subtitleStyle.Render("  (/skills off to disable)")))
				m.appendContent("")
			}
			m.fetchAndShowSkills()
		}
		m.appendContent("")

	case "/new":
		m.totalInputTokens = 0
		m.totalOutputTokens = 0
		m.contextPct = 0
		m.content.Reset()
		if err := m.createNewSession(); err != nil {
			m.appendContent(m.renderError(err.Error()))
			m.appendContent("")
		}

	case "/history":
		m.showSessionHistorySelect()

	case "/switch":
		if len(parts) < 2 {
			m.appendContent(m.renderInfo("Usage: /switch <session-id>"))
			m.appendContent("")
			return
		}
		m.switchSession(parts[1])

	default:
		m.appendContent(m.renderError("Unknown: "+cmd) + subtitleStyle.Render("  /help for commands"))
		m.appendContent("")
	}
}

func (m *chatModel) switchSession(targetID string) {
	var resp struct {
		ID       string           `json:"ID"`
		Model    string           `json:"Model"`
		Skills   string           `json:"Skills"`
		Title    string           `json:"Title"`
		Usage    struct {
			InputTokens  int64   `json:"InputTokens"`
			OutputTokens int64   `json:"OutputTokens"`
			Cost         float64 `json:"Cost"`
		} `json:"Usage"`
		Messages []sessionMessage `json:"Messages"`
	}
	if err := m.client.get("/api/v1/chat/sessions/"+targetID, &resp); err != nil {
		m.appendContent(m.renderError("session not found: " + targetID))
		m.appendContent("")
		return
	}

	m.sessionID = targetID
	m.totalInputTokens = int(resp.Usage.InputTokens)
	m.totalOutputTokens = int(resp.Usage.OutputTokens)
	m.totalCost = resp.Usage.Cost
	m.contextPct = 0
	if resp.Model != "" {
		m.model = resp.Model
	}
	if resp.Skills != "" {
		m.skills = strings.Split(resp.Skills, ",")
	} else {
		m.skills = nil
	}

	m.content.Reset()
	title := resp.Title
	if title == "" {
		title = targetID
	}
	m.appendContent(m.renderSuccess("Switched to: " + accentStyle.Render(title)))
	m.appendContent("")
	m.showRecentMessages(resp.Messages)
}

func (m *chatModel) patchSession(fields map[string]any) {
	if m.sessionID == "" {
		return
	}
	bodyBytes, _ := json.Marshal(fields)
	if err := m.client.patch("/api/v1/chat/sessions/"+m.sessionID, bytes.NewReader(bodyBytes), nil); err != nil {
		m.appendContent(m.renderError("sync failed: " + err.Error()))
	}
}

func (m *chatModel) showSessionHistorySelect() {
	path := "/api/v1/chat/sessions?scope=" + m.scope

	var sessions []sessionIndex
	if err := m.client.get(path, &sessions); err != nil {
		m.appendContent(m.renderError(err.Error()))
		m.appendContent("")
		return
	}

	if len(sessions) == 0 {
		m.appendContent(m.renderInfo("No sessions found."))
		m.appendContent("")
		return
	}

	items := make([]selectItem, 0, len(sessions))
	for _, s := range sessions {
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		titleRunes := []rune(title)
		if len(titleRunes) > 40 {
			title = string(titleRunes[:37]) + "..."
		}
		sid := s.ID
		if len(sid) > 10 {
			sid = sid[:10]
		}
		meta := fmt.Sprintf("[%s · %d msgs · $%.4f]", sid, s.MessageCount, s.TotalCost)
		items = append(items, selectItem{id: s.ID, label: title, meta: meta})
	}

	m.enterSelectMode("Sessions", items, func(item selectItem) {
		m.switchSession(item.id)
	})
}

// --- Fetch available models/skills from daemon API ---

func (m *chatModel) showModelSelect() {
	type apiModel struct {
		Name     string `json:"name"`
		Provider string `json:"provider"`
	}

	var models []apiModel
	if err := m.client.get("/api/v1/models", &models); err != nil {
		m.appendContent(m.renderInfo("(could not fetch models: " + err.Error() + ")"))
		m.appendContent("")
		return
	}

	if len(models) == 0 {
		m.appendContent(m.renderInfo("No models available. Configure a provider first."))
		m.appendContent("")
		return
	}

	items := make([]selectItem, 0, len(models))
	cursorIdx := 0
	for i, mod := range models {
		if mod.Name == m.model {
			cursorIdx = i
		}
		items = append(items, selectItem{id: mod.Name, label: mod.Name, meta: mod.Provider})
	}

	m.enterSelectMode("Select Model", items, func(item selectItem) {
		m.model = item.id
		m.patchSession(map[string]any{"model": m.model})
		m.appendContent(m.renderSuccess("Model: " + accentStyle.Render(m.model)))
		m.appendContent("")
	})
	m.selectCursor = cursorIdx
}

func (m *chatModel) fetchAndShowSkills() {
	type apiSkill struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Scope       string `json:"scope"`
	}

	var skills []apiSkill
	if err := m.client.get("/api/v1/skills", &skills); err != nil {
		m.appendContent(m.renderInfo("(could not fetch skills: " + err.Error() + ")"))
		return
	}

	if len(skills) == 0 {
		m.appendContent(m.renderInfo("No skills installed."))
		return
	}

	activeSet := make(map[string]bool)
	for _, s := range m.skills {
		activeSet[s] = true
	}

	m.appendContent(m.renderInfo("Available skills:"))
	for _, sk := range skills {
		check := subtitleStyle.Render(" [ ] ")
		if activeSet[sk.Name] {
			check = successStyle.Render(" [✓] ")
		}
		name := accentStyle.Render(sk.Name)
		scope := ""
		if sk.Scope == "system" {
			scope = subtitleStyle.Render(" (built-in)")
		}
		desc := ""
		if sk.Description != "" {
			d := []rune(sk.Description)
			if len(d) > 35 {
				desc = subtitleStyle.Render(" — " + string(d[:32]) + "...")
			} else {
				desc = subtitleStyle.Render(" — " + string(d))
			}
		}
		m.appendContent(check + name + scope + desc)
	}
	m.appendContent("")
	m.appendContent(m.renderInfo("Use " + accentStyle.Render("/skills name1,name2") + subtitleStyle.Render(" to enable")))
	m.appendContent(m.renderInfo("Use " + accentStyle.Render("/skills off") + subtitleStyle.Render(" to disable all")))
}

// --- SSE streaming via Session API ---

func (m *chatModel) streamChatMessage(message string) {
	reqBody := map[string]string{"message": message}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		m.sendMsg(streamErrorMsg{err: err.Error()})
		return
	}

	req, err := http.NewRequest("POST",
		fmt.Sprintf("http://localhost:%d/api/v1/chat/sessions/%s/messages", startPort, m.sessionID),
		bytes.NewReader(bodyBytes))
	if err != nil {
		m.sendMsg(streamErrorMsg{err: err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if m.client.token != "" {
		req.Header.Set("Authorization", "Bearer "+m.client.token)
	}

	httpClient := &http.Client{Timeout: 30 * time.Minute}
	resp, err := httpClient.Do(req)
	if err != nil {
		m.sendMsg(streamErrorMsg{err: fmt.Sprintf("connection failed: %v", err)})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		m.sendMsg(streamErrorMsg{err: fmt.Sprintf("API error (%d): %s", resp.StatusCode, string(body))})
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var fullContent strings.Builder
	var doneMsg streamDoneMsg

	for scanner.Scan() {
		line := scanner.Text()
		eventType, ok := strings.CutPrefix(line, "event: ")
		if !ok {
			continue
		}
		if !scanner.Scan() {
			break
		}
		dataStr, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}

		switch eventType {
		case "chunk":
			var chunk sseChunk
			if json.Unmarshal([]byte(dataStr), &chunk) == nil && chunk.Delta != "" {
				fullContent.WriteString(chunk.Delta)
				m.sendMsg(streamChunkMsg{delta: chunk.Delta})
			}
		case "tool_call":
			var tc sseToolCall
			if json.Unmarshal([]byte(dataStr), &tc) == nil {
				m.sendMsg(streamToolMsg{tool: tc.Tool, durationMs: tc.DurationMs})
			}
		case "context_compact":
			m.sendMsg(streamCompactMsg{})
		case "done":
			var done sseDone
			if json.Unmarshal([]byte(dataStr), &done) == nil {
				result := fullContent.String()
				if result == "" {
					result = done.Result
				}
				doneMsg = streamDoneMsg{
					result:       result,
					model:        done.Model,
					inputTokens:  done.TotalInputTokens,
					outputTokens: done.TotalOutputTokens,
					cost:         done.TotalCost,
					contextPct:   done.ContextUsagePercent,
				}
			}
		case "error":
			var sseErr sseError
			if json.Unmarshal([]byte(dataStr), &sseErr) == nil {
				m.sendMsg(streamErrorMsg{err: sseErr.Error})
				return
			}
		}
	}

	if doneMsg.model == "" && fullContent.Len() == 0 {
		m.sendMsg(streamErrorMsg{err: "stream ended unexpectedly"})
		return
	}
	if doneMsg.model == "" {
		doneMsg.result = fullContent.String()
		doneMsg.model = "unknown"
	}
	m.sendMsg(doneMsg)
}

// sendMsg safely sends a message to the Bubble Tea program (goroutine-safe).
func (m *chatModel) sendMsg(msg tea.Msg) {
	if m.program != nil {
		m.program.Send(msg)
	}
}

func formatTokens(n int) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// --- Entry point ---

func runChat(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ " + err.Error()))
		fmt.Println()
		return err
	}

	model := newChatModel(client)

	if err := model.ensureSession(); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ " + err.Error()))
		fmt.Println()
		return err
	}

	p := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	model.program = p

	_, err := p.Run()
	return err
}
