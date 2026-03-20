package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	glamourstyles "github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	chatSessionID   string
	chatAgentName   string
	chatForceNew    bool     // skip auto-resume, always create a new session
	chatInitMessage string   // auto-send this message when chat starts
	chatInitSkills  []string // pre-load skills into new session
)

var chatCmd = &cobra.Command{
	Use:   "chat [message]",
	Short: "Interactive AI chat",
	Long: `Start an interactive chat session with the Tofi engine.

  tofi chat                    Interactive TUI
  tofi chat "hello"            TUI with initial message
  tofi chat -p "hello"         Non-interactive: print response to stdout
  tofi chat -c                 Continue last session
  tofi chat send "hello"       Non-interactive send (alias for -p)
  tofi chat history            List past sessions
  tofi chat model [name]       View or set model
  tofi chat new                Create new session`,
	Args: cobra.ArbitraryArgs,
	RunE: runChat,
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
	Input      string `json:"input"`
	Output     string `json:"output"`
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
type streamThinkingMsg struct{ delta string }
type streamToolMsg struct {
	tool       string
	input      string
	output     string
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
type ctrlCResetMsg struct{}
type titlePollMsg struct{}      // poll server for AI-generated title
type titleAnimTickMsg struct{}  // typewriter animation tick
type resumeDoneMsg struct{ title string } // restore header after "Resuming" flash

// --- Slash command definitions ---

type slashCmd struct {
	cmd  string
	desc string
}

var slashCommands = []slashCmd{
	{"/help", "Show available commands"},
	{"/status", "Session info and usage"},
	{"/model", "Switch or view model"},
	{"/skills", "Manage skills"},
	{"/new", "Start new session"},
	{"/resume", "Resume a past session"},
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

	streaming    bool
	streamCancel context.CancelFunc // cancel SSE stream
	ctrlCOnce    bool               // first Ctrl+C pressed
	ctrlCTimer   *time.Timer        // 3s reset timer
	inputHistory []string // user message history
	historyIdx   int     // -1 = not browsing, 0..len-1 = browsing
	client       *apiClient
	sessionID    string
	scope        string
	model        string
	skills       []string
	sessionTitle     string // displayed in header: "New Chat" → AI-generated title
	isNewSession     bool   // true until first AI response title is set
	titleAnimating   bool   // typewriter animation in progress
	titleTarget      string // target title to animate to
	titleAnimIdx     int    // current char index in animation
	titlePollCount   int    // retry counter for title polling
	titleInitial     string // initial truncated title (to detect AI update)
	resumingTitle    string // non-empty = header shows "Resuming · title" temporarily

	// Thinking/reasoning display
	thinkingBuf   strings.Builder // accumulated thinking text
	thinkingDone  bool            // true after first content chunk arrives
	thinkingStart time.Time       // when thinking started
	contextPct        int
	width             int
	height            int
	totalInputTokens  int
	totalOutputTokens int
	totalCost         float64
	ready             bool

	// Interactive selection mode
	selectMode          bool
	selectTitle         string
	selectItems         []selectItem  // all items
	selectFiltered      []selectItem  // filtered subset (nil = show all)
	selectFilter        string        // current search/filter text
	selectCursor        int
	selectSelected      int // index of selected item (-1 = none, single select)
	selectAction        func(item selectItem)
	selectMulti         bool                    // multi-select mode
	selectMultiChecked  map[string]bool         // id → checked state
	selectMultiAction   func(items []selectItem)

	// Deferred init rendering (ensureSession runs before TUI has width)
	initPending  bool
	initLines    []string           // raw info lines (not width-dependent)
	initMessages []sessionMessage   // raw messages to render after WindowSizeMsg

	// Slash command completion
	completionMode   bool
	completionCmds   []slashCmd
	completionCursor int

	// Initial message to auto-send after TUI is ready
	initialMessage string

	// Markdown renderer
	mdRenderer *glamour.TermRenderer
}

type selectItem struct {
	id    string
	label string
	meta  string
}

func newChatModel(client *apiClient) *chatModel {
	ta := textarea.New()
	ta.Placeholder = "Type a message... (Enter send, Alt+Enter newline, /help commands)"
	ta.Focus()
	ta.CharLimit = 4096
	ta.SetHeight(2)
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetEnabled(false) // we handle newline manually via Alt+Enter
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

	// Create glamour renderer with compact margins (1 char indent instead of default 2)
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStyles(chatGlamourStyle()),
		glamour.WithWordWrap(0), // we'll set width dynamically
	)

	m := &chatModel{
		textarea:   ta,
		client:     client,
		scope:      scope,
		mdRenderer: renderer,
	}

	// Pick up initial message (e.g. from App TUI "Chat with AI")
	if chatInitMessage != "" {
		m.initialMessage = chatInitMessage
		chatInitMessage = "" // reset
	}

	// Pick up pre-loaded skills (e.g. app skills from TUI)
	if len(chatInitSkills) > 0 {
		m.skills = chatInitSkills
		chatInitSkills = nil // reset
	}

	return m
}

// headerTitle returns the styled header with session title.
func (m *chatModel) headerTitle() string {
	prefix := "TOFI Chat"
	if agentName, ok := strings.CutPrefix(m.scope, "agent:"); ok {
		prefix = "TOFI Chat — " + agentName
	}

	styledPrefix := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff7b72")).Render(" " + prefix + " ")

	// Temporary "Resuming" flash
	if m.resumingTitle != "" {
		resumePart := " · Resuming · " + m.resumingTitle
		styledResume := lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922")).Render(resumePart)
		return styledPrefix + styledResume
	}

	if m.sessionTitle == "" {
		return styledPrefix
	}

	sessionPart := " · " + m.sessionTitle
	styledSession := lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e")).Render(sessionPart)
	return styledPrefix + styledSession
}

// innerWidth returns the content width inside the border frame.
// Layout: │ <content> │ → border(1) + pad(1) + content(iw) + pad(1) + border(1) = width
func (m *chatModel) innerWidth() int {
	return max(20, m.width-4)
}

// autoResizeTextarea adjusts textarea height based on content lines (2-8 lines).
func (m *chatModel) autoResizeTextarea() {
	lines := strings.Count(m.textarea.Value(), "\n") + 1
	newHeight := max(2, min(8, lines))
	if newHeight != m.textarea.Height() {
		m.textarea.SetHeight(newHeight)
		// Recalculate viewport height
		vpHeight := max(3, m.height-m.fixedLines())
		m.viewport.Height = vpHeight
		m.refreshViewport()
	}
}

// Fixed lines in the frame (everything except viewport).
// padding(1) + top_border(1) + separator(1) + status(1) + separator(1) + textarea_lines + bottom_border(1)
func (m *chatModel) fixedLines() int {
	return 6 + m.textarea.Height()
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
		// Recreate glamour renderer with new width
		if r, err := glamour.NewTermRenderer(
			glamour.WithStyles(chatGlamourStyle()),
			glamour.WithWordWrap(iw-2),
		); err == nil {
			m.mdRenderer = r
		}

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
			// Auto-send initial message if provided via CLI args
			if m.initialMessage != "" {
				msg := m.initialMessage
				m.initialMessage = ""
				m.appendContent(m.renderUserMsg(msg))
				m.appendContent("")
				m.streaming = true
				m.thinkingBuf.Reset()
				m.thinkingDone = false
				m.thinkingStart = time.Time{}
				ctx, cancel := context.WithCancel(context.Background())
				m.streamCancel = cancel
				m.viewport.SetContent(m.viewportContent())
				m.viewport.GotoBottom()
				go m.streamChatMessage(ctx, msg)
			}
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
			if m.streaming {
				// While streaming: stop generation
				if m.streamCancel != nil {
					m.streamCancel()
				}
				return m, nil
			}
			if m.ctrlCOnce {
				// Second Ctrl+C within 3s → actually quit
				return m, tea.Quit
			}
			// First Ctrl+C → show warning, start 3s timer
			m.ctrlCOnce = true
			m.ctrlCTimer = time.NewTimer(3 * time.Second)
			go func() {
				<-m.ctrlCTimer.C
				m.sendMsg(ctrlCResetMsg{})
			}()
			return m, nil
		case "esc":
			if m.streaming {
				// Esc while streaming → stop generation
				if m.streamCancel != nil {
					m.streamCancel()
				}
			}
			// Esc when idle → do nothing (use Ctrl+C twice to quit)
			return m, nil
		case "alt+enter":
			// Insert newline for multi-line input
			if !m.streaming {
				m.textarea.InsertString("\n")
				m.autoResizeTextarea()
				return m, nil
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
			m.autoResizeTextarea()

			if strings.HasPrefix(input, "/") {
				m.handleSlashCommand(input)
				return m, nil
			}

			// Save to input history
			m.inputHistory = append(m.inputHistory, input)
			m.historyIdx = -1

			m.appendContent(m.renderUserMsg(input))
			m.appendContent("")

			m.streaming = true
			m.thinkingBuf.Reset()
			m.thinkingDone = false
			m.thinkingStart = time.Time{}
			m.textarea.Blur()
			ctx, cancel := context.WithCancel(context.Background())
			m.streamCancel = cancel
			m.refreshViewport()

			go m.streamChatMessage(ctx, input)
			return m, nil

		case "up":
			// Up arrow: browse input history (only when textarea is empty or already browsing)
			if !m.streaming && len(m.inputHistory) > 0 {
				val := strings.TrimSpace(m.textarea.Value())
				if val == "" || m.historyIdx >= 0 {
					if m.historyIdx < 0 {
						m.historyIdx = len(m.inputHistory) - 1
					} else if m.historyIdx > 0 {
						m.historyIdx--
					}
					m.textarea.SetValue(m.inputHistory[m.historyIdx])
					m.textarea.CursorEnd()
					return m, nil
				}
			}
		case "down":
			// Down arrow: navigate forward in history
			if !m.streaming && m.historyIdx >= 0 {
				if m.historyIdx < len(m.inputHistory)-1 {
					m.historyIdx++
					m.textarea.SetValue(m.inputHistory[m.historyIdx])
					m.textarea.CursorEnd()
				} else {
					m.historyIdx = -1
					m.textarea.SetValue("")
				}
				return m, nil
			}

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

	case streamThinkingMsg:
		if m.thinkingStart.IsZero() {
			m.thinkingStart = time.Now()
		}
		m.thinkingBuf.WriteString(msg.delta)
		m.refreshViewport()
		return m, nil

	case streamChunkMsg:
		// First content chunk = thinking is done
		if !m.thinkingDone && m.thinkingBuf.Len() > 0 {
			m.thinkingDone = true
		}
		m.streamBuf.WriteString(msg.delta)
		m.refreshViewport()
		return m, nil

	case streamToolMsg:
		if !m.thinkingDone && m.thinkingBuf.Len() > 0 {
			m.thinkingDone = true
		}
		m.finalizeStreamBlock()
		if msg.tool == "tofi_display_app_plan" && msg.input != "" {
			m.appendContent(m.renderAppPlan(msg.input))
		} else {
			m.appendContent(m.renderToolCall(msg.tool, msg.input, msg.durationMs))
		}
		m.appendContent("") // blank line after tool call
		return m, nil

	case streamCompactMsg:
		m.appendContent(lipgloss.NewStyle().
			Foreground(lipgloss.Color("#d29922")).
			Italic(true).
			PaddingLeft(1).
			Render("⟳ Context compacted"))
		return m, nil

	case streamDoneMsg:
		m.thinkingDone = true // ensure thinking is collapsed
		m.finalizeStreamBlock()
		m.streaming = false
		m.textarea.Focus()
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

		// After first AI response on new session, poll for AI-generated title
		if m.isNewSession {
			m.isNewSession = false
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return titlePollMsg{} })
		}
		return m, nil

	case streamErrorMsg:
		m.finalizeStreamBlock()
		m.streaming = false
		m.textarea.Focus()
		m.appendContent(m.renderError(msg.err))
		m.appendContent("")
		return m, nil

	case ctrlCResetMsg:
		m.ctrlCOnce = false
		return m, nil

	case resumeDoneMsg:
		// Clear the "Resuming" flash, restore normal title
		if m.resumingTitle == msg.title {
			m.resumingTitle = ""
		}
		return m, nil

	case titlePollMsg:
		m.titlePollCount++
		if m.titlePollCount > 10 {
			return m, nil // give up
		}
		// Poll server for AI-generated title
		var resp struct{ Title string `json:"Title"` }
		if err := m.client.get("/api/v1/chat/sessions/"+m.sessionID, &resp); err == nil && resp.Title != "" {
			// Compare with what's currently displayed ("New Chat" or truncated text)
			if resp.Title != m.sessionTitle {
				// Check if this is just the truncated version (not AI-generated yet)
				if m.titleInitial == "" {
					m.titleInitial = resp.Title // first value seen = truncated
				}
				// If different from initial (truncated) OR first poll already has AI title
				// (detected by: title is shorter than truncated, or totally different)
				isAITitle := resp.Title != m.titleInitial || m.titlePollCount >= 2
				if isAITitle {
					m.titleTarget = resp.Title
					m.titleAnimIdx = 0
					m.titleAnimating = true
					m.sessionTitle = ""
					return m, tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return titleAnimTickMsg{} })
				}
				// First poll: just set the truncated title, keep polling for AI title
				m.sessionTitle = resp.Title
			}
		}
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return titlePollMsg{} })

	case titleAnimTickMsg:
		if !m.titleAnimating {
			return m, nil
		}
		titleRunes := []rune(m.titleTarget)
		m.titleAnimIdx++
		if m.titleAnimIdx >= len(titleRunes) {
			// Animation complete
			m.sessionTitle = m.titleTarget
			m.titleAnimating = false
			return m, nil
		}
		m.sessionTitle = string(titleRunes[:m.titleAnimIdx])
		return m, tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return titleAnimTickMsg{} })
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

	// Top border with title (extra padding line above)
	out.WriteString("\n")
	title := m.headerTitle()
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
	} else if m.streaming {
		// During streaming: show muted placeholder instead of textarea
		streamHint := subtitleStyle.Render("...")
		out.WriteString(m.padBorderLine(" "+streamHint, iw) + "\n")
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
	var leftText string
	selectKey := "Opt"
	if runtime.GOOS != "darwin" {
		selectKey = "Shift"
	}
	if m.ctrlCOnce {
		leftText = errorStyle.Render("Press Ctrl+C again to exit")
	} else if m.streaming {
		leftText = subtitleStyle.Render("Esc stop · Ctrl+C stop · " + selectKey + "+drag select")
	} else {
		leftText = subtitleStyle.Render("/help · Esc quit · " + selectKey + "+drag select")
	}
	left := leftText

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
			Background(lipgloss.Color("#30363d")).
			Foreground(lipgloss.Color("#f0f6fc")).
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

func (m *chatModel) enterMultiSelectMode(title string, items []selectItem, preSelected map[string]bool, action func([]selectItem)) {
	m.selectMode = true
	m.selectMulti = true
	m.selectTitle = title
	m.selectItems = items
	m.selectCursor = 0
	m.selectSelected = -1
	m.selectMultiChecked = make(map[string]bool)
	for k, v := range preSelected {
		m.selectMultiChecked[k] = v
	}
	m.selectMultiAction = action
}

func (m *chatModel) exitSelectMode() {
	m.selectMode = false
	m.selectMulti = false
	m.selectItems = nil
	m.selectFiltered = nil
	m.selectFilter = ""
	m.selectAction = nil
	m.selectSelected = -1
	m.selectMultiChecked = nil
	m.selectMultiAction = nil
}

// selectVisible returns the currently visible items (filtered or all).
func (m *chatModel) selectVisible() []selectItem {
	if m.selectFiltered != nil {
		return m.selectFiltered
	}
	return m.selectItems
}

// applySelectFilter filters selectItems by the current filter string.
func (m *chatModel) applySelectFilter() {
	if m.selectFilter == "" {
		m.selectFiltered = nil
		return
	}
	query := strings.ToLower(m.selectFilter)
	filtered := make([]selectItem, 0)
	for _, item := range m.selectItems {
		if strings.Contains(strings.ToLower(item.label), query) ||
			strings.Contains(strings.ToLower(item.meta), query) {
			filtered = append(filtered, item)
		}
	}
	m.selectFiltered = filtered
	m.selectCursor = 0
	m.selectSelected = -1
}

func (m *chatModel) updateSelectMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	visible := m.selectVisible()

	switch msg.String() {
	case "esc":
		if m.selectFilter != "" {
			m.selectFilter = ""
			m.applySelectFilter()
			return m, nil
		}
		m.exitSelectMode()
		return m, nil
	case "up", "ctrl+p":
		if m.selectCursor > 0 {
			m.selectCursor--
		}
		return m, nil
	case "down", "ctrl+n":
		if m.selectCursor < len(visible)-1 {
			m.selectCursor++
		}
		return m, nil
	case " ": // Space = select/toggle
		if len(visible) > 0 && m.selectCursor < len(visible) {
			if m.selectMulti {
				// Multi-select: toggle checked state
				item := visible[m.selectCursor]
				m.selectMultiChecked[item.id] = !m.selectMultiChecked[item.id]
			} else {
				// Single-select: toggle selection
				if m.selectSelected == m.selectCursor {
					m.selectSelected = -1
				} else {
					m.selectSelected = m.selectCursor
				}
			}
		}
		return m, nil
	case "enter": // Enter = confirm
		if m.selectMulti {
			// Collect all checked items
			var selected []selectItem
			for _, item := range m.selectItems {
				if m.selectMultiChecked[item.id] {
					selected = append(selected, item)
				}
			}
			action := m.selectMultiAction
			m.exitSelectMode()
			if action != nil {
				action(selected)
			}
		} else {
			// Single select
			idx := m.selectSelected
			if idx < 0 {
				idx = m.selectCursor
			}
			if idx >= 0 && idx < len(visible) {
				item := visible[idx]
				action := m.selectAction
				m.exitSelectMode()
				if action != nil {
					action(item)
				}
			}
		}
		return m, nil
	case "backspace":
		if len(m.selectFilter) > 0 {
			m.selectFilter = m.selectFilter[:len(m.selectFilter)-1]
			m.applySelectFilter()
		}
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	default:
		// Type to filter — only accept printable single runes
		key := msg.String()
		if len(key) == 1 && key[0] >= 32 && key[0] < 127 {
			m.selectFilter += key
			m.applySelectFilter()
		}
		return m, nil
	}
}

