package cli

import (
	"fmt"

	"tofi-core/internal/daemon"

	"github.com/spf13/cobra"
)

var stopForce bool

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the Tofi engine",
	Long:  "Send a graceful shutdown signal to the running engine. Use --force to kill immediately.",
	RunE:  runStop,
}

func init() {
	stopCmd.Flags().BoolVar(&stopForce, "force", false, "force kill immediately")
	rootCmd.AddCommand(stopCmd)
}

func runStop(cmd *cobra.Command, args []string) error {
	fmt.Println()

	status := daemon.GetStatus(homeDir, startPort)
	if !status.Running {
		fmt.Println(subtitleStyle.Render("  Engine is not running."))
		fmt.Println()
		return nil
	}

	if stopForce {
		fmt.Printf("  %s Force stopping engine (pid %d)...\n", accentStyle.Render("●"), status.PID)
	} else {
		fmt.Printf("  %s Stopping engine (pid %d)...\n", accentStyle.Render("●"), status.PID)
	}

	if err := daemon.Stop(homeDir, stopForce); err != nil {
		fmt.Printf("  %s %s\n\n", errorStyle.Render("✗"), err.Error())
		return err
	}

	fmt.Printf("  %s Engine stopped\n\n", successStyle.Render("✓"))
	return nil
}
