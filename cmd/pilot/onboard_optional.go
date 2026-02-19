package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/alekspetrov/pilot/internal/autopilot"
	"github.com/alekspetrov/pilot/internal/config"
)

// onboardOptionalSetup handles the automation/optional features stage.
// For Solo persona: skips entirely (returns nil immediately).
// For Team/Enterprise: shows autopilot, alerts, and daily brief sections.
func onboardOptionalSetup(state *OnboardState) error {
	// Solo persona: skip entirely
	if state.Persona == PersonaSolo {
		return nil
	}

	printStageHeader("AUTOMATION", state.CurrentStage, state.StagesTotal)
	fmt.Println()

	// Section 1: Autopilot
	if err := onboardAutopilot(state); err != nil {
		return err
	}

	// Section 2: Alerts
	if err := onboardAlerts(state); err != nil {
		return err
	}

	// Section 3: Daily Brief
	if err := onboardDailyBrief(state); err != nil {
		return err
	}

	printStageFooter()
	return nil
}

// onboardAutopilot configures autopilot settings.
func onboardAutopilot(state *OnboardState) error {
	cfg := state.Config
	reader := state.Reader

	fmt.Println("    Autopilot auto-merges PRs when CI passes.")
	fmt.Print("    Enable? [y/N] ")
	if !readYesNo(reader, false) {
		return nil
	}

	// Initialize autopilot config if needed
	if cfg.Orchestrator == nil {
		cfg.Orchestrator = &config.OrchestratorConfig{
			Model:         "claude-sonnet-4-6",
			MaxConcurrent: 2,
		}
	}
	if cfg.Orchestrator.Autopilot == nil {
		cfg.Orchestrator.Autopilot = autopilot.DefaultConfig()
	}

	cfg.Orchestrator.Autopilot.Enabled = true
	cfg.Orchestrator.Autopilot.AutoMerge = true
	cfg.Orchestrator.Autopilot.MergeMethod = "squash"

	// Ask how many environments user wants
	fmt.Println()
	envCountOptions := []string{
		"Single pipeline",
		"Dev + Production",
		"Dev + Staging + Production",
	}
	envChoice := selectOption(reader, "    How many environments?", envCountOptions)

	// Initialize environments map if needed
	if cfg.Orchestrator.Autopilot.Environments == nil {
		cfg.Orchestrator.Autopilot.Environments = make(map[string]*autopilot.EnvironmentConfig)
	}

	// Configure environment(s) based on choice
	switch envChoice {
	case 1:
		// Single pipeline: staging/main
		envName := readLineWithDefault(reader, "Environment name", "staging")

		branch := readLineWithDefault(reader, "Target branch", "main")

		fmt.Print("    Require approval? [y/N] ")
		requireApproval := readYesNo(reader, false)

		ciTimeoutStr := readLineWithDefault(reader, "CI timeout", "30m")

		// Parse timeout
		ciTimeout, _ := time.ParseDuration(ciTimeoutStr)
		if ciTimeout == 0 {
			ciTimeout = 30 * time.Minute
		}

		// Post-merge action
		fmt.Println("    After merge:")
		fmt.Println("      1. Nothing")
		fmt.Println("      2. Create tag")
		fmt.Println("      3. Webhook")
		postMergeChoice := selectOption(reader, "    Action:", []string{"Nothing", "Create tag", "Webhook"})

		postMergeAction := "none"
		switch postMergeChoice {
		case 2:
			postMergeAction = "tag"
		case 3:
			postMergeAction = "webhook"
		}

		cfg.Orchestrator.Autopilot.Environments[envName] = &autopilot.EnvironmentConfig{
			Branch:          branch,
			RequireApproval: requireApproval,
			CITimeout:       ciTimeout,
			SkipPostMergeCI: false,
			PostMerge: &autopilot.PostMergeConfig{
				Action: postMergeAction,
			},
		}
		cfg.Orchestrator.Autopilot.Environment = autopilot.Environment(envName)
		fmt.Printf("    Autopilot: %s\n", envName)

	case 2:
		// Dev + Production
		// Dev environment
		devCfg := &autopilot.EnvironmentConfig{
			Branch:          "main",
			RequireApproval: false,
			CITimeout:       5 * time.Minute,
			SkipPostMergeCI: true,
			PostMerge: &autopilot.PostMergeConfig{
				Action: "none",
			},
		}
		cfg.Orchestrator.Autopilot.Environments["dev"] = devCfg

		// Production environment
		prodBranch := readLineWithDefault(reader, "Production branch", "main")

		fmt.Print("    Require approval for prod? [Y/n] ")
		requireApproval := readYesNo(reader, true)

		fmt.Println("    After merge:")
		fmt.Println("      1. Nothing")
		fmt.Println("      2. Create tag")
		fmt.Println("      3. Webhook")
		postMergeChoice := selectOption(reader, "    Action:", []string{"Nothing", "Create tag", "Webhook"})

		postMergeAction := "none"
		switch postMergeChoice {
		case 2:
			postMergeAction = "tag"
		case 3:
			postMergeAction = "webhook"
		}

		prodCfg := &autopilot.EnvironmentConfig{
			Branch:          prodBranch,
			RequireApproval: requireApproval,
			ApprovalSource:  autopilot.ApprovalSourceTelegram,
			CITimeout:       30 * time.Minute,
			SkipPostMergeCI: false,
			PostMerge: &autopilot.PostMergeConfig{
				Action: postMergeAction,
			},
		}
		cfg.Orchestrator.Autopilot.Environments["prod"] = prodCfg
		cfg.Orchestrator.Autopilot.Environment = autopilot.EnvDev

		fmt.Println("    Autopilot: dev + prod")

	default:
		// Dev + Staging + Production (all three)
		devCfg := &autopilot.EnvironmentConfig{
			Branch:          "develop",
			RequireApproval: false,
			CITimeout:       5 * time.Minute,
			SkipPostMergeCI: true,
			PostMerge: &autopilot.PostMergeConfig{
				Action: "none",
			},
		}
		cfg.Orchestrator.Autopilot.Environments["dev"] = devCfg

		stagingCfg := &autopilot.EnvironmentConfig{
			Branch:          "staging",
			RequireApproval: false,
			CITimeout:       30 * time.Minute,
			SkipPostMergeCI: false,
			PostMerge: &autopilot.PostMergeConfig{
				Action: "none",
			},
		}
		cfg.Orchestrator.Autopilot.Environments["staging"] = stagingCfg

		prodCfg := &autopilot.EnvironmentConfig{
			Branch:          "main",
			RequireApproval: true,
			ApprovalSource:  autopilot.ApprovalSourceTelegram,
			CITimeout:       30 * time.Minute,
			SkipPostMergeCI: false,
			PostMerge: &autopilot.PostMergeConfig{
				Action: "tag",
			},
		}
		cfg.Orchestrator.Autopilot.Environments["prod"] = prodCfg
		cfg.Orchestrator.Autopilot.Environment = autopilot.EnvDev

		fmt.Println("    Autopilot: dev + staging + prod")
	}

	return nil
}

