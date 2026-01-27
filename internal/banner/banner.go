package banner

import (
	"fmt"
	"strings"

	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/health"
)

// Logo is the ASCII art logo for Pilot
const Logo = `
   ██████╗ ██╗██╗      ██████╗ ████████╗
   ██╔══██╗██║██║     ██╔═══██╗╚══██╔══╝
   ██████╔╝██║██║     ██║   ██║   ██║
   ██╔═══╝ ██║██║     ██║   ██║   ██║
   ██║     ██║███████╗╚██████╔╝   ██║
   ╚═╝     ╚═╝╚══════╝ ╚═════╝    ╚═╝
`

// Tagline is the project tagline
const Tagline = "AI That Ships Your Tickets"

// Print prints the banner with tagline
func Print() {
	fmt.Print(Logo)
	fmt.Printf("   %s\n\n", Tagline)
}

// PrintWithVersion prints the banner with version info
func PrintWithVersion(version string) {
	fmt.Print(Logo)
	fmt.Printf("   %s\n", Tagline)
	fmt.Printf("   v%s\n\n", version)
}

// PrintCompact prints a compact single-line banner
func PrintCompact() {
	fmt.Println("🚀 Pilot - AI That Ships Your Tickets")
}

// StartupBanner prints the full startup banner
func StartupBanner(version, gateway string) {
	fmt.Print(Logo)
	fmt.Printf("   %s\n", Tagline)
	fmt.Println()
	fmt.Printf("   Version:  v%s\n", version)
	fmt.Printf("   Gateway:  %s\n", gateway)
	fmt.Println()
}

// StartupWithHealth prints startup banner with health status
func StartupWithHealth(version string, cfg *config.Config) {
	report := health.RunChecks(cfg)

	// Header
	fmt.Println()
	fmt.Printf("PILOT v%s\n", version)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	// Features in compact grid
	features := report.Features
	cols := 3
	colWidth := 14

	for i, f := range features {
		symbol := f.Status.Symbol()
		name := f.Name
		if f.Note != "" {
			name = f.Name + "*"
		}
		fmt.Printf("%s %-*s", symbol, colWidth-2, name)
		if (i+1)%cols == 0 || i == len(features)-1 {
			fmt.Println()
		}
	}

	// Notes for warnings
	hasNotes := false
	for _, f := range features {
		if f.Note != "" {
			if !hasNotes {
				fmt.Println()
				hasNotes = true
			}
			fmt.Printf("  * %s: %s\n", f.Name, f.Note)
		}
	}

	// Projects
	if report.Projects > 0 {
		fmt.Println()
		fmt.Printf("Projects: %d configured\n", report.Projects)
	}

	fmt.Println()
}

