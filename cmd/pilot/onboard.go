package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/alekspetrov/pilot/internal/banner"
	"github.com/alekspetrov/pilot/internal/config"
)

// Persona represents the user's workflow persona
type Persona int

const (
	PersonaSolo Persona = iota + 1
	PersonaTeam
	PersonaEnterprise
)

// String returns the persona display name
func (p Persona) String() string {
	switch p {
	case PersonaSolo:
		return "Solo"
	case PersonaTeam:
		return "Team"
	case PersonaEnterprise:
		return "Enterprise"
	default:
		return "Unknown"
	}
}

// OnboardState holds the state during onboarding wizard
type OnboardState struct {
	Persona      Persona
	Config       *config.Config
	Reader       *bufio.Reader
	StagesTotal  int
	CurrentStage int
}

// Card dimensions for summary cards (21 chars wide, 3 columns)
const (
	summaryCardWidth      = 21
	summaryCardInnerWidth = 17 // cardWidth - 4 (borders)
)

func newOnboardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "onboard",
		Short: "Interactive onboarding wizard",
		Long: `Interactive wizard to configure Pilot for your workflow.

Guides you through:
  - Persona selection (Solo, Team, Enterprise)
  - Project setup
  - Ticket source configuration
  - Notification settings
  - Optional features (for Team/Enterprise)

Examples:
  pilot onboard    # Start interactive onboarding`,
		RunE: runOnboard,
	}

	return cmd
}

func runOnboard(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)

	// Load existing config or create new
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		cfg = config.DefaultConfig()
	}

	// Print welcome banner
	fmt.Println()
	banner.PrintWithVersion(version)

	// Check current configuration status
	hasProjects := len(cfg.Projects) > 0
	hasTickets := hasTicketSource(cfg)
	hasNotify := hasNotificationChannel(cfg)

	// Show current status if config exists
	if hasProjects || hasTickets || hasNotify {
		printCurrentStatus(cfg, hasProjects, hasTickets, hasNotify)

		// If all configured, ask to reconfigure
		if hasProjects && hasTickets && hasNotify {
			fmt.Println()
			fmt.Print("  Reconfigure? [y/N]: ")
			if !readYesNo(reader, false) {
				fmt.Println()
				fmt.Println("  Run 'pilot start' to begin")
				return nil
			}
		}
	}

	// Persona selection
	fmt.Println()
	persona := selectPersona(reader)

	// Create onboard state
	state := &OnboardState{
		Persona: persona,
		Config:  cfg,
		Reader:  reader,
	}

	// Set stage count based on persona
	// Stages: Project, Backend, Tickets, Notify, [Optional for Team/Enterprise]
	switch persona {
	case PersonaSolo:
		state.StagesTotal = 5
	case PersonaTeam, PersonaEnterprise:
		state.StagesTotal = 6
	}

	// Execute stages
	state.CurrentStage = 1
	if err := onboardProjectSetup(state); err != nil {
		return err
	}

	state.CurrentStage = 2
	if err := onboardBackendSetup(state); err != nil {
		return err
	}

	state.CurrentStage = 3
	if err := onboardTicketSetup(state); err != nil {
		return err
	}

	state.CurrentStage = 4
	if err := onboardNotifySetup(state); err != nil {
		return err
	}

	// Optional setup for Team/Enterprise
	if persona == PersonaTeam || persona == PersonaEnterprise {
		state.CurrentStage = 5
		if err := onboardOptionalSetup(state); err != nil {
			return err
		}
	}

	// Save config
	configPath := config.DefaultConfigPath()
	if err := config.Save(cfg, configPath); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Print summary
	fmt.Println()
	printOnboardSummary(state)

	return nil
}

func selectPersona(reader *bufio.Reader) Persona {
	options := []string{
		"Solo Developer — Personal projects (5 stages)",
		"Team — Shared repos, Slack notifications (6 stages)",
		"Enterprise — Full automation, approvals (6 stages)",
	}

	idx := selectOption(reader, "Select your workflow:", options)

	switch idx {
	case 1:
		return PersonaSolo
	case 2:
		return PersonaTeam
	case 3:
		return PersonaEnterprise
	default:
		return PersonaSolo
	}
}