// onboardAlerts configures failure alerts.
func onboardAlerts(state *OnboardState) error {
	cfg := state.Config
	reader := state.Reader

	fmt.Print("\n    Enable failure alerts? [Y/n] ")
	if !readYesNo(reader, true) {
		return nil
	}

	// Initialize alerts config with basic settings
	// Rules will use defaults from config.DefaultConfig() when empty
	if cfg.Alerts == nil {
		cfg.Alerts = &config.AlertsConfig{
			Enabled:  true,
			Channels: []config.AlertChannelConfig{},
			Rules:    []config.AlertRuleConfig{}, // Will use defaults
			Defaults: config.AlertDefaultsConfig{
				DefaultSeverity:    "warning",
				SuppressDuplicates: true,
			},
		}
	}
	cfg.Alerts.Enabled = true

	// Auto-route to configured notification channel
	if cfg.Adapters != nil && cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled {
		fmt.Printf("    Alerts -> Slack %s\n", cfg.Adapters.Slack.Channel)
	} else if cfg.Adapters != nil && cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled {
		fmt.Printf("    Alerts -> Telegram %s\n", cfg.Adapters.Telegram.ChatID)
	} else {
		fmt.Println("    Alerts enabled (configure destination in config.yaml)")
	}

	return nil
}

// onboardDailyBrief configures daily brief settings.
func onboardDailyBrief(state *OnboardState) error {
	cfg := state.Config
	reader := state.Reader

	fmt.Print("\n    Daily brief? [y/N] ")
	if !readYesNo(reader, false) {
		return nil
	}

	// Initialize daily brief config
	if cfg.Orchestrator == nil {
		cfg.Orchestrator = &config.OrchestratorConfig{
			Model:         "claude-sonnet-4-6",
			MaxConcurrent: 2,
		}
	}
	if cfg.Orchestrator.DailyBrief == nil {
		cfg.Orchestrator.DailyBrief = &config.DailyBriefConfig{
			Channels: []config.BriefChannelConfig{},
			Content: config.BriefContentConfig{
				IncludeMetrics:     true,
				IncludeErrors:      true,
				MaxItemsPerSection: 10,
			},
			Filters: config.BriefFilterConfig{
				Projects: []string{},
			},
		}
	}

	// Prompt for time
	fmt.Print("    Time [9:00] > ")
	timeStr := readLine(reader)
	if timeStr == "" {
		timeStr = "9:00"
	}

	// Parse time into cron format
	schedule := parseTimeToCron(timeStr)

	// Prompt for timezone
	defaultTZ := "America/New_York"
	fmt.Printf("    Timezone [%s] > ", defaultTZ)
	tz := readLine(reader)
	if tz == "" {
		tz = defaultTZ
	}

	cfg.Orchestrator.DailyBrief.Enabled = true
	cfg.Orchestrator.DailyBrief.Schedule = schedule
	cfg.Orchestrator.DailyBrief.Timezone = tz

	// Route to configured notification channel
	if cfg.Adapters != nil && cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled {
		cfg.Orchestrator.DailyBrief.Channels = []config.BriefChannelConfig{
			{
				Type:    "slack",
				Channel: cfg.Adapters.Slack.Channel,
			},
		}
	} else if cfg.Adapters != nil && cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled {
		cfg.Orchestrator.DailyBrief.Channels = []config.BriefChannelConfig{
			{
				Type:    "telegram",
				Channel: cfg.Adapters.Telegram.ChatID,
			},
		}
	}

	fmt.Printf("    Brief at %s %s\n", timeStr, tz)
	return nil
}

// parseTimeToCron converts "HH:MM" to cron format "M H * * 1-5" (weekdays).
func parseTimeToCron(timeStr string) string {
	hour := "9"
	minute := "0"

	parts := strings.Split(timeStr, ":")
	if len(parts) >= 1 {
		hour = strings.TrimLeft(parts[0], "0")
		if hour == "" {
			hour = "0"
		}
	}
	if len(parts) >= 2 {
		minute = strings.TrimLeft(parts[1], "0")
		if minute == "" {
			minute = "0"
		}
	}

	return fmt.Sprintf("%s %s * * 1-5", minute, hour)
}