// StartupTelegram prints telegram-specific startup with health
func StartupTelegram(version, project, chatID string, cfg *config.Config) {
	report := health.RunChecks(cfg)

	// ASCII logo
	fmt.Print(Logo)
	fmt.Printf("   %s\n", Tagline)
	fmt.Printf("   v%s │ Telegram Bot\n", version)
	fmt.Println()

	// Health check section
	fmt.Println("Checking dependencies...")
	for _, d := range report.Dependencies {
		symbol := d.Status.Symbol()
		switch d.Status {
		case health.StatusOK:
			fmt.Printf("  %s %s %s\n", symbol, d.Name, d.Message)
		case health.StatusWarning, health.StatusError:
			fmt.Printf("  %s %s %s\n", symbol, d.Name, d.Message)
			if d.Fix != "" {
				fmt.Printf("    → %s\n", d.Fix)
			}
		default:
			fmt.Printf("  %s %s %s\n", symbol, d.Name, d.Message)
		}
	}

	// Config issues (only show problems)
	hasConfigIssues := false
	for _, c := range report.Config {
		if c.Status != health.StatusOK {
			if !hasConfigIssues {
				fmt.Println()
				fmt.Println("Configuration issues:")
				hasConfigIssues = true
			}
			symbol := c.Status.Symbol()
			fmt.Printf("  %s %s: %s\n", symbol, c.Name, c.Message)
			if c.Fix != "" {
				fmt.Printf("    → %s\n", c.Fix)
			}
		}
	}

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Features inline
	fmt.Println("Features:")
	var enabled []string
	var disabled []string
	var degraded []string

	for _, f := range report.Features {
		switch f.Status {
		case health.StatusOK:
			enabled = append(enabled, f.Name)
		case health.StatusWarning:
			if f.Degraded {
				degraded = append(degraded, fmt.Sprintf("%s (%s)", f.Name, f.Note))
			} else if len(f.Missing) > 0 {
				disabled = append(disabled, fmt.Sprintf("%s (missing: %s)", f.Name, strings.Join(f.Missing, ", ")))
			} else if f.Note != "" {
				degraded = append(degraded, fmt.Sprintf("%s (%s)", f.Name, f.Note))
			}
		case health.StatusDisabled:
			disabled = append(disabled, f.Name)
		case health.StatusError:
			disabled = append(disabled, fmt.Sprintf("%s (missing: %s)", f.Name, strings.Join(f.Missing, ", ")))
		}
	}

	if len(enabled) > 0 {
		fmt.Printf("  ✓ %s\n", strings.Join(enabled, ", "))
	}
	if len(degraded) > 0 {
		fmt.Printf("  ○ %s\n", strings.Join(degraded, ", "))
	}
	if len(disabled) > 0 {
		fmt.Printf("  · %s\n", strings.Join(disabled, ", "))
	}

	fmt.Println()
	fmt.Printf("Project: %s\n", project)
	if chatID != "" {
		fmt.Printf("Chat ID: %s\n", chatID)
	}
	fmt.Println()

	// Ready status
	if !report.ReadyToStart() {
		fmt.Println("❌ Cannot start - missing critical dependencies")
		fmt.Println("   Run 'pilot doctor' for details")
		fmt.Println()
	} else if report.HasWarnings {
		fmt.Println("⚠️  Starting with warnings - some features limited")
		fmt.Println("   Run 'pilot doctor' for details")
		fmt.Println()
	}

	fmt.Println("Listening... (Ctrl+C to stop)")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
}

// StartupServer prints server-specific startup with health
func StartupServer(version, gateway string, cfg *config.Config) {
	report := health.RunChecks(cfg)

	// Header
	fmt.Print(Logo)
	fmt.Printf("   %s\n", Tagline)
	fmt.Printf("   v%s │ Server\n", version)
	fmt.Println()

	// Quick health summary
	fmt.Println("Checking dependencies...")
	errCount := 0
	warnCount := 0
	for _, d := range report.Dependencies {
		switch d.Status {
		case health.StatusError:
			errCount++
			fmt.Printf("  ✗ %s: %s\n", d.Name, d.Message)
			if d.Fix != "" {
				fmt.Printf("    → %s\n", d.Fix)
			}
		case health.StatusWarning:
			warnCount++
		}
	}

	if errCount == 0 {
		fmt.Println("  ✓ All dependencies OK")
		if warnCount > 0 {
			fmt.Printf("  ○ %d optional dependencies missing\n", warnCount)
		}
	}

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Features
	fmt.Println("Features:")
	for _, f := range report.Features {
		symbol := f.Status.Symbol()
		note := ""
		if f.Note != "" {
			note = " (" + f.Note + ")"
		}
		fmt.Printf("  %s %s%s\n", symbol, f.Name, note)
	}

	fmt.Println()
	fmt.Printf("Gateway: %s\n", gateway)
	fmt.Printf("Projects: %d configured\n", report.Projects)
	fmt.Println()

	if !report.ReadyToStart() {
		fmt.Println("❌ Cannot start - fix critical errors first")
		fmt.Println("   Run 'pilot doctor' for details")
	} else {
		fmt.Println("Ready to receive tasks")
	}

	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
}