func hasTicketSource(cfg *config.Config) bool {
	if cfg.Adapters == nil {
		return false
	}
	if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled {
		return true
	}
	if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled {
		return true
	}
	if cfg.Adapters.Jira != nil && cfg.Adapters.Jira.Enabled {
		return true
	}
	if cfg.Adapters.Asana != nil && cfg.Adapters.Asana.Enabled {
		return true
	}
	return false
}

func hasNotificationChannel(cfg *config.Config) bool {
	if cfg.Adapters == nil {
		return false
	}
	if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled {
		return true
	}
	if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled {
		return true
	}
	return false
}

func printCurrentStatus(cfg *config.Config, hasProjects, hasTickets, hasNotify bool) {
	fmt.Println("  Current Configuration:")
	fmt.Println()

	if hasProjects {
		fmt.Printf("    %s Projects: %d configured\n",
			onboardSuccessStyle.Render("✓"),
			len(cfg.Projects))
	} else {
		fmt.Printf("    %s Projects: not configured\n",
			onboardDimStyle.Render("○"))
	}

	if hasTickets {
		source := getTicketSourceName(cfg)
		fmt.Printf("    %s Tickets: %s\n",
			onboardSuccessStyle.Render("✓"),
			source)
	} else {
		fmt.Printf("    %s Tickets: not configured\n",
			onboardDimStyle.Render("○"))
	}

	if hasNotify {
		channel := getNotifyChannelName(cfg)
		fmt.Printf("    %s Notifications: %s\n",
			onboardSuccessStyle.Render("✓"),
			channel)
	} else {
		fmt.Printf("    %s Notifications: not configured\n",
			onboardDimStyle.Render("○"))
	}
}

func getTicketSourceName(cfg *config.Config) string {
	var sources []string
	if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled {
		sources = append(sources, "GitHub")
	}
	if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled {
		sources = append(sources, "Linear")
	}
	if cfg.Adapters.Jira != nil && cfg.Adapters.Jira.Enabled {
		sources = append(sources, "Jira")
	}
	if cfg.Adapters.Asana != nil && cfg.Adapters.Asana.Enabled {
		sources = append(sources, "Asana")
	}
	return strings.Join(sources, ", ")
}

func getNotifyChannelName(cfg *config.Config) string {
	var channels []string
	if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled {
		channels = append(channels, "Telegram")
	}
	if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled {
		channels = append(channels, "Slack")
	}
	return strings.Join(channels, ", ")
}

// Note: onboardProjectSetup is implemented in onboard_project.go
// Note: onboardBackendSetup is implemented in onboard_backend.go
// Note: onboardTicketSetup is implemented in onboard_ticket.go
// Note: onboardNotifySetup is implemented in onboard_notify.go
// Note: onboardOptionalSetup is implemented in onboard_optional.go

// printOnboardSummary prints the final summary with 3-column cards
func printOnboardSummary(state *OnboardState) {
	cfg := state.Config

	printSectionDivider("SUMMARY")

	// Print summary cards in rows of 3
	cards := buildSummaryCards(cfg, state.Persona)

	for i := 0; i < len(cards); i += 3 {
		end := i + 3
		if end > len(cards) {
			end = len(cards)
		}
		printCardRow(cards[i:end])
		if end < len(cards) {
			fmt.Println()
		}
	}

	// Print "Get started" section
	fmt.Println()
	printSectionDivider("GET STARTED")

	printGetStartedCommands(cfg, state.Persona)
}

// SummaryCard represents a summary card
type SummaryCard struct {
	Title    string
	Value    string
	Line1    string
	Line2    string
	Configured bool
}

func buildSummaryCards(cfg *config.Config, persona Persona) []SummaryCard {
	cards := []SummaryCard{
		buildProjectCard(cfg),
		buildBackendCardFromConfig(cfg),
		buildTicketsCard(cfg),
		buildNotifyCard(cfg),
	}

	// Add additional cards for Team/Enterprise
	if persona == PersonaTeam || persona == PersonaEnterprise {
		cards = append(cards,
			buildPRsCard(cfg),
			buildAutopilotCard(cfg),
			buildBriefCard(cfg),
		)
	}

	return cards
}

