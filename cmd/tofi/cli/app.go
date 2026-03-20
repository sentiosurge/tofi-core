package cli

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

var appCmd = &cobra.Command{
	Use:   "app",
	Short: "Manage AI apps",
	Long:  "Interactive app management. Run without subcommands for the TUI.",
	RunE:  runAppTUI,
}

func runAppTUI(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ " + err.Error()))
		fmt.Println()
		return err
	}

	model := newAppModel(client)
	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		return err
	}

	// Post-exit: launch chat if requested
	if model.launchChat {
		// Set the package-level vars that chat command reads
		chatSessionID = model.launchChatSession
		if scope := model.launchChatScope; scope != "" {
			// scope is "agent:xxx", chatAgentName expects just "xxx"
			chatAgentName = strings.TrimPrefix(scope, "agent:")
		}
		// If no specific session to resume, force a new session
		if chatSessionID == "" {
			chatForceNew = true
		}
		// Pass initial message if set (e.g. from Edit action)
		if model.launchChatMessage != "" {
			chatInitMessage = model.launchChatMessage
		}
		// Pass skills to pre-load into the session
		if len(model.launchChatSkills) > 0 {
			chatInitSkills = model.launchChatSkills
		}
		return runChat(cmd, nil)
	}

	return nil
}

func init() {
	rootCmd.AddCommand(appCmd)
}