func (m *chatModel) renderSelectList(iw int, maxLines int) []string {
	lines := make([]string, 0, maxLines)
	visible := m.selectVisible()

	// Title + optional filter
	titleLine := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff7b72")).PaddingLeft(1).Render(m.selectTitle)
	if m.selectFilter != "" {
		filterDisplay := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc")).Render(" 🔍 " + m.selectFilter + "▎")
		titleLine += "  " + filterDisplay
	}
	lines = append(lines, "", titleLine, "")

	if len(visible) == 0 {
		lines = append(lines, subtitleStyle.Render("   (no matches)"))
		for len(lines) < maxLines {
			lines = append(lines, "")
		}
		return lines
	}

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
	if endIdx > len(visible) {
		endIdx = len(visible)
	}

	for i := startIdx; i < endIdx; i++ {
		item := visible[i]
		var line string

		isCursor := i == m.selectCursor
		isChecked := false
		if m.selectMulti {
			isChecked = m.selectMultiChecked[item.id]
		} else {
			isChecked = i == m.selectSelected
		}

		if isCursor {
			// Cursor row — highlighted background, full width
			indicator := " ► "
			if isChecked {
				indicator = " ✓ "
			}
			plain := indicator + item.label
			if item.meta != "" {
				plain += "  " + item.meta
			}
			plainW := lipgloss.Width(plain)
			pad := max(0, iw-plainW)
			line = lipgloss.NewStyle().
				Background(lipgloss.Color("#ff7b72")).
				Foreground(lipgloss.Color("#ffffff")).
				Bold(true).
				Render(plain + strings.Repeat(" ", pad))
		} else {
			indicator := subtitleStyle.Render(" ○ ")
			if isChecked {
				indicator = successStyle.Render(" ✓ ")
			}
			label := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc")).Render(item.label)
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
				Background(lipgloss.Color("#ff7b72")).
				Foreground(lipgloss.Color("#ffffff")).
				Bold(true).
				Render(plain + strings.Repeat(" ", pad))
			lines = append(lines, line)
		} else {
			name := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc")).Render(fmt.Sprintf("%-12s", cmd.cmd))
			desc := subtitleStyle.Render(cmd.desc)
			lines = append(lines, "   "+name+" "+desc)
		}
	}

	return lines
}

