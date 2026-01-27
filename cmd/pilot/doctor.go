package main

import (
	"fmt"
	"strings"

	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/health"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	var verbose bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check system health and configuration",
		Long: `Run health checks on system dependencies, configuration, and features.

Shows what's working, what's missing, and how to fix issues.

Examples:
  pilot doctor           # Run all checks
  pilot doctor --verbose # Show detailed output`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			cfg, err := loadConfig()
			if err != nil {
				cfg = config.DefaultConfig()
			}

			// Run health checks
			report := health.RunChecks(cfg)

			// Header
			fmt.Println()
			fmt.Println("Pilot Health Check")
			fmt.Println("==================")
			fmt.Println()

			// Dependencies
			fmt.Println("System Dependencies:")
			for _, d := range report.Dependencies {
				symbol := d.Status.ColorSymbol()
				fmt.Printf("  %s %-12s %s\n", symbol, d.Name, d.Message)
				if verbose && d.Fix != "" && d.Status != health.StatusOK {
					fmt.Printf("                    → %s\n", d.Fix)
				}
			}
			fmt.Println()

			// Configuration
			fmt.Println("Configuration:")
			for _, c := range report.Config {
				symbol := c.Status.ColorSymbol()
				fmt.Printf("  %s %-24s %s\n", symbol, c.Name, c.Message)
				if verbose && c.Fix != "" && c.Status != health.StatusOK {
					fmt.Printf("                              → %s\n", c.Fix)
				}
			}
			fmt.Println()

			// Features
			fmt.Println("Features Status:")
			for _, f := range report.Features {
				symbol := f.Status.ColorSymbol()
				note := ""
				if f.Note != "" {
					note = " (" + f.Note + ")"
				}
				fmt.Printf("  %s %-16s%s\n", symbol, f.Name, note)
				if verbose && len(f.Missing) > 0 {
					fmt.Printf("                     missing: %s\n", strings.Join(f.Missing, ", "))
				}
			}
			fmt.Println()

			// Summary and recommendations
			errors, warnings := report.Summary()
			if errors > 0 || warnings > 0 {
				fmt.Println("Recommendations:")
				shown := 0
				maxRecs := 5

				// Show critical fixes first
				for _, d := range report.Dependencies {
					if d.Status == health.StatusError && d.Fix != "" && shown < maxRecs {
						fmt.Printf("  %d. %s: %s\n", shown+1, d.Name, d.Fix)
						shown++
					}
				}
				for _, c := range report.Config {
					if c.Status == health.StatusError && c.Fix != "" && shown < maxRecs {
						fmt.Printf("  %d. %s: %s\n", shown+1, c.Name, c.Fix)
						shown++
					}
				}

				// Then warnings
				for _, d := range report.Dependencies {
					if d.Status == health.StatusWarning && d.Fix != "" && shown < maxRecs {
						fmt.Printf("  %d. %s: %s\n", shown+1, d.Name, d.Fix)
						shown++
					}
				}
				for _, c := range report.Config {
					if c.Status == health.StatusWarning && c.Fix != "" && shown < maxRecs {
						fmt.Printf("  %d. %s: %s\n", shown+1, c.Name, c.Fix)
						shown++
					}
				}

				fmt.Println()
			}

			// Final status
			if report.ReadyToStart() {
				if errors == 0 && warnings == 0 {
					fmt.Println("✅ All systems operational!")
				} else if errors == 0 {
					fmt.Printf("✅ Ready to start (%d warning(s))\n", warnings)
				} else {
					fmt.Printf("⚠️  Ready with issues (%d error(s), %d warning(s))\n", errors, warnings)
				}
			} else {
				fmt.Printf("❌ Not ready - %d critical error(s)\n", errors)
				fmt.Println("   Fix required dependencies before running Pilot")
			}
			fmt.Println()

			// Helpful next steps
			fmt.Println("Run 'pilot setup' for interactive configuration wizard")

			return nil
		},
	}

	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show detailed output with fix suggestions")

	return cmd
}