// buildBackendCardFromConfig builds the summary card for backend selection
func buildBackendCardFromConfig(cfg *config.Config) SummaryCard {
	card := SummaryCard{Title: "BACKEND"}

	if cfg.Executor != nil && cfg.Executor.Type != "" {
		backendType := cfg.Executor.Type
		// Map type to display name
		switch backendType {
		case "claude-code":
			card.Value = "Claude Code"
		case "qwen-code":
			card.Value = "Qwen Code"
		case "opencode":
			card.Value = "OpenCode"
		default:
			card.Value = backendType
		}
		card.Configured = true
	} else {
		card.Value = "Claude Code"
		card.Line1 = "(default)"
		card.Configured = true
	}

	return card
}

func buildProjectCard(cfg *config.Config) SummaryCard {
	card := SummaryCard{Title: "PROJECT"}

	if len(cfg.Projects) > 0 {
		proj := cfg.Projects[0]
		card.Value = proj.Name
		if proj.GitHub != nil {
			card.Line1 = fmt.Sprintf("%s/%s", proj.GitHub.Owner, proj.GitHub.Repo)
		} else {
			card.Line1 = truncate(proj.Path, summaryCardInnerWidth)
		}
		if proj.Navigator {
			card.Line2 = "✓ Navigator"
		}
		card.Configured = true
	} else {
		card.Value = "—"
		card.Line1 = "not configured"
		card.Configured = false
	}

	return card
}

func buildTicketsCard(cfg *config.Config) SummaryCard {
	card := SummaryCard{Title: "TICKETS"}

	source := getTicketSourceName(cfg)
	if source != "" {
		card.Value = source
		if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled {
			label := "pilot"
			if cfg.Adapters.GitHub.PilotLabel != "" {
				label = cfg.Adapters.GitHub.PilotLabel
			}
			card.Line1 = fmt.Sprintf("label: %s", label)
			if cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled {
				card.Line2 = "polling: on"
			}
		}
		card.Configured = true
	} else {
		card.Value = "—"
		card.Line1 = "not configured"
		card.Configured = false
	}

	return card
}

func buildNotifyCard(cfg *config.Config) SummaryCard {
	card := SummaryCard{Title: "NOTIFY"}

	channel := getNotifyChannelName(cfg)
	if channel != "" {
		card.Value = channel
		if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled {
			if cfg.Adapters.Telegram.ChatID != "" {
				card.Line1 = fmt.Sprintf("chat: %s", cfg.Adapters.Telegram.ChatID)
			}
		}
		card.Configured = true
	} else {
		card.Value = "—"
		card.Line1 = "not configured"
		card.Configured = false
	}

	return card
}

func buildPRsCard(cfg *config.Config) SummaryCard {
	card := SummaryCard{Title: "PRS"}
	card.Value = "auto-create"
	card.Line1 = "self-review: on"
	card.Configured = true
	return card
}

func buildAutopilotCard(cfg *config.Config) SummaryCard {
	card := SummaryCard{Title: "AUTOPILOT"}

	if cfg.Orchestrator != nil && cfg.Orchestrator.Autopilot != nil {
		env := string(cfg.Orchestrator.Autopilot.Environment)
		if env == "" {
			env = "dev"
		}
		card.Value = env
		card.Line1 = "CI monitor: on"
		card.Configured = true
	} else {
		card.Value = "dev"
		card.Line1 = "CI monitor: off"
		card.Configured = false
	}

	return card
}

func buildBriefCard(cfg *config.Config) SummaryCard {
	card := SummaryCard{Title: "BRIEF"}

	if cfg.Orchestrator != nil && cfg.Orchestrator.DailyBrief != nil && cfg.Orchestrator.DailyBrief.Enabled {
		card.Value = "daily"
		card.Line1 = cfg.Orchestrator.DailyBrief.Schedule
		card.Configured = true
	} else {
		card.Value = "—"
		card.Line1 = "not configured"
		card.Configured = false
	}

	return card
}

