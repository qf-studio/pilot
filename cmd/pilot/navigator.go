package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

func newNavigatorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "navigator",
		Short: "Manage the project's Navigator knowledge base",
	}
	cmd.AddCommand(newNavigatorBootstrapCmd())
	return cmd
}

func newNavigatorBootstrapCmd() *cobra.Command {
	var projectPath string

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Fill the Navigator scaffold's context sections with one agent session",
		Long: `Run the one-time bootstrap session on a project: Claude reads the repository
and writes the Key Components, Key Files and Project Structure sections of
.agent/DEVELOPMENT-README.md, an architecture note, pattern memories and the
knowledge graph. Writes are restricted to the .agent/ directory; no tracked
file is touched and no PR is opened. The run is recorded as an execution with
task id bootstrap-<date>.

The same session runs automatically before the first task when
executor.navigator.bootstrap is "auto".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := filepath.Abs(projectPath)
			if err != nil {
				return fmt.Errorf("invalid project path: %w", err)
			}
			if info, err := os.Stat(path); err != nil || !info.IsDir() {
				return fmt.Errorf("project path %s is not a directory", path)
			}

			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			runner, err := executor.NewRunnerWithConfig(cfg.Executor)
			if err != nil {
				return fmt.Errorf("failed to create executor runner: %w", err)
			}
			if store, err := memory.NewStore(cfg.Memory.Path); err != nil {
				fmt.Fprintf(os.Stderr, "warning: execution will not be recorded: %v\n", err)
			} else {
				defer func() { _ = store.Close() }()
				runner.SetLogStore(store)
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			fmt.Printf("Bootstrapping Navigator for %s (up to 10 minutes)...\n", path)
			res, err := runner.ExecuteNavigatorBootstrap(ctx, path)
			if res != nil {
				printNavigatorBootstrapSummary(res)
			}
			return err
		},
	}

	cmd.Flags().StringVar(&projectPath, "project", ".", "Project path to bootstrap")
	return cmd
}

func printNavigatorBootstrapSummary(res *executor.NavigatorBootstrapResult) {
	fmt.Println()
	fmt.Printf("Task:      %s\n", res.TaskID)
	fmt.Printf("Model:     %s\n", res.Model)
	fmt.Printf("Duration:  %s\n", res.Duration.Round(100*1e6))
	fmt.Printf("Tokens:    %s in / %s out\n", formatTokens(res.TokensInput), formatTokens(res.TokensOutput))
	fmt.Printf("Cost:      $%.4f (estimated)", res.CostUSD)
	if res.ReportedCostUSD != nil {
		fmt.Printf(", $%.4f (reported)", *res.ReportedCostUSD)
	}
	fmt.Printf("\nKey:       %s\n", res.KeySource)
	fmt.Printf("Files written (%d):\n", len(res.FilesWritten))
	for _, f := range res.FilesWritten {
		fmt.Printf("  %s\n", f)
	}
	if res.Partial {
		fmt.Println("Session stopped on its turn or time limit; the output may be partial.")
	}
	if res.PlaceholdersRemain {
		fmt.Println("Placeholder text remains in the README context sections; rerun to finish.")
	}
}
