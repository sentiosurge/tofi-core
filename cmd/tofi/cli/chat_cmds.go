package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Flags for the parent chat command.
var (
	chatPrintMode bool // -p: non-interactive, print result to stdout
	chatContinue  bool // -c: continue last session
)

func init() {
	chatCmd.Flags().BoolVarP(&chatPrintMode, "print", "p", false, "non-interactive mode: send message and print response")
	chatCmd.Flags().BoolVarP(&chatContinue, "continue", "c", false, "continue the last session")

	chatCmd.AddCommand(chatHistoryCmd)
	chatCmd.AddCommand(chatModelCmd)
	chatCmd.AddCommand(chatNewCmd)
	chatCmd.AddCommand(chatSendCmd)
}

// --- tofi chat history ---

var chatHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "List chat sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		if err := client.ensureRunning(); err != nil {
			return err
		}

		scope := ""
		if chatAgentName != "" {
			scope = "agent:" + chatAgentName
		}

		var sessions []sessionIndex
		if err := client.get("/api/v1/chat/sessions?scope="+scope, &sessions); err != nil {
			return err
		}

		if len(sessions) == 0 {
			fmt.Println("No sessions found.")
			return nil
		}

		// Output as formatted table
		fmt.Printf("%-16s %-30s %-20s %8s %10s\n", "ID", "TITLE", "MODEL", "MSGS", "COST")
		fmt.Println(strings.Repeat("─", 90))
		for _, s := range sessions {
			title := s.Title
			if title == "" {
				title = "(untitled)"
			}
			titleRunes := []rune(title)
			if len(titleRunes) > 28 {
				title = string(titleRunes[:28]) + "…"
			}
			model := s.Model
			if len(model) > 18 {
				model = model[:18] + "…"
			}
			fmt.Printf("%-16s %-30s %-20s %8d %10s\n",
				s.ID, title, model, s.MessageCount, formatCost(s.TotalCost))
		}
		return nil
	},
}

// --- tofi chat model [name] ---

var chatModelCmd = &cobra.Command{
	Use:   "model [name]",
	Short: "View or set the session model",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		if err := client.ensureRunning(); err != nil {
			return err
		}

		sessionID, err := resolveLastSessionID(client)
		if err != nil {
			return err
		}

		if len(args) == 0 {
			// Show current model
			var resp struct {
				Model string `json:"Model"`
			}
			if err := client.get("/api/v1/chat/sessions/"+sessionID, &resp); err != nil {
				return err
			}
			if resp.Model == "" {
				resp.Model = "(default)"
			}
			fmt.Println(resp.Model)
			return nil
		}

		// Set model
		body, _ := json.Marshal(map[string]any{"model": args[0]})
		var resp struct {
			Model string `json:"model"`
		}
		if err := client.patch("/api/v1/chat/sessions/"+sessionID, bytes.NewReader(body), &resp); err != nil {
			return err
		}
		fmt.Printf("Model set to: %s\n", resp.Model)
		return nil
	},
}

// --- tofi chat new ---

var chatNewCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new chat session",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		if err := client.ensureRunning(); err != nil {
			return err
		}

		scope := ""
		if chatAgentName != "" {
			scope = "agent:" + chatAgentName
		}

		body, _ := json.Marshal(map[string]any{"scope": scope})
		var resp sessionInfo
		if err := client.post("/api/v1/chat/sessions", bytes.NewReader(body), &resp); err != nil {
			return err
		}
		fmt.Println(resp.ID)
		return nil
	},
}

// --- tofi chat send "message" ---

var chatSendCmd = &cobra.Command{
	Use:   "send [message]",
	Short: "Send a message non-interactively",
	Long:  "Send a message and print the response to stdout. For scripts and AI agents.",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		message := strings.Join(args, " ")
		return runNonInteractive(message)
	},
}

// --- Non-interactive mode ---

func runNonInteractive(message string) error {
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	// Resolve or create session
	sessionID, err := resolveOrCreateSession(client)
	if err != nil {
		return err
	}

	// Send message via SSE
	body, _ := json.Marshal(map[string]string{"message": message})
	req, err := http.NewRequest("POST",
		client.baseURL+"/api/v1/chat/sessions/"+sessionID+"/messages",
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	if client.token != "" {
		req.Header.Set("Authorization", "Bearer "+client.token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Timezone", time.Now().Location().String())

	// Use a long-timeout HTTP client for streaming
	streamClient := &http.Client{Timeout: 10 * time.Minute}
	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API error: %d", resp.StatusCode)
	}

	// Read SSE stream, print chunks to stdout
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	var finalResult string
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			eventType := strings.TrimPrefix(line, "event: ")
			// Read the data line
			if !scanner.Scan() {
				break
			}
			dataLine := scanner.Text()
			if !strings.HasPrefix(dataLine, "data: ") {
				continue
			}
			data := strings.TrimPrefix(dataLine, "data: ")

			switch eventType {
			case "chunk":
				var chunk sseChunk
				if json.Unmarshal([]byte(data), &chunk) == nil {
					fmt.Print(chunk.Delta)
				}
			case "done":
				var done sseDone
				if json.Unmarshal([]byte(data), &done) == nil {
					finalResult = done.Result
				}
			case "error":
				var sseErr sseError
				if json.Unmarshal([]byte(data), &sseErr) == nil {
					return fmt.Errorf("error: %s", sseErr.Error)
				}
			}
		}
	}

	// Ensure output ends with newline
	if finalResult != "" && !strings.HasSuffix(finalResult, "\n") {
		fmt.Println()
	}

	return scanner.Err()
}

// --- Helpers ---

// resolveLastSessionID finds the most recent session ID.
func resolveLastSessionID(client *apiClient) (string, error) {
	if chatSessionID != "" {
		return chatSessionID, nil
	}

	scope := ""
	if chatAgentName != "" {
		scope = "agent:" + chatAgentName
	}

	var sessions []sessionIndex
	if err := client.get("/api/v1/chat/sessions?scope="+scope, &sessions); err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		return "", fmt.Errorf("no sessions found — create one with: tofi chat new")
	}
	return sessions[0].ID, nil
}

// resolveOrCreateSession finds the last session or creates a new one.
func resolveOrCreateSession(client *apiClient) (string, error) {
	if chatSessionID != "" {
		return chatSessionID, nil
	}

	scope := ""
	if chatAgentName != "" {
		scope = "agent:" + chatAgentName
	}

	// Try to find existing session
	var sessions []sessionIndex
	if err := client.get("/api/v1/chat/sessions?scope="+scope, &sessions); err == nil && len(sessions) > 0 {
		return sessions[0].ID, nil
	}

	// Create new session
	body, _ := json.Marshal(map[string]any{"scope": scope})
	var resp sessionInfo
	if err := client.post("/api/v1/chat/sessions", bytes.NewReader(body), &resp); err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	return resp.ID, nil
}

func formatCost(cost float64) string {
	if cost == 0 {
		return "-"
	}
	return fmt.Sprintf("$%.4f", cost)
}
