package cli

import (
	"fmt"

	"tofi-core/internal/doctor"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var doctorFix bool

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check system health and configuration",
	Long:  "Run diagnostics on your Tofi setup. Use --fix to auto-repair fixable issues.",
	RunE:  runDoctor,
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorFix, "fix", false, "auto-fix all fixable issues")
	rootCmd.AddCommand(doctorCmd)
}

var warnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffa657"))

func runDoctor(cmd *cobra.Command, args []string) error {
	fmt.Println()
	fmt.Println(titleStyle.Render("  Tofi Doctor"))
	fmt.Println(subtitleStyle.Render("  Checking your setup...\n"))

	report := doctor.Run(doctor.Options{
		HomeDir: homeDir,
	})

	// Render results grouped by category
	var currentCategory string
	var okCount, warnCount, failCount, fixableCount int

	for _, r := range report.Results {
		// Section header
		if r.Category != currentCategory {
			currentCategory = r.Category
			fmt.Printf("\n  %s\n", titleStyle.Render(currentCategory))
		}

		// Icon
		var icon string
		switch r.Severity {
		case doctor.SeverityOK:
			icon = successStyle.Render("✓")
			okCount++
		case doctor.SeverityWarn:
			icon = warnStyle.Render("⚠")
			warnCount++
		case doctor.SeverityFail:
			icon = errorStyle.Render("✗")
			failCount++
		case doctor.SeverityInfo:
			icon = accentStyle.Render("●")
		}

		// Label + detail
		line := fmt.Sprintf("  %s %-24s", icon, r.Label)
		if r.Detail != "" {
			var detailStyled string
			switch r.Severity {
			case doctor.SeverityWarn:
				detailStyled = warnStyle.Render(r.Detail)
			case doctor.SeverityFail:
				detailStyled = errorStyle.Render(r.Detail)
			default:
				detailStyled = subtitleStyle.Render(r.Detail)
			}
			line += " " + detailStyled
		}
		if r.Fixable {
			line += subtitleStyle.Render(" [fixable]")
			fixableCount++
		}
		fmt.Println(line)
	}

	fmt.Println()

	// Run fixes if --fix
	var fixedCount, fixFailedCount int
	if doctorFix && fixableCount > 0 {
		fmt.Println(titleStyle.Render("  Fixing..."))
		fmt.Println()

		fixResults := doctor.Fix(report)
		for _, fr := range fixResults {
			if fr.Fixed {
				fixedCount++
				fmt.Printf("  %s %-24s %s\n", successStyle.Render("✓"), fr.Label, subtitleStyle.Render("fixed"))
			} else {
				fixFailedCount++
				fmt.Printf("  %s %-24s %s\n", errorStyle.Render("✗"), fr.Label, errorStyle.Render(fr.Error))
			}
		}
		fmt.Println()
	} else if fixableCount > 0 && !doctorFix {
		fmt.Printf("  %s %d fixable issue(s) found. Run %s to repair.\n",
			warnStyle.Render("⚠"),
			fixableCount,
			accentStyle.Render("tofi doctor --fix"),
		)
		fmt.Println()
	}

	// Summary — subtract successfully fixed items from warn/fail counts
	effectiveOK := okCount + fixedCount
	effectiveWarn := warnCount - fixedCount
	if effectiveWarn < 0 {
		effectiveWarn = 0
	}
	effectiveFail := failCount + fixFailedCount

	summary := fmt.Sprintf("  %s %d passed", successStyle.Render("●"), effectiveOK)
	if fixedCount > 0 {
		summary += fmt.Sprintf(" (%d fixed)", fixedCount)
	}
	if effectiveWarn > 0 {
		summary += fmt.Sprintf("  %s %d warnings", warnStyle.Render("●"), effectiveWarn)
	}
	if effectiveFail > 0 {
		summary += fmt.Sprintf("  %s %d failures", errorStyle.Render("●"), effectiveFail)
	}
	fmt.Println(summary)
	fmt.Println()

	return nil
}
