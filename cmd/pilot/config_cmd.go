package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/replay"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage Pilot configuration",
		Long:  `View, edit, and validate Pilot configuration.`,
	}

	cmd.AddCommand(
		newConfigShowCmd(),
		newConfigEditCmd(),
		newConfigValidateCmd(),
		newConfigPathCmd(),
	)

	return cmd
}

func newConfigShowCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			if outputJSON {
				data, err := json.MarshalIndent(cfg, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal config: %w", err)
				}
				fmt.Println(string(data))
				return nil
			}

			// YAML output
			data, err := yaml.Marshal(cfg)
			if err != nil {
				return fmt.Errorf("failed to marshal config: %w", err)
			}
			fmt.Print(string(data))

			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")

	return cmd
}

func newConfigEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open config in editor",
		Long: `Open the Pilot configuration file in your default editor.

Uses $EDITOR environment variable, falling back to:
  - vim (if available)
  - nano (if available)
  - vi (if available)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			// Check if config exists
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				fmt.Printf("Config file does not exist at %s\n", configPath)
				fmt.Println("Run 'pilot init' to create one.")
				return nil
			}

			// Find editor
			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = os.Getenv("VISUAL")
			}
			if editor == "" {
				// Try common editors
				for _, e := range []string{"vim", "nano", "vi"} {
					if _, err := exec.LookPath(e); err == nil {
						editor = e
						break
					}
				}
			}
			if editor == "" {
				return fmt.Errorf("no editor found. Set $EDITOR environment variable")
			}

			// Open editor
			editorCmd := exec.Command(editor, configPath)
			editorCmd.Stdin = os.Stdin
			editorCmd.Stdout = os.Stdout
			editorCmd.Stderr = os.Stderr

			if err := editorCmd.Run(); err != nil {
				return fmt.Errorf("editor exited with error: %w", err)
			}

			// Validate after editing
			fmt.Println()
			fmt.Println("Validating configuration...")

			cfg, err := config.Load(configPath)
			if err != nil {
				fmt.Printf("Warning: Failed to load config: %v\n", err)
				return nil
			}

			if err := cfg.Validate(); err != nil {
				fmt.Printf("Warning: Config validation failed: %v\n", err)
				return nil
			}

			fmt.Println("Configuration is valid!")

			return nil
		},
	}
}

func newConfigValidateCmd() *cobra.Command {
	var quiet bool

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration syntax",
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			// Check if file exists
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				if quiet {
					os.Exit(1)
				}
				return fmt.Errorf("config file does not exist: %s", configPath)
			}

			// Try to load
			cfg, err := config.Load(configPath)
			if err != nil {
				if quiet {
					os.Exit(1)
				}
				return fmt.Errorf("invalid YAML syntax: %w", err)
			}

			// Validate
			if err := cfg.Validate(); err != nil {
				if quiet {
					os.Exit(1)
				}
				return fmt.Errorf("validation failed: %w", err)
			}

			// Check for common issues
			var warnings []string

			// Check adapters
			if cfg.Adapters == nil {
				warnings = append(warnings, "No adapters configured")
			} else {
				hasAdapter := false
				if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled {
					hasAdapter = true
					if cfg.Adapters.Telegram.BotToken == "" {
						warnings = append(warnings, "Telegram enabled but bot_token not set")
					}
				}
				if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled {
					hasAdapter = true
				}
				if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled {
					hasAdapter = true
					if cfg.Adapters.Slack.BotToken == "" {
						warnings = append(warnings, "Slack enabled but bot_token not set")
					}
				}
				if cfg.Adapters.Github != nil && cfg.Adapters.Github.Enabled {
					hasAdapter = true
					if cfg.Adapters.Github.Token == "" && os.Getenv("GITHUB_TOKEN") == "" {
						warnings = append(warnings, "GitHub enabled but token not set")
					}
				}
				if !hasAdapter {
					warnings = append(warnings, "No adapters enabled")
				}
			}

			// Check projects
			if len(cfg.Projects) == 0 {
				warnings = append(warnings, "No projects configured")
			} else {
				for _, proj := range cfg.Projects {
					if _, err := os.Stat(proj.Path); os.IsNotExist(err) {
						warnings = append(warnings, fmt.Sprintf("Project path does not exist: %s", proj.Path))
					}
				}
			}

			if quiet {
				return nil
			}

			fmt.Printf("Config: %s\n", configPath)
			fmt.Println()
			fmt.Println("Syntax:     OK")
			fmt.Println("Validation: OK")
			fmt.Println()

			if len(warnings) > 0 {
				fmt.Println("Warnings:")
				for _, w := range warnings {
					fmt.Printf("  - %s\n", w)
				}
			} else {
				fmt.Println("No warnings.")
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Exit with code 1 on error, no output")

	return cmd
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Show config file path",
		Run: func(cmd *cobra.Command, args []string) {
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}
			fmt.Println(configPath)
		},
	}
}

// newLogsCmd creates the logs command for viewing task execution logs
func newLogsCmd() *cobra.Command {
	var (
		limit   int
		follow  bool
		verbose bool
		jsonOut bool
	)

	cmd := &cobra.Command{
		Use:   "logs [task-id]",
		Short: "View task execution logs",
		Long: `View logs from task executions.

