package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/webhooks"
)

func newWebhooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "webhooks",
		Short: "Manage outbound webhooks",
		Long: `Manage outbound webhooks for Pilot events.

Webhooks allow external integrations to receive real-time notifications
when tasks start, complete, fail, or when PRs are created.

Supported events:
  - task.started    Task execution began
  - task.progress   Phase updates during execution
  - task.completed  Task finished successfully
  - task.failed     Task failed
  - pr.created      Pull request was created
  - budget.warning  Budget threshold reached

Examples:
  pilot webhooks list                              # List configured webhooks
  pilot webhooks add --url https://example.com/hook --secret $SECRET
  pilot webhooks remove ep_abc123
  pilot webhooks test ep_abc123                    # Send test event`,
	}

	cmd.AddCommand(
		newWebhooksListCmd(),
		newWebhooksAddCmd(),
		newWebhooksRemoveCmd(),
		newWebhooksTestCmd(),
		newWebhooksEventsCmd(),
	)

	return cmd
}

func newWebhooksListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured webhook endpoints",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			webhooksCfg := cfg.Webhooks
			if webhooksCfg == nil {
				webhooksCfg = webhooks.DefaultConfig()
			}

			if outputJSON {
				data, err := json.MarshalIndent(webhooksCfg.Endpoints, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal webhooks: %w", err)
				}
				fmt.Println(string(data))
				return nil
			}

			fmt.Println()
			fmt.Println("Webhook Endpoints")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			if !webhooksCfg.Enabled {
				fmt.Println("  Status: ○ Disabled")
				fmt.Println()
				fmt.Println("  Enable webhooks in ~/.pilot/config.yaml:")
				fmt.Println("    webhooks:")
				fmt.Println("      enabled: true")
				fmt.Println()
				return nil
			}

			if len(webhooksCfg.Endpoints) == 0 {
				fmt.Println("  No endpoints configured")
				fmt.Println()
				fmt.Println("  Add an endpoint:")
				fmt.Println("    pilot webhooks add --url https://example.com/hook --secret $SECRET")
				fmt.Println()
				return nil
			}

			for _, ep := range webhooksCfg.Endpoints {
				statusIcon := "○"
				if ep.Enabled {
					statusIcon = "✓"
				}

				fmt.Printf("\n  %s %s\n", statusIcon, ep.Name)
				fmt.Printf("    ID:     %s\n", ep.ID)
				fmt.Printf("    URL:    %s\n", ep.URL)
				if len(ep.Events) > 0 {
					events := make([]string, len(ep.Events))
					for i, e := range ep.Events {
						events[i] = string(e)
					}
					fmt.Printf("    Events: %s\n", strings.Join(events, ", "))
				} else {
					fmt.Printf("    Events: all\n")
				}
				if ep.Secret != "" {
					fmt.Printf("    Secret: ****%s\n", lastN(ep.Secret, 4))
				}
			}

			fmt.Println()
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")

	return cmd
}

func newWebhooksAddCmd() *cobra.Command {
	var (
		name    string
		url     string
		secret  string
		events  []string
		enabled bool
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a new webhook endpoint",
		Long: `Add a new webhook endpoint to receive Pilot events.

Events:
  task.started, task.progress, task.completed, task.failed, pr.created, budget.warning

Examples:
  # Subscribe to all events
  pilot webhooks add --url https://example.com/hook --secret $SECRET

  # Subscribe to specific events
  pilot webhooks add --url https://example.com/hook --secret $SECRET \
    --events task.completed,task.failed,pr.created

  # With custom name
  pilot webhooks add --name "Slack Integration" --url https://hooks.slack.com/... --secret $SECRET`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if url == "" {
				return fmt.Errorf("--url is required")
			}

			cfg, err := loadConfig()
			if err != nil {
				cfg = config.DefaultConfig()
			}

			if cfg.Webhooks == nil {
				cfg.Webhooks = webhooks.DefaultConfig()
			}

			// Parse events
			var eventTypes []webhooks.EventType
			for _, e := range events {
				eventTypes = append(eventTypes, webhooks.EventType(e))
			}

			// Generate ID
			id := "ep_" + randomID(8)

			// Default name from URL if not provided
			if name == "" {
				name = extractHostFromURL(url)
			}

			endpoint := &webhooks.EndpointConfig{
				ID:      id,
				Name:    name,
				URL:     url,
				Secret:  os.ExpandEnv(secret),
				Events:  eventTypes,
				Enabled: enabled,
			}

			cfg.Webhooks.Endpoints = append(cfg.Webhooks.Endpoints, endpoint)

			// Save config
			configPath := config.DefaultConfigPath()
			if err := config.Save(cfg, configPath); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			fmt.Printf("✓ Added webhook endpoint: %s (%s)\n", endpoint.Name, endpoint.ID)
			if !cfg.Webhooks.Enabled {
				fmt.Println()
				fmt.Println("Note: Webhooks are disabled. Enable in config:")
				fmt.Println("  webhooks:")
				fmt.Println("    enabled: true")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Endpoint name (optional)")
	cmd.Flags().StringVar(&url, "url", "", "Webhook URL (required)")
	cmd.Flags().StringVar(&secret, "secret", "", "HMAC signing secret")
	cmd.Flags().StringSliceVar(&events, "events", nil, "Event types to subscribe (default: all)")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "Enable endpoint")

	return cmd
}

func newWebhooksRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <endpoint-id>",
		Short: "Remove a webhook endpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			endpointID := args[0]

			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if cfg.Webhooks == nil {
				return fmt.Errorf("no webhooks configured")
			}

			// Find and remove endpoint
			found := false
			var newEndpoints []*webhooks.EndpointConfig
			for _, ep := range cfg.Webhooks.Endpoints {
				if ep.ID == endpointID {
					found = true
					continue
				}
				newEndpoints = append(newEndpoints, ep)
			}

			if !found {
				return fmt.Errorf("endpoint not found: %s", endpointID)
			}

			cfg.Webhooks.Endpoints = newEndpoints

			// Save config
			configPath := config.DefaultConfigPath()
			if err := config.Save(cfg, configPath); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			fmt.Printf("✓ Removed webhook endpoint: %s\n", endpointID)
			return nil
		},
	}
}

