package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/alekspetrov/pilot/internal/upgrade"
)

func newUpgradeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade Pilot to the latest version",
		Long: `Check for and install Pilot updates.

The upgrade process:
1. Checks GitHub for the latest release
2. Waits for any running tasks to complete
3. Downloads the new version
4. Creates a backup of the current version
5. Installs the update
6. Automatically restarts (unless --no-restart)

On failure, the previous version is automatically restored.

Examples:
  pilot upgrade                    # Check and upgrade
  pilot upgrade --check            # Only check for updates
  pilot upgrade --force            # Skip task completion wait
  pilot upgrade --no-restart       # Don't restart after upgrade
  pilot upgrade rollback           # Restore previous version`,
	}

	cmd.AddCommand(
		newUpgradeCheckCmd(),
		newUpgradeRunCmd(),
		newUpgradeRollbackCmd(),
		newUpgradeCleanupCmd(),
	)

	// Default subcommand is "run"
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runUpgradeRun(cmd, args, false, true, false)
	}

	return cmd
}

func newUpgradeCheckCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check for available updates",
		RunE: func(cmd *cobra.Command, args []string) error {
			upgrader, err := upgrade.NewUpgrader(version)
			if err != nil {
				return fmt.Errorf("failed to initialize upgrader: %w", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			info, err := upgrader.CheckVersion(ctx)
			if err != nil {
				return fmt.Errorf("failed to check version: %w", err)
			}

			if jsonOutput {
				fmt.Printf(`{"current":"%s","latest":"%s","update_available":%t}`,
					info.Current, info.Latest, info.UpdateAvail)
				fmt.Println()
				return nil
			}

			fmt.Println("🔍 Version Check")
			fmt.Println("───────────────────────────────────────")
			fmt.Printf("   Current:  %s\n", info.Current)
			fmt.Printf("   Latest:   %s\n", info.Latest)
			fmt.Println()

			if info.UpdateAvail {
				fmt.Println("✨ A new version is available!")
				fmt.Println()
				if info.ReleaseNotes != "" {
					fmt.Println("Release Notes:")
					fmt.Println("───────────────────────────────────────")
					// Truncate long release notes
					notes := info.ReleaseNotes
					if len(notes) > 500 {
						notes = notes[:497] + "..."
					}
					fmt.Println(notes)
					fmt.Println("───────────────────────────────────────")
					fmt.Println()
				}
				fmt.Println("Run 'pilot upgrade' to install the update.")
			} else {
				fmt.Println("✅ You're running the latest version!")
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}

func newUpgradeRunCmd() *cobra.Command {
	var (
		force     bool
		noRestart bool
		yes       bool
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Download and install the latest version",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpgradeRun(cmd, args, force, !noRestart, yes)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip waiting for running tasks")
	cmd.Flags().BoolVar(&noRestart, "no-restart", false, "Don't restart after upgrade")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")

	return cmd
}

func runUpgradeRun(cmd *cobra.Command, args []string, force, autoRestart, skipConfirm bool) error {
	// Create graceful upgrader (no task checker for CLI mode)
	gracefulUpgrader, err := upgrade.NewGracefulUpgrader(version, &upgrade.NoOpTaskChecker{})
	if err != nil {
		return fmt.Errorf("failed to initialize upgrader: %w", err)
	}

	upgrader := gracefulUpgrader.GetUpgrader()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl+C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n⚠️  Upgrade cancelled")
		cancel()
	}()

	// Check for updates
	fmt.Println("🔍 Checking for updates...")

	info, err := upgrader.CheckVersion(ctx)
	if err != nil {
		return fmt.Errorf("failed to check version: %w", err)
	}

	if !info.UpdateAvail {
		fmt.Println()
		fmt.Printf("✅ Already running the latest version (%s)\n", info.Current)
		return nil
	}

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("🚀 Pilot Upgrade")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("   Current:  %s\n", info.Current)
	fmt.Printf("   New:      %s\n", info.Latest)
	fmt.Println()

	if info.ReleaseNotes != "" {
		fmt.Println("Release Notes:")
		fmt.Println("───────────────────────────────────────")
		notes := info.ReleaseNotes
		if len(notes) > 300 {
			notes = notes[:297] + "..."
		}
		fmt.Println(notes)
		fmt.Println("───────────────────────────────────────")
		fmt.Println()
	}

	// Confirm unless -y flag
	if !skipConfirm {
		fmt.Print("Proceed with upgrade? [y/N]: ")
		var input string
		_, _ = fmt.Scanln(&input)
		if input != "y" && input != "Y" {
			fmt.Println("Upgrade cancelled.")
			return nil
		}
		fmt.Println()
	}

	// Perform upgrade with progress
	opts := &upgrade.UpgradeOptions{
		WaitForTasks: !force,
		TaskTimeout:  5 * time.Minute,
		Force:        force,
		AutoRestart:  autoRestart,
		OnProgress: func(pct int, msg string) {
			bar := progressBar(pct, 30)
			fmt.Printf("\r   %s %3d%% %s", bar, pct, msg)
			if pct >= 100 {
				fmt.Println()
			}
		},
	}

	if err := gracefulUpgrader.PerformUpgrade(ctx, info.LatestRelease, opts); err != nil {
		fmt.Println()
		fmt.Printf("❌ Upgrade failed: %v\n", err)

		if upgrader.HasBackup() {
			fmt.Println()
			fmt.Println("💡 A backup of the previous version exists.")
			fmt.Println("   Run 'pilot upgrade rollback' to restore it.")
		}

		return err
	}

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("✅ Upgrade complete!")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	if !autoRestart {
		fmt.Println()
		fmt.Println("Restart Pilot to use the new version:")
		fmt.Println("   pilot start")
	}

	return nil
}

func newUpgradeRollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback",
		Short: "Restore the previous version",
		Long:  `Restore the previous Pilot version from backup created during upgrade.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			upgrader, err := upgrade.NewUpgrader(version)
			if err != nil {
				return fmt.Errorf("failed to initialize upgrader: %w", err)
			}

			if !upgrader.HasBackup() {
				fmt.Println("❌ No backup found.")
				fmt.Println()
				fmt.Println("   A backup is created automatically during upgrade")
				fmt.Println("   and removed after successful verification.")
				return nil
			}

			fmt.Println("🔄 Rolling back to previous version...")

			if err := upgrader.Rollback(); err != nil {
				return fmt.Errorf("rollback failed: %w", err)
			}

			fmt.Println("✅ Rollback complete!")
			fmt.Println()
			fmt.Println("   Restart Pilot to use the previous version:")
			fmt.Println("   pilot start")

			return nil
		},
	}
}

func newUpgradeCleanupCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "cleanup",
		Short:  "Clean up upgrade state and backup",
		Hidden: true, // Internal command
		RunE: func(cmd *cobra.Command, args []string) error {
			gracefulUpgrader, err := upgrade.NewGracefulUpgrader(version, &upgrade.NoOpTaskChecker{})
			if err != nil {
				return err
			}

			if err := gracefulUpgrader.CleanupState(); err != nil {
				return fmt.Errorf("cleanup failed: %w", err)
			}

			fmt.Println("✅ Cleanup complete")
			return nil
		},
	}
}

// progressBar generates an ASCII progress bar
func progressBar(pct, width int) string {
	filled := pct * width / 100
	empty := width - filled

	bar := "["
	for i := 0; i < filled; i++ {
		bar += "█"
	}
	for i := 0; i < empty; i++ {
		bar += "░"
	}
	bar += "]"

	return bar
}
