package cli

import (
	"fmt"

	"tofi-core/internal/daemon"

	"github.com/spf13/cobra"
)

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the Tofi engine",
	Long:  "Stop the running engine and start it again.",
	RunE:  runRestart,
}

func init() {
	rootCmd.AddCommand(restartCmd)
}

func runRestart(cmd *cobra.Command, args []string) error {
	fmt.Println()

	status := daemon.GetStatus(homeDir, startPort)
	if status.Running {
		fmt.Printf("  %s Stopping engine (pid %d)...\n", accentStyle.Render("●"), status.PID)
		if err := daemon.Stop(homeDir, false); err != nil {
			fmt.Printf("  %s %s\n", errorStyle.Render("✗"), err.Error())
			return err
		}
		fmt.Printf("  %s Engine stopped\n", successStyle.Render("✓"))
	}

	// Delegate to start
	fmt.Printf("  %s Starting engine...\n", accentStyle.Render("●"))
	pid, err := daemon.Start(homeDir, startPort, false)
	if err != nil {
		fmt.Printf("  %s %s\n\n", errorStyle.Render("✗"), err.Error())
		return err
	}

	fmt.Printf("  %s Engine running       %s\n", successStyle.Render("✓"), subtitleStyle.Render(fmt.Sprintf("pid %d", pid)))
	fmt.Printf("  %s Listening on         %s\n\n", successStyle.Render("✓"), accentStyle.Render(fmt.Sprintf("http://localhost:%d", startPort)))
	return nil
}
