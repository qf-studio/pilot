package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/alekspetrov/pilot/internal/replay"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205"))

	menuStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("86"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))
)

// runInteractiveMode starts an interactive CLI session
func runInteractiveMode() error {
	fmt.Println()
	fmt.Println(titleStyle.Render("  Pilot Interactive Mode"))
	fmt.Println(dimStyle.Render("  AI that ships your tickets"))
	fmt.Println()

	// Load config
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	for {
		option := showMainMenu(cfg)

		switch option {
		case "task":
			if err := interactiveNewTask(cfg); err != nil {
				fmt.Printf("Error: %v\n", err)
			}
		case "history":
			if err := interactiveHistory(cfg); err != nil {
				fmt.Printf("Error: %v\n", err)
			}
		case "project":
			if err := interactiveProjectSwitch(cfg); err != nil {
				fmt.Printf("Error: %v\n", err)
			}
		case "status":
			if err := interactiveStatus(cfg); err != nil {
				fmt.Printf("Error: %v\n", err)
			}
		case "quit":
			fmt.Println("\nGoodbye!")
			return nil
		}
	}
}

func showMainMenu(cfg *config.Config) string {
	fmt.Println(menuStyle.Render("  ─────────────────────────────────────"))
	fmt.Println()
	fmt.Printf("  %s Run a new task\n", selectedStyle.Render("[1]"))
	fmt.Printf("  %s View task history\n", selectedStyle.Render("[2]"))
	fmt.Printf("  %s Switch project\n", selectedStyle.Render("[3]"))
	fmt.Printf("  %s Show status\n", selectedStyle.Render("[4]"))
	fmt.Printf("  %s Quit\n", selectedStyle.Render("[q]"))
	fmt.Println()

	// Show current project
	if defaultProj := cfg.GetDefaultProject(); defaultProj != nil {
		fmt.Printf("  %s %s\n", dimStyle.Render("Current project:"), defaultProj.Name)
	}
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("  Select option: ")
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	switch input {
	case "1":
		return "task"
	case "2":
		return "history"
	case "3":
		return "project"
	case "4":
		return "status"
	case "q", "Q", "quit", "exit":
		return "quit"
	default:
		return ""
	}
}

func interactiveNewTask(cfg *config.Config) error {
	fmt.Println()
	fmt.Println(titleStyle.Render("  New Task"))
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)

	// Get task description
	fmt.Print("  Describe your task: ")
	taskDesc, _ := reader.ReadString('\n')
	taskDesc = strings.TrimSpace(taskDesc)

	if taskDesc == "" {
		fmt.Println("  Cancelled.")
		return nil
	}

	// Get project path
	projectPath := ""
	if defaultProj := cfg.GetDefaultProject(); defaultProj != nil {
		projectPath = defaultProj.Path
	} else {
		cwd, _ := os.Getwd()
		projectPath = cwd
	}

	// Confirm
	fmt.Println()
	fmt.Printf("  Task:    %s\n", taskDesc)
	fmt.Printf("  Project: %s\n", projectPath)
	fmt.Println()
	fmt.Print("  Execute? [Y/n]: ")

	confirm, _ := reader.ReadString('\n')
	confirm = strings.TrimSpace(strings.ToLower(confirm))

	if confirm != "" && confirm != "y" && confirm != "yes" {
		fmt.Println("  Cancelled.")
		return nil
	}

	// Execute task
	taskID := fmt.Sprintf("TASK-%d", time.Now().Unix()%100000)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	task := &executor.Task{
		ID:          taskID,
		Title:       taskDesc,
		Description: taskDesc,
		ProjectPath: projectPath,
		Branch:      branchName,
	}

	runner := executor.NewRunner()
	progress := executor.NewProgressDisplay(task.ID, taskDesc, true)

	// Suppress slog progress output when visual display is active
	runner.SuppressProgressLogs(true)

	// Track Navigator mode
	var detectedNavMode string

	runner.OnProgress(func(taskID, phase string, pct int, message string) {
		// Detect Navigator mode
		switch phase {
		case "Navigator", "Loop Mode", "Task Mode":
			progress.SetNavigator(true, phase)
			detectedNavMode = phase
		case "Research", "Implement", "Verify":
			if detectedNavMode == "" {
				detectedNavMode = "nav-task"
			}
			progress.SetNavigator(true, detectedNavMode)
		}
		progress.Update(phase, pct, message)
	})

	fmt.Println()
	fmt.Println("  Executing task with Claude Code...")
	fmt.Println()

	// Start with Navigator check
	progress.StartWithNavigatorCheck(projectPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n\n  Cancelling task...")
		cancel()
	}()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		progress.Finish(false, err.Error())
		return err
	}

	// Build execution report
	report := &executor.ExecutionReport{
		TaskID:           result.TaskID,
		TaskTitle:        taskDesc,
		Success:          result.Success,
		Duration:         result.Duration,
		Branch:           task.Branch,
		CommitSHA:        result.CommitSHA,
		PRUrl:            result.PRUrl,
		HasNavigator:     detectedNavMode != "",
		NavMode:          detectedNavMode,
		TokensInput:      result.TokensInput,
		TokensOutput:     result.TokensOutput,
		EstimatedCostUSD: result.EstimatedCostUSD,
		ModelName:        result.ModelName,
		ErrorMessage:     result.Error,
	}

	// Finish with comprehensive report
	progress.FinishWithReport(report)

	fmt.Println()
	fmt.Print("  Press Enter to continue...")
	_, _ = reader.ReadString('\n')

	return nil
}