// --- Content management ---

func (m *chatModel) viewportContent() string {
	base := "\n" + m.content.String()
	if m.streaming {
		base += m.renderStreamingBlock()
	}
	return base
}

func (m *chatModel) renderStreamingBlock() string {
	var out strings.Builder

	// Show thinking block (gray, max 3 lines)
	if m.thinkingBuf.Len() > 0 {
		thinkStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6e7681")).
			Italic(true).
			PaddingLeft(1)

		if m.thinkingDone {
			// Collapsed: show duration only
			dur := time.Since(m.thinkingStart).Truncate(time.Millisecond)
			out.WriteString(thinkStyle.Render(fmt.Sprintf("💭 thinking (%s)", dur)) + "\n")
		} else {
			// In progress: show last 3 lines of thinking
			lines := strings.Split(m.thinkingBuf.String(), "\n")
			start := len(lines) - 3
			if start < 0 {
				start = 0
			}
			iw := m.innerWidth() - 2 // padding
			for _, line := range lines[start:] {
				r := []rune(line)
				if len(r) > iw {
					r = r[:iw]
				}
				out.WriteString(thinkStyle.Render(string(r)) + "\n")
			}
		}
	}

	if m.streamBuf.Len() == 0 && !m.thinkingDone {
		if m.thinkingBuf.Len() == 0 {
			// No thinking either — show generic placeholder
			out.WriteString(lipgloss.NewStyle().
				Foreground(lipgloss.Color("#8b949e")).
				Italic(true).
				PaddingLeft(1).
				Render("thinking...") + "\n")
		}
		return out.String()
	}

	if m.streamBuf.Len() > 0 {
		label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff7b72")).PaddingLeft(1).Render("Tofi")
		text := m.renderMarkdown(m.streamBuf.String())
		out.WriteString(label + "\n" + text + "\n")
	}
	return out.String()
}

