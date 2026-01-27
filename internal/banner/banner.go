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

	// Compact header
	fmt.Println()
	fmt.Printf("PILOT v%s │ Telegram Bot\n", version)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Features inline
	var enabled []string
	var warnings []string
	for _, f := range report.Features {
		if f.Status == health.StatusOK {
			enabled = append(enabled, f.Name)
		} else if f.Status == health.StatusWarning {
			warnings = append(warnings, f.Name+"*")
		}
	}

	if len(enabled) > 0 {
		fmt.Printf("✓ %s\n", strings.Join(enabled, ", "))
	}
	if len(warnings) > 0 {
		fmt.Printf("○ %s\n", strings.Join(warnings, ", "))
	}

	fmt.Println()
	fmt.Printf("Project: %s\n", project)
	fmt.Printf("Chat ID: %s\n", chatID)
	fmt.Println()
	fmt.Println("Listening... (Ctrl+C to stop)")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
}