func newWebhooksTestCmd() *cobra.Command {
	var eventType string

	cmd := &cobra.Command{
		Use:   "test [endpoint-id]",
		Short: "Send a test event to webhook endpoint(s)",
		Long: `Send a test event to verify webhook endpoint configuration.

If no endpoint ID is specified, sends to all enabled endpoints.

Examples:
  pilot webhooks test                    # Test all enabled endpoints
  pilot webhooks test ep_abc123          # Test specific endpoint
  pilot webhooks test --event task.failed # Test with specific event type`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if cfg.Webhooks == nil || len(cfg.Webhooks.Endpoints) == 0 {
				return fmt.Errorf("no webhooks configured")
			}

			// Create manager
			manager := webhooks.NewManager(cfg.Webhooks, nil)

			// Create test event
			testEvent := webhooks.NewEvent(
				webhooks.EventType(eventType),
				map[string]interface{}{
					"task_id": "test_" + randomID(8),
					"title":   "Test Event",
					"project": "pilot",
					"message": "This is a test event from 'pilot webhooks test'",
				},
			)

			// Verify endpoint exists if ID specified
			if len(args) > 0 {
				ep := manager.GetEndpoint(args[0])
				if ep == nil {
					return fmt.Errorf("endpoint not found: %s", args[0])
				}
			}

			fmt.Println()
			fmt.Printf("Sending test event: %s\n", eventType)
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// Dispatch event
			results := manager.Dispatch(ctx, testEvent)

			// Show results
			for _, result := range results {
				ep := manager.GetEndpoint(result.EndpointID)
				name := result.EndpointID
				if ep != nil {
					name = ep.Name
				}

				if result.Success {
					fmt.Printf("  ✓ %s (status %d, %v)\n", name, result.StatusCode, result.Duration.Round(time.Millisecond))
				} else {
					fmt.Printf("  ✗ %s: %v\n", name, result.Error)
				}
			}

			// Summary
			successCount := 0
			for _, r := range results {
				if r.Success {
					successCount++
				}
			}

			fmt.Println()
			if len(results) == 0 {
				fmt.Println("No endpoints to test (all disabled or filtered)")
			} else {
				fmt.Printf("Results: %d/%d successful\n", successCount, len(results))
			}
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().StringVar(&eventType, "event", "task.completed", "Event type to test")

	return cmd
}

func newWebhooksEventsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "events",
		Short: "List available webhook event types",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println()
			fmt.Println("Available Webhook Events")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println()
			fmt.Println("  Task Events:")
			fmt.Println("    task.started    - Task execution began")
			fmt.Println("    task.progress   - Phase updates during execution")
			fmt.Println("    task.completed  - Task finished successfully")
			fmt.Println("    task.failed     - Task failed")
			fmt.Println()
			fmt.Println("  PR Events:")
			fmt.Println("    pr.created      - Pull request was created")
			fmt.Println()
			fmt.Println("  Budget Events:")
			fmt.Println("    budget.warning  - Budget threshold reached")
			fmt.Println()
		},
	}
}

// Helper functions

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func extractHostFromURL(urlStr string) string {
	// Simple extraction - just get host part
	urlStr = strings.TrimPrefix(urlStr, "https://")
	urlStr = strings.TrimPrefix(urlStr, "http://")
	parts := strings.Split(urlStr, "/")
	if len(parts) > 0 {
		return parts[0]
	}
	return urlStr
}

func randomID(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[time.Now().UnixNano()%int64(len(charset))]
		time.Sleep(time.Nanosecond)
	}
	return string(b)
}