func (m *chatModel) finalizeStreamBlock() {
	// Persist thinking collapsed line if there was thinking
	if m.thinkingBuf.Len() > 0 && !m.thinkingStart.IsZero() {
		dur := time.Since(m.thinkingStart).Truncate(time.Millisecond)
		thinkLine := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6e7681")).
			Italic(true).
			PaddingLeft(1).
			Render(fmt.Sprintf("💭 thinking (%s)", dur))
		m.content.WriteString(thinkLine + "\n")
		m.thinkingBuf.Reset()
		m.thinkingStart = time.Time{}
	}

	if m.streamBuf.Len() == 0 {
		return
	}
	label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff7b72")).PaddingLeft(1).Render("Tofi")
	text := m.renderMarkdown(m.streamBuf.String())
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
	label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc")).PaddingLeft(1).Render("You")
	text := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc")).Width(iw).PaddingLeft(1).Render(content)
	return label + "\n" + text
}

func (m *chatModel) renderAssistantMsg(content string) string {
	label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff7b72")).PaddingLeft(1).Render("Tofi")
	text := m.renderMarkdown(content)
	return label + "\n" + text
}

// chatGlamourStyle returns a glamour style based on "dark" with compact margins.
func chatGlamourStyle() ansi.StyleConfig {
	// Start from the dark theme
	s := glamourstyles.DarkStyleConfig
	// Reduce document margin from default 2 to 1 for tighter layout
	one := uint(1)
	zero := uint(0)
	s.Document.Margin = &one
	s.Document.BlockPrefix = ""
	s.Document.BlockSuffix = ""
	// Remove paragraph margins to avoid double spacing
	s.Paragraph.Margin = &zero
	return s
}

