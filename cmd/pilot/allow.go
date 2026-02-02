package main

import (
	"fmt"
	"strconv"

	"github.com/alekspetrov/pilot/internal/adapters/telegram"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/spf13/cobra"
)

func newAllowCmd() *cobra.Command {
	var (
		remove bool
		list   bool
	)

	cmd := &cobra.Command{
		Use:   "allow [user_id]",
		Short: "Manage Telegram allowed users",
		Long: `Add, remove, or list Telegram user IDs in allowed_ids.

Examples:
  pilot allow 123456789          # Add user
  pilot allow --remove 123456789 # Remove user
  pilot allow --list             # List allowed users`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// List mode
			if list {
				return listAllowedUsers()
			}

			// Add/remove requires a user ID
			if len(args) == 0 {
				return fmt.Errorf("user_id is required (or use --list to show current users)")
			}

			userID := args[0]

			// Validate ID is numeric
			id, err := strconv.ParseInt(userID, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid user_id: must be a numeric Telegram user ID")
			}

			if remove {
				return removeAllowedUser(id)
			}
			return addAllowedUser(id)
		},
	}

	cmd.Flags().BoolVar(&remove, "remove", false, "Remove user from allowed_ids")
	cmd.Flags().BoolVar(&list, "list", false, "List current allowed users")

	return cmd
}

func listAllowedUsers() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if cfg.Adapters == nil || cfg.Adapters.Telegram == nil {
		fmt.Println("No Telegram configuration found")
		return nil
	}

	allowedIDs := cfg.Adapters.Telegram.AllowedIDs
	if len(allowedIDs) == 0 {
		fmt.Println("No allowed users configured")
		return nil
	}

	fmt.Println("Allowed Telegram users:")
	for _, id := range allowedIDs {
		fmt.Printf("  %d\n", id)
	}
	fmt.Printf("\nTotal: %d user(s)\n", len(allowedIDs))

	return nil
}

func addAllowedUser(id int64) error {
	configPath := cfgFile
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Initialize adapters if nil
	if cfg.Adapters == nil {
		cfg.Adapters = &config.AdaptersConfig{}
	}
	if cfg.Adapters.Telegram == nil {
		cfg.Adapters.Telegram = &telegram.Config{
			Enabled: false,
		}
	}

	// Check for duplicates
	for _, existingID := range cfg.Adapters.Telegram.AllowedIDs {
		if existingID == id {
			fmt.Printf("User %d is already in allowed_ids\n", id)
			return nil
		}
	}

	// Add the ID
	cfg.Adapters.Telegram.AllowedIDs = append(cfg.Adapters.Telegram.AllowedIDs, id)

	// Save config
	if err := config.Save(cfg, configPath); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("✓ Added Telegram user %d to allowed_ids\n", id)
	fmt.Println("  Restart Pilot to apply: pilot restart")

	return nil
}

func removeAllowedUser(id int64) error {
	configPath := cfgFile
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.Adapters == nil || cfg.Adapters.Telegram == nil {
		return fmt.Errorf("no Telegram configuration found")
	}

	// Find and remove the ID
	found := false
	newIDs := make([]int64, 0, len(cfg.Adapters.Telegram.AllowedIDs))
	for _, existingID := range cfg.Adapters.Telegram.AllowedIDs {
		if existingID == id {
			found = true
		} else {
			newIDs = append(newIDs, existingID)
		}
	}

	if !found {
		fmt.Printf("User %d is not in allowed_ids\n", id)
		return nil
	}

	cfg.Adapters.Telegram.AllowedIDs = newIDs

	// Save config
	if err := config.Save(cfg, configPath); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("✓ Removed Telegram user %d from allowed_ids\n", id)
	fmt.Println("  Restart Pilot to apply: pilot restart")

	return nil
}

// init registers the allow command - handled in main.go AddCommand