Without arguments, shows recent task logs.
With a task ID, shows detailed logs for that specific task.

Examples:
  pilot logs              # Show recent task logs
  pilot logs TASK-12345   # Show logs for specific task
  pilot logs GH-15        # Show logs for GitHub issue task
  pilot logs --limit 20   # Show last 20 tasks`,
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// If task ID provided, show specific task logs
			if len(args) > 0 {
				return showTaskLogs(args[0], cfg, verbose, jsonOut)
			}

			// Otherwise, show recent logs
			return showRecentLogs(cfg, limit, jsonOut)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "Number of recent tasks to show")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output (not implemented)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show detailed output")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

func showTaskLogs(taskID string, cfg *config.Config, verbose, jsonOut bool) error {
	// Try to find recording by task ID
	recordingsPath := replay.DefaultRecordingsPath()

	// List all recordings and find matching task
	recordings, err := replay.ListRecordings(recordingsPath, &replay.RecordingFilter{Limit: 100})
	if err != nil {
		return fmt.Errorf("failed to list recordings: %w", err)
	}

	var matchingRec *replay.RecordingSummary
	for _, rec := range recordings {
		if rec.TaskID == taskID || rec.ID == taskID || strings.Contains(rec.TaskID, taskID) {
			matchingRec = rec
			break
		}
	}

	if matchingRec == nil {
		return fmt.Errorf("no logs found for task: %s", taskID)
	}

	// Load full recording
	recording, err := replay.LoadRecording(recordingsPath, matchingRec.ID)
	if err != nil {
		return fmt.Errorf("failed to load recording: %w", err)
	}

	if jsonOut {
		data, err := json.MarshalIndent(recording, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	// Display task info
	statusIcon := "+"
	switch recording.Status {
	case "failed":
		statusIcon = "x"
	case "cancelled":
		statusIcon = "!"
	}

	fmt.Printf("Task: %s [%s]\n", recording.TaskID, statusIcon)
	fmt.Printf("Status: %s\n", recording.Status)
	fmt.Printf("Duration: %s\n", recording.Duration)
	fmt.Printf("Started: %s\n", recording.StartTime.Format("2006-01-02 15:04:05"))
	fmt.Println()

	if recording.Metadata != nil {
		if recording.Metadata.Branch != "" {
			fmt.Printf("Branch: %s\n", recording.Metadata.Branch)
		}
		if recording.Metadata.CommitSHA != "" {
			fmt.Printf("Commit: %s\n", recording.Metadata.CommitSHA)
		}
		if recording.Metadata.PRUrl != "" {
			fmt.Printf("PR: %s\n", recording.Metadata.PRUrl)
		}
		fmt.Println()
	}

	if verbose {
		// Load and show events
		events, err := replay.LoadStreamEvents(recording)
		if err != nil {
			return fmt.Errorf("failed to load events: %w", err)
		}

		fmt.Printf("Events (%d):\n", len(events))
		fmt.Println(strings.Repeat("-", 50))

		for i, event := range events {
			formatted := replay.FormatEvent(event, true)
			fmt.Printf("[%d] %s\n", i+1, formatted)
		}
	}

	return nil
}

func showRecentLogs(cfg *config.Config, limit int, jsonOut bool) error {
	recordingsPath := replay.DefaultRecordingsPath()

	recordings, err := replay.ListRecordings(recordingsPath, &replay.RecordingFilter{Limit: limit})
	if err != nil {
		return fmt.Errorf("failed to list recordings: %w", err)
	}

	if len(recordings) == 0 {
		fmt.Println("No task logs found.")
		return nil
	}

	if jsonOut {
		data, err := json.MarshalIndent(recordings, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	fmt.Printf("Recent Tasks (%d):\n", len(recordings))
	fmt.Println()

	for _, rec := range recordings {
		statusIcon := "+"
		switch rec.Status {
		case "failed":
			statusIcon = "x"
		case "cancelled":
			statusIcon = "!"
		}

		fmt.Printf("  [%s] %-20s %8s  %s\n",
			statusIcon,
			rec.TaskID,
			rec.Duration.Round(1),
			rec.StartTime.Format("Jan 02 15:04"))
	}

	fmt.Println()
	fmt.Println("Use 'pilot logs <task-id>' for details")

	return nil
}
