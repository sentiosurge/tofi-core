package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version info — set via ldflags at build time.
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version info",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(titleStyle.Render("Tofi") + " " + subtitleStyle.Render(Version))
		if verbose {
			fmt.Println(subtitleStyle.Render("  commit: " + GitCommit))
			fmt.Println(subtitleStyle.Render("  built:  " + BuildTime))
		}
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