func printCardRow(cards []SummaryCard) {
	// Print each card line by line
	// Line 1: top borders
	for i, card := range cards {
		fmt.Print(renderCardTopBorder(card.Title))
		if i < len(cards)-1 {
			fmt.Print(" ")
		}
	}
	fmt.Println()

	// Line 2: empty
	for i := range cards {
		fmt.Print(renderCardEmptyLine())
		if i < len(cards)-1 {
			fmt.Print(" ")
		}
	}
	fmt.Println()

	// Line 3: title and value
	for i, card := range cards {
		fmt.Print(renderCardTitleLine(card.Title, card.Value))
		if i < len(cards)-1 {
			fmt.Print(" ")
		}
	}
	fmt.Println()

	// Line 4: empty
	for i := range cards {
		fmt.Print(renderCardEmptyLine())
		if i < len(cards)-1 {
			fmt.Print(" ")
		}
	}
	fmt.Println()

	// Line 5: line1
	for i, card := range cards {
		fmt.Print(renderCardContentLine(card.Line1))
		if i < len(cards)-1 {
			fmt.Print(" ")
		}
	}
	fmt.Println()

	// Line 6: line2
	for i, card := range cards {
		fmt.Print(renderCardContentLine(card.Line2))
		if i < len(cards)-1 {
			fmt.Print(" ")
		}
	}
	fmt.Println()

	// Line 7: empty
	for i := range cards {
		fmt.Print(renderCardEmptyLine())
		if i < len(cards)-1 {
			fmt.Print(" ")
		}
	}
	fmt.Println()

	// Line 8: bottom border
	for i := range cards {
		fmt.Print(renderCardBottomBorder())
		if i < len(cards)-1 {
			fmt.Print(" ")
		}
	}
	fmt.Println()
}

func renderCardTopBorder(title string) string {
	// ╭───────────────────╮ (21 chars)
	dashCount := summaryCardWidth - 2
	return onboardBorderStyle.Render("╭" + strings.Repeat("─", dashCount) + "╮")
}

func renderCardBottomBorder() string {
	// ╰───────────────────╯ (21 chars)
	dashCount := summaryCardWidth - 2
	return onboardBorderStyle.Render("╰" + strings.Repeat("─", dashCount) + "╯")
}

func renderCardEmptyLine() string {
	// │                   │ (21 chars)
	spaceCount := summaryCardWidth - 2
	return onboardBorderStyle.Render("│") +
		strings.Repeat(" ", spaceCount) +
		onboardBorderStyle.Render("│")
}

func renderCardTitleLine(title, value string) string {
	// │  TITLE    value  │
	return onboardBorderStyle.Render("│") + " " +
		onboardLabelStyle.Render(title) + "  " +
		onboardValueStyle.Render(padRight(value, summaryCardInnerWidth-len(title)-4)) + " " +
		onboardBorderStyle.Render("│")
}

func renderCardContentLine(content string) string {
	// │  content...       │
	if content == "" {
		return renderCardEmptyLine()
	}
	truncated := truncate(content, summaryCardInnerWidth)
	padded := padRight("  "+truncated, summaryCardInnerWidth)
	return onboardBorderStyle.Render("│") + " " +
		onboardDimStyle.Render(padded) + " " +
		onboardBorderStyle.Render("│")
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s[:width]
	}
	return s + strings.Repeat(" ", width-len(s))
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func printGetStartedCommands(cfg *config.Config, persona Persona) {
	fmt.Println("  Start Pilot:")
	fmt.Println()

	var flags []string
	if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled {
		flags = append(flags, "--github")
	}
	if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled {
		flags = append(flags, "--linear")
	}
	if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled {
		flags = append(flags, "--telegram")
	}
	if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled {
		flags = append(flags, "--slack")
	}

	// Add autopilot for team/enterprise
	if persona == PersonaTeam || persona == PersonaEnterprise {
		flags = append(flags, "--env=stage")
	}

	cmd := "pilot start"
	if len(flags) > 0 {
		cmd = cmd + " " + strings.Join(flags, " ")
	}

	fmt.Printf("    %s\n", onboardValueStyle.Render(cmd))
	fmt.Println()

	// Suggested first commands
	fmt.Println("  Try these commands:")
	fmt.Println()
	fmt.Printf("    %s   # Check system health\n", onboardDimStyle.Render("pilot doctor"))
	fmt.Printf("    %s   # Check status\n", onboardDimStyle.Render("pilot status"))

	if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled {
		fmt.Printf("    %s   # List queued issues\n", onboardDimStyle.Render("gh issue list --label pilot"))
	}

	fmt.Println()
}