// renderMarkdown renders content as Markdown using glamour.
// Falls back to plain text on error.
func (m *chatModel) renderMarkdown(content string) string {
	if m.mdRenderer == nil || strings.TrimSpace(content) == "" {
		iw := m.innerWidth()
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc")).Width(iw).PaddingLeft(1).Render(content)
	}
	rendered, err := m.mdRenderer.Render(content)
	if err != nil {
		iw := m.innerWidth()
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc")).Width(iw).PaddingLeft(1).Render(content)
	}
	// glamour adds trailing newlines; trim them
	rendered = strings.TrimRight(rendered, "\n")
	return rendered
}

// toolDisplayName maps internal tool IDs to user-friendly names.
var toolDisplayName = map[string]string{
	"tofi_shell":           "Shell",
	"tofi_read":            "Read",
	"tofi_write":           "Write",
	"tofi_search":          "Search",
	"tofi_wait":            "Wait",
	"tofi_save_memory":     "Save Memory",
	"tofi_recall_memory":   "Recall Memory",
	"tofi_get_time":        "Get Time",
	"tofi_get_user":        "Get User",
	"tofi_notify":          "Notify",
	"tofi_suggest_install": "Suggest Install",
	"tofi_update_progress": "Update Progress",
	"tofi_session_info":    "Session Info",
	"web_search":           "Web Search",
	"auto_compact":         "Auto Compact",
}

