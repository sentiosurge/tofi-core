package cli

import (
	"github.com/spf13/cobra"
)

var appCmd = &cobra.Command{
	Use:   "app",
	Short: "Manage AI agents",
	Long:  "Create, list, show, edit, run, and delete AI agents.",
}

func init() {
	rootCmd.AddCommand(appCmd)
}