func interactiveHistory(cfg *config.Config) error {
	fmt.Println()
	fmt.Println(titleStyle.Render("  Task History"))
	fmt.Println()

	// Load recordings
	recordingsPath := replay.DefaultRecordingsPath()
	recordings, err := replay.ListRecordings(recordingsPath, &replay.RecordingFilter{Limit: 10})
	if err != nil {
		return fmt.Errorf("failed to list recordings: %w", err)
	}

	if len(recordings) == 0 {
		fmt.Println("  No task history found.")
		fmt.Println()
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("  Press Enter to continue...")
		_, _ = reader.ReadString('\n')
		return nil
	}

	// Show recent tasks
	for i, rec := range recordings {
		statusIcon := "+"
		switch rec.Status {
		case "failed":
			statusIcon = "x"
		case "cancelled":
			statusIcon = "!"
		}

		fmt.Printf("  %s [%s] %s (%s)\n",
			selectedStyle.Render(fmt.Sprintf("[%d]", i+1)),
			statusIcon,
			rec.TaskID,
			rec.Duration.Round(time.Second))
	}

	fmt.Println()
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("  Select task to view (or Enter to go back): ")
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	if input == "" {
		return nil
	}

	// Parse selection
	var idx int
	if _, err := fmt.Sscanf(input, "%d", &idx); err != nil || idx < 1 || idx > len(recordings) {
		return nil
	}

	// Show selected recording
	rec := recordings[idx-1]
	recording, err := replay.LoadRecording(recordingsPath, rec.ID)
	if err != nil {
		return fmt.Errorf("failed to load recording: %w", err)
	}

	fmt.Println()
	fmt.Printf("  Task:     %s\n", recording.TaskID)
	fmt.Printf("  Status:   %s\n", recording.Status)
	fmt.Printf("  Duration: %s\n", recording.Duration.Round(time.Second))
	fmt.Printf("  Events:   %d\n", recording.EventCount)
	if recording.Metadata != nil && recording.Metadata.CommitSHA != "" {
		fmt.Printf("  Commit:   %s\n", recording.Metadata.CommitSHA)
	}
	if recording.Metadata != nil && recording.Metadata.PRUrl != "" {
		fmt.Printf("  PR:       %s\n", recording.Metadata.PRUrl)
	}

	fmt.Println()
	fmt.Print("  Press Enter to continue...")
	_, _ = reader.ReadString('\n')

	return nil
}

func interactiveProjectSwitch(cfg *config.Config) error {
	fmt.Println()
	fmt.Println(titleStyle.Render("  Switch Project"))
	fmt.Println()

	if len(cfg.Projects) == 0 {
		fmt.Println("  No projects configured.")
		fmt.Println("  Add projects to ~/.pilot/config.yaml")
		fmt.Println()
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("  Press Enter to continue...")
		_, _ = reader.ReadString('\n')
		return nil
	}

	for i, proj := range cfg.Projects {
		marker := " "
		if cfg.DefaultProject == proj.Name || (cfg.DefaultProject == "" && i == 0) {
			marker = "*"
		}
		nav := ""
		if proj.Navigator {
			nav = " [Navigator]"
		}
		fmt.Printf("  %s [%d] %s%s\n", marker, i+1, proj.Name, nav)
	}

	fmt.Println()
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("  Select project: ")
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	if input == "" {
		return nil
	}

	var idx int
	if _, err := fmt.Sscanf(input, "%d", &idx); err != nil || idx < 1 || idx > len(cfg.Projects) {
		return nil
	}

	selectedProj := cfg.Projects[idx-1]
	cfg.DefaultProject = selectedProj.Name

	// Save updated config
	if err := config.Save(cfg, config.DefaultConfigPath()); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("\n  Switched to: %s\n", selectedProj.Name)
	fmt.Println()
	fmt.Print("  Press Enter to continue...")
	_, _ = reader.ReadString('\n')

	return nil
}

func interactiveStatus(cfg *config.Config) error {
	fmt.Println()
	fmt.Println(titleStyle.Render("  Pilot Status"))
	fmt.Println()

	fmt.Printf("  Gateway: http://%s:%d\n", cfg.Gateway.Host, cfg.Gateway.Port)
	fmt.Println()

	// Adapters
	fmt.Println("  Adapters:")
	if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled {
		fmt.Println("    + Telegram")
	}
	if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled {
		fmt.Println("    + Linear")
	}
	if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled {
		fmt.Println("    + Slack")
	}
	if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled {
		fmt.Println("    + GitHub")
	}
	fmt.Println()

	// Projects
	fmt.Println("  Projects:")
	if len(cfg.Projects) == 0 {
		fmt.Println("    (none)")
	} else {
		for _, proj := range cfg.Projects {
			nav := ""
			if proj.Navigator {
				nav = " [Navigator]"
			}
			fmt.Printf("    - %s: %s%s\n", proj.Name, proj.Path, nav)
		}
	}
	fmt.Println()

	// Memory stats
	store, err := memory.NewStore(cfg.Memory.Path)
	if err == nil {
		defer func() { _ = store.Close() }()
		stats, err := store.GetCrossPatternStats()
		if err == nil && stats.TotalPatterns > 0 {
			fmt.Printf("  Patterns: %d learned\n", stats.TotalPatterns)
		}
	}

	fmt.Println()
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("  Press Enter to continue...")
	_, _ = reader.ReadString('\n')

	return nil
}