func (m *chatModel) renderToolCall(tool, input string, durationMs int) string {
	display := tool
	if friendly, ok := toolDisplayName[tool]; ok {
		display = friendly
	}
	icon := lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922")).Render("▸")
	name := lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922")).Bold(true).Render(display)
	dur := ""
	if durationMs > 0 {
		dur = subtitleStyle.Render(fmt.Sprintf(" (%dms)", durationMs))
	}

	// Extract a brief context hint from the input JSON
	hint := toolCallHint(tool, input)
	hintStr := ""
	if hint != "" {
		hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6e7681"))
		// Truncate to ~60 chars
		if len(hint) > 60 {
			hint = hint[:57] + "..."
		}
		hintStr = "  " + hintStyle.Render(hint)
	}

	return " " + icon + " " + name + dur + hintStr
}

// toolCallHint extracts a short context string from tool input for display.
func toolCallHint(tool, input string) string {
	if input == "" {
		return ""
	}

	// Parse input as JSON
	var args map[string]interface{}
	if json.Unmarshal([]byte(input), &args) != nil {
		return ""
	}

	// Tool-specific hint extraction
	switch {
	case tool == "tofi_shell":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	case tool == "tofi_load_skill":
		if name, ok := args["name"].(string); ok {
			return name
		}
	case tool == "tofi_read":
		if path, ok := args["path"].(string); ok {
			return path
		}
	case tool == "tofi_write":
		if path, ok := args["path"].(string); ok {
			return path
		}
	case strings.HasPrefix(tool, "tofi_") && strings.Contains(tool, "app"):
		// App tools: show app_id or name
		if id, ok := args["app_id"].(string); ok {
			return id
		}
		if id, ok := args["id"].(string); ok {
			return id
		}
		if name, ok := args["name"].(string); ok {
			return name
		}
	case tool == "tofi_recall_memory" || tool == "tofi_save_memory":
		if q, ok := args["query"].(string); ok {
			return q
		}
		if tags, ok := args["tags"].(string); ok {
			return tags
		}
	case strings.HasPrefix(tool, "run_skill__"):
		if inp, ok := args["input"].(string); ok {
			return inp
		}
	}

	// Fallback: try common field names
	for _, key := range []string{"query", "name", "command", "path", "id", "message", "input"} {
		if v, ok := args[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func (m *chatModel) renderAppPlan(output string) string {
	// Extract JSON portion — handler may append non-JSON text after the object
	jsonStr := output
	if idx := strings.Index(output, "\n\n["); idx > 0 {
		jsonStr = output[:idx]
	}

	var plan struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Description  string `json:"description"`
		Prompt       string `json:"prompt"`
		Model        string `json:"model"`
		Schedule     string `json:"schedule"`
		Timezone     string `json:"timezone"`
		Skills       string `json:"skills"`
		Notify       string `json:"notify"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &plan); err != nil {
		return " " + subtitleStyle.Render("(failed to parse app plan)")
	}

	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e")).Width(10).Align(lipgloss.Right)
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#484f58"))

	row := func(label, value string) string {
		if value == "" {
			return ""
		}
		return labelStyle.Render(label) + "  " + valueStyle.Render(value)
	}

	var rows []string
	rows = append(rows, row("ID", plan.ID))
	if plan.Name != "" && plan.Name != plan.ID {
		rows = append(rows, row("Name", plan.Name))
	}
	rows = append(rows, row("Desc", plan.Description))
	rows = append(rows, row("Model", plan.Model))

	// Prompt — truncate for display
	prompt := plan.Prompt
	if len(prompt) > 120 {
		prompt = prompt[:120] + "..."
	}
	rows = append(rows, row("Prompt", prompt))

	if plan.Schedule != "" {
		sched := plan.Schedule
		if plan.Timezone != "" {
			sched += " (" + plan.Timezone + ")"
		}
		rows = append(rows, row("Schedule", sched))
	}
	if plan.Skills != "" {
		rows = append(rows, row("Skills", plan.Skills))
	}
	if plan.Notify != "" {
		rows = append(rows, row("Notify", plan.Notify))
	}

	// Filter empty rows
	var nonEmpty []string
	for _, r := range rows {
		if r != "" {
			nonEmpty = append(nonEmpty, r)
		}
	}

	contentWidth := max(40, m.innerWidth()-6)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#30363d")).
		Padding(0, 2).
		Width(contentWidth)

	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff7b72")).Render("App Plan")
	sep := dimStyle.Render(strings.Repeat("─", contentWidth-6))
	content := header + "\n" + sep + "\n" + strings.Join(nonEmpty, "\n")

	return " " + box.Render(content)
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

	// When chatForceNew is set (e.g. TUI "Chat with AI"), skip auto-resume
	if chatForceNew {
		chatForceNew = false // reset for future calls
		return m.createNewSession()
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
		m.sessionTitle = title
		m.isNewSession = false
		m.resumingTitle = title // flash "Resuming · title" in header for 1s
		m.loadAndShowHistory()
		return nil
	}

	return m.createNewSession()
}

func (m *chatModel) createNewSession() error {
	// If model not yet known, resolve default from server
	if m.model == "" {
		m.resolveDefaultModel()
	}

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
	m.sessionTitle = "New Chat"
	m.isNewSession = true
	if resp.Model != "" {
		m.model = resp.Model
	}

	// Header shows "New Chat · session_id" — no content line needed
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

	if resp.Title != "" {
		m.sessionTitle = resp.Title
	} else {
		m.sessionTitle = m.sessionID
	}
	m.isNewSession = false

	// Flash "Resuming · title" in header
	m.resumingTitle = m.sessionTitle

	if m.initPending {
		m.initMessages = resp.Messages
	} else {
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

	m.initLines = nil

	// Schedule resume flash clear if resuming
	if m.resumingTitle != "" {
		m.scheduleResumeFlashClear(m.resumingTitle)
	}

	if len(m.initMessages) > 0 {
		m.showRecentMessages(m.initMessages)
		m.initMessages = nil
	}
}

// scheduleResumeFlashClear sends a resumeDoneMsg after 1s to clear the "Resuming" header flash.
func (m *chatModel) scheduleResumeFlashClear(title string) {
	go func() {
		time.Sleep(time.Second)
		if m.program != nil {
			m.program.Send(resumeDoneMsg{title: title})
		}
	}()
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
		m.appendContent(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff7b72")).PaddingLeft(1).Render("Commands:"))
		m.appendContent("")
		m.appendContent(" " + accentStyle.Render("/status") + subtitleStyle.Render("            Session info and usage"))
		m.appendContent(" " + accentStyle.Render("/model <name>") + subtitleStyle.Render("     Switch model"))
		m.appendContent(" " + accentStyle.Render("/model") + subtitleStyle.Render("             Show current model"))
		m.appendContent(" " + accentStyle.Render("/skills <s1,s2>") + subtitleStyle.Render("   Enable skills"))
		m.appendContent(" " + accentStyle.Render("/skills off") + subtitleStyle.Render("        Disable all skills"))
		m.appendContent(" " + accentStyle.Render("/skills") + subtitleStyle.Render("            Show active skills"))
		m.appendContent(" " + accentStyle.Render("/new") + subtitleStyle.Render("               Start new session"))
		m.appendContent(" " + accentStyle.Render("/resume") + subtitleStyle.Render("            Resume a past session"))
		m.appendContent(" " + accentStyle.Render("/resume <id>") + subtitleStyle.Render("      Resume session by ID"))
		m.appendContent(" " + accentStyle.Render("/help") + subtitleStyle.Render("              Show this help"))
		m.appendContent("")

	case "/status":
		m.appendContent("")
		m.appendContent(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff7b72")).PaddingLeft(1).Render("Session Status"))
		m.appendContent("")
		m.appendContent("  " + subtitleStyle.Render("Session: ") + accentStyle.Render(m.sessionID))
		m.appendContent("  " + subtitleStyle.Render("Model:   ") + accentStyle.Render(m.model))
		inTok := formatTokens(m.totalInputTokens)
		outTok := formatTokens(m.totalOutputTokens)
		m.appendContent("  " + subtitleStyle.Render("Tokens:  ") + lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc")).Render(fmt.Sprintf("↑%s in · ↓%s out", inTok, outTok)))
		m.appendContent("  " + subtitleStyle.Render("Cost:    ") + lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc")).Render(fmt.Sprintf("$%.4f", m.totalCost)))
		if m.contextPct > 0 {
			ctxColor := "#3fb950"
			if m.contextPct > 80 {
				ctxColor = "#f85149"
			} else if m.contextPct > 60 {
				ctxColor = "#d29922"
			}
			m.appendContent("  " + subtitleStyle.Render("Context: ") + lipgloss.NewStyle().Foreground(lipgloss.Color(ctxColor)).Render(fmt.Sprintf("%d%%", m.contextPct)))
		}
		if len(m.skills) > 0 {
			m.appendContent("  " + subtitleStyle.Render("Skills:  ") + accentStyle.Render(strings.Join(m.skills, ", ")))
		}
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
		m.totalCost = 0
		m.contextPct = 0
		m.resumingTitle = ""
		m.titleAnimating = false
		m.titleTarget = ""
		m.titleInitial = ""
		m.titlePollCount = 0
		m.content.Reset()
		if err := m.createNewSession(); err != nil {
			m.appendContent(m.renderError(err.Error()))
			m.appendContent("")
		}
		m.refreshViewport()

	case "/resume", "/history", "/switch":
		if len(parts) >= 2 {
			m.switchSession(parts[1])
		} else {
			m.showSessionHistorySelect()
		}

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
	m.sessionTitle = title
	m.isNewSession = false
	m.resumingTitle = title // flash in header
	m.scheduleResumeFlashClear(title)
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

// resolveDefaultModel fetches models list and picks the first one as default.
func (m *chatModel) resolveDefaultModel() {
	type apiModel struct {
		Name string `json:"name"`
	}
	var models []apiModel
	if err := m.client.get("/api/v1/models", &models); err == nil && len(models) > 0 {
		m.model = models[0].Name
	}
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

	// Build items for multi-select
	items := make([]selectItem, 0, len(skills))
	for _, sk := range skills {
		meta := sk.Description
		if sk.Scope == "system" {
			meta = "(built-in) " + meta
		}
		d := []rune(meta)
		if len(d) > 45 {
			meta = string(d[:42]) + "..."
		}
		items = append(items, selectItem{id: sk.Name, label: sk.Name, meta: meta})
	}

	// Pre-select currently active skills
	preSelected := make(map[string]bool)
	for _, s := range m.skills {
		preSelected[s] = true
	}

	m.enterMultiSelectMode("Select Skills (Space toggle, Enter confirm)", items, preSelected, func(selected []selectItem) {
		m.skills = nil
		for _, item := range selected {
			m.skills = append(m.skills, item.id)
		}
		m.patchSession(map[string]any{"skills": m.skills})
		if len(m.skills) > 0 {
			m.appendContent(m.renderSuccess("Skills: " + accentStyle.Render(strings.Join(m.skills, ", "))))
		} else {
			m.appendContent(m.renderSuccess("All skills disabled"))
		}
		m.appendContent("")
	})
}

// --- SSE streaming via Session API ---

func (m *chatModel) streamChatMessage(ctx context.Context, message string) {
	reqBody := map[string]string{"message": message}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		m.sendMsg(streamErrorMsg{err: err.Error()})
		return
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
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
		case "thinking":
			var chunk sseChunk
			if json.Unmarshal([]byte(dataStr), &chunk) == nil && chunk.Delta != "" {
				m.sendMsg(streamThinkingMsg{delta: chunk.Delta})
			}
		case "chunk":
			var chunk sseChunk
			if json.Unmarshal([]byte(dataStr), &chunk) == nil && chunk.Delta != "" {
				fullContent.WriteString(chunk.Delta)
				m.sendMsg(streamChunkMsg{delta: chunk.Delta})
			}
		case "tool_call":
			var tc sseToolCall
			if json.Unmarshal([]byte(dataStr), &tc) == nil {
				m.sendMsg(streamToolMsg{tool: tc.Tool, input: tc.Input, output: tc.Output, durationMs: tc.DurationMs})
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
	// Non-interactive mode: -p "message" or positional args with -p
	if chatPrintMode && len(args) > 0 {
		return runNonInteractive(strings.Join(args, " "))
	}

	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ " + err.Error()))
		fmt.Println()
		return err
	}

	model := newChatModel(client)

	// Pass initial message if provided as positional args
	if len(args) > 0 {
		model.initialMessage = strings.Join(args, " ")
	}

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
