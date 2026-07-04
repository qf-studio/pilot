package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/memory"
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
	var reveal bool

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		Long: `Show current configuration.

Values whose key looks like a token, key, secret, or password are masked by
default (first 4 + last 4 characters shown). Pass --reveal to print the raw,
unredacted values.`,
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
				if !reveal {
					if data, err = redactSecretsJSON(data); err != nil {
						return fmt.Errorf("failed to redact config: %w", err)
					}
				}
				fmt.Println(string(data))
				return nil
			}

			// YAML output
			data, err := yaml.Marshal(cfg)
			if err != nil {
				return fmt.Errorf("failed to marshal config: %w", err)
			}
			if !reveal {
				if data, err = redactSecretsYAML(data); err != nil {
					return fmt.Errorf("failed to redact config: %w", err)
				}
			}
			fmt.Print(string(data))

			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&reveal, "reveal", false, "Show raw, unredacted secret values")

	return cmd
}

// secretKeyPattern matches config keys likely to hold sensitive values
// (tokens, API keys, secrets, passwords) so `pilot config show` can redact
// them by default (GH-3839).
var secretKeyPattern = regexp.MustCompile(`(?i)(token|key|secret|password)`)

// maskSecret shows only the first 4 and last 4 characters of a secret value
// (e.g. "ghp_...cdef"). Values too short to mask meaningfully (<=8 chars)
// are fully masked so no useful substring leaks.
func maskSecret(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "..." + s[len(s)-4:]
}

// redactSecretValue walks a generically-decoded config tree (nested maps and
// slices from a YAML/JSON round-trip) and masks any string value whose key
// matches secretKeyPattern. Operating on the generic tree rather than the
// typed *config.Config struct means every current and future secret-shaped
// field is covered without needing a matching struct tag or field list.
func redactSecretValue(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, sub := range val {
			if s, ok := sub.(string); ok && s != "" && secretKeyPattern.MatchString(k) {
				val[k] = maskSecret(s)
			} else {
				val[k] = redactSecretValue(sub)
			}
		}
		return val
	case []interface{}:
		for i, sub := range val {
			val[i] = redactSecretValue(sub)
		}
		return val
	default:
		return v
	}
}

// redactSecretsYAML re-serializes YAML-encoded config with secret values masked.
func redactSecretsYAML(data []byte) ([]byte, error) {
	var generic interface{}
	if err := yaml.Unmarshal(data, &generic); err != nil {
		return nil, err
	}
	return yaml.Marshal(redactSecretValue(generic))
}

// redactSecretsJSON re-serializes JSON-encoded config with secret values masked.
func redactSecretsJSON(data []byte) ([]byte, error) {
	var generic interface{}
	if err := json.Unmarshal(data, &generic); err != nil {
		return nil, err
	}
	return json.MarshalIndent(redactSecretValue(generic), "", "  ")
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
				if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled {
					hasAdapter = true
					if cfg.Adapters.GitHub.Token == "" && os.Getenv("GITHUB_TOKEN") == "" {
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

// logDisplayLimit is the number of execution_logs lines shown for `pilot logs <task-id>`.
// verboseLogDisplayLimit raises the cap when --verbose is passed.
const (
	logDisplayLimit        = 100
	verboseLogDisplayLimit = 500
)

// showTaskLogs prints logs for a specific task, sourced from the memory store (executions +
// execution_logs tables) rather than replay recordings, so it also covers tasks that are
// still running or finished recently but haven't produced a finalized recording (GH-3724).
func showTaskLogs(taskID string, cfg *config.Config, verbose, jsonOut bool) error {
	store, err := memory.NewStore(cfg.Memory.Path)
	if err != nil {
		return fmt.Errorf("failed to open memory store: %w", err)
	}
	defer func() { _ = store.Close() }()

	exec, err := store.GetLatestExecutionByTaskID(taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no logs found for task: %s", taskID)
		}
		return fmt.Errorf("failed to look up task: %w", err)
	}

	limit := logDisplayLimit
	if verbose {
		limit = verboseLogDisplayLimit
	}
	logs, err := store.GetLogsByExecutionID(exec.TaskID, limit)
	if err != nil {
		return fmt.Errorf("failed to load logs: %w", err)
	}

	if jsonOut {
		out := struct {
			Execution *memory.Execution  `json:"execution"`
			Logs      []*memory.LogEntry `json:"logs"`
		}{exec, logs}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	// Display task info
	statusIcon := "+"
	switch exec.Status {
	case "failed":
		statusIcon = "x"
	case "cancelled", "no_op":
		statusIcon = "!"
	}

	fmt.Printf("Task: %s [%s]\n", exec.TaskID, statusIcon)
	fmt.Printf("Status: %s\n", exec.Status)
	fmt.Printf("Duration: %s\n", time.Duration(exec.DurationMs)*time.Millisecond)
	fmt.Printf("Started: %s\n", exec.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Println()

	if exec.TaskBranch != "" || exec.CommitSHA != "" || exec.PRUrl != "" {
		if exec.TaskBranch != "" {
			fmt.Printf("Branch: %s\n", exec.TaskBranch)
		}
		if exec.CommitSHA != "" {
			fmt.Printf("Commit: %s\n", exec.CommitSHA)
		}
		if exec.PRUrl != "" {
			fmt.Printf("PR: %s\n", exec.PRUrl)
		}
		fmt.Println()
	}

	if len(logs) == 0 {
		fmt.Println("No log entries recorded for this task.")
		return nil
	}

	fmt.Printf("Log entries (%d):\n", len(logs))
	fmt.Println(strings.Repeat("-", 50))

	for _, entry := range logs {
		fmt.Printf("[%s] %-5s %s: %s\n",
			entry.Timestamp.Format("15:04:05"),
			strings.ToUpper(entry.Level),
			entry.Component,
			entry.Message)
	}

	return nil
}

// showRecentLogs prints the most recently created tasks, sourced from the memory store
// (executions table) so it reflects live/in-progress executions, not just finalized
// replay recordings (GH-3724).
func showRecentLogs(cfg *config.Config, limit int, jsonOut bool) error {
	store, err := memory.NewStore(cfg.Memory.Path)
	if err != nil {
		return fmt.Errorf("failed to open memory store: %w", err)
	}
	defer func() { _ = store.Close() }()

	executions, err := store.GetRecentExecutions(limit, "")
	if err != nil {
		return fmt.Errorf("failed to load recent executions: %w", err)
	}

	if len(executions) == 0 {
		fmt.Println("No task logs found.")
		return nil
	}

	if jsonOut {
		data, err := json.MarshalIndent(executions, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	fmt.Printf("Recent Tasks (%d):\n", len(executions))
	fmt.Println()

	for _, exec := range executions {
		statusIcon := "+"
		switch exec.Status {
		case "failed":
			statusIcon = "x"
		case "cancelled", "no_op":
			statusIcon = "!"
		}

		fmt.Printf("  [%s] %-20s %8s  %s\n",
			statusIcon,
			exec.TaskID,
			(time.Duration(exec.DurationMs) * time.Millisecond).Round(time.Second),
			exec.CreatedAt.Format("Jan 02 15:04"))
	}

	fmt.Println()
	fmt.Println("Use 'pilot logs <task-id>' for details")

	return nil
}
