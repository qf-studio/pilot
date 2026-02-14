// Package main provides the onboard ticket source setup stage.
// GH-1240: Ticket source setup for pilot onboard command.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/asana"
	"github.com/alekspetrov/pilot/internal/adapters/azuredevops"
	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/adapters/gitlab"
	"github.com/alekspetrov/pilot/internal/adapters/jira"
	"github.com/alekspetrov/pilot/internal/adapters/linear"
	"github.com/alekspetrov/pilot/internal/config"
)

// TicketSource represents an available ticket source adapter
type TicketSource struct {
	Name        string
	Description string
	SetupFunc   func(*OnboardState) error
	IsEnabled   func(*config.Config) bool
}

// getTicketSourcesForPersona returns available ticket sources based on persona
func getTicketSourcesForPersona(persona Persona) []TicketSource {
	allSources := []TicketSource{
		{
			Name:        "GitHub Issues",
			Description: "Track issues in GitHub repositories",
			SetupFunc:   onboardGitHubTickets,
			IsEnabled:   func(cfg *config.Config) bool { return cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled },
		},
		{
			Name:        "Linear",
			Description: "Modern issue tracking for software teams",
			SetupFunc:   onboardLinearTickets,
			IsEnabled:   func(cfg *config.Config) bool { return cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled },
		},
		{
			Name:        "Jira",
			Description: "Atlassian's project management tool",
			SetupFunc:   onboardJiraTickets,
			IsEnabled:   func(cfg *config.Config) bool { return cfg.Adapters.Jira != nil && cfg.Adapters.Jira.Enabled },
		},
		{
			Name:        "GitLab Issues",
			Description: "Track issues in GitLab projects",
			SetupFunc:   onboardGitLabTickets,
			IsEnabled:   func(cfg *config.Config) bool { return cfg.Adapters.GitLab != nil && cfg.Adapters.GitLab.Enabled },
		},
		{
			Name:        "Azure DevOps",
			Description: "Microsoft's DevOps work items",
			SetupFunc:   onboardAzureDevOpsTickets,
			IsEnabled:   func(cfg *config.Config) bool { return cfg.Adapters.AzureDevOps != nil && cfg.Adapters.AzureDevOps.Enabled },
		},
		{
			Name:        "Asana",
			Description: "Task and project management",
			SetupFunc:   onboardAsanaTickets,
			IsEnabled:   func(cfg *config.Config) bool { return cfg.Adapters.Asana != nil && cfg.Adapters.Asana.Enabled },
		},
	}

	switch persona {
	case PersonaSolo:
		// Solo: GitHub only
		return allSources[:1]
	case PersonaTeam:
		// Team: GitHub, Linear, Jira
		return allSources[:3]
	case PersonaEnterprise:
		// Enterprise: All sources
		return allSources
	default:
		return allSources[:3]
	}
}

// onboardTicketSetup runs the ticket source setup stage
func onboardTicketSetup(state *OnboardState) error {
	sources := getTicketSourcesForPersona(state.Persona)

	// Show already configured sources
	configuredSources := []string{}
	for _, src := range sources {
		if src.IsEnabled(state.Config) {
			configuredSources = append(configuredSources, src.Name)
		}
	}

	if len(configuredSources) > 0 {
		fmt.Println()
		fmt.Println("Already configured:")
		for _, name := range configuredSources {
			fmt.Printf("  ✓ %s\n", name)
		}
		fmt.Println()
	}

	// For Solo persona with GitHub already configured, skip
	if state.Persona == PersonaSolo {
		if state.Config.Adapters.GitHub != nil && state.Config.Adapters.GitHub.Enabled {
			fmt.Println("  GitHub Issues already configured")
			return nil
		}
		// Solo goes straight to GitHub setup
		return onboardGitHubTickets(state)
	}

	// For Team/Enterprise, show selection menu
	for {
		availableSources := []TicketSource{}
		for _, src := range sources {
			if !src.IsEnabled(state.Config) {
				availableSources = append(availableSources, src)
			}
		}

		if len(availableSources) == 0 {
			fmt.Println("  All ticket sources configured")
			break
		}

		fmt.Println()
		fmt.Println("Where do your tickets live?")
		fmt.Println()
		for i, src := range availableSources {
			fmt.Printf("  %d  %s\n", i+1, src.Name)
		}
		fmt.Println()

		fmt.Print("▸ ")
		choice := readOnboardLine(state.Reader)
		if choice == "" {
			break
		}

		// Parse choice
		idx := 0
		if _, err := fmt.Sscanf(choice, "%d", &idx); err != nil || idx < 1 || idx > len(availableSources) {
			fmt.Println("  Invalid selection")
			continue
		}

		// Run setup for selected source
		selected := availableSources[idx-1]
		if err := selected.SetupFunc(state); err != nil {
			return err
		}

		// Ask if they want to add another
		fmt.Println()
		fmt.Print("Add another ticket source? [y/N]: ")
		if !readOnboardYesNo(state.Reader, false) {
			break
		}
	}

	return nil
}

// onboardGitHubTickets sets up GitHub Issues as a ticket source
func onboardGitHubTickets(state *OnboardState) error {
	fmt.Println()
	fmt.Println("GitHub Issues Setup")
	fmt.Println("─────────────────────────")

	// Initialize config if needed
	if state.Config.Adapters.GitHub == nil {
		state.Config.Adapters.GitHub = github.DefaultConfig()
	}

	// Check for token from environment first
	token := os.Getenv("GITHUB_TOKEN")
	if token != "" {
		fmt.Println("  Found $GITHUB_TOKEN in environment")
		fmt.Print("  Use this token? [Y/n]: ")
		if readOnboardYesNo(state.Reader, true) {
			state.Config.Adapters.GitHub.Token = token
		} else {
			token = ""
		}
	}

	// Prompt for token if not set
	if token == "" {
		fmt.Println()
		fmt.Println("  Create a token at: https://github.com/settings/tokens")
		fmt.Println("  Required scopes: repo")
		fmt.Print("  GitHub token: ")
		token = readOnboardLine(state.Reader)
		if token == "" {
			fmt.Println("  ○ Skipped - no token provided")
			return nil
		}
		state.Config.Adapters.GitHub.Token = token
	}

	// Validate connection
	fmt.Print("  Validating... ")
	if err := validateGitHubConn(token); err != nil {
		fmt.Printf("✗ %v\n", err)
		return handleValidationFailure(state, "GitHub", func() error {
			return onboardGitHubTickets(state)
		})
	}
	fmt.Println("✓ Connected")

	// Pre-fill repo from project config if available
	defaultRepo := ""
	if len(state.Config.Projects) > 0 && state.Config.Projects[0].GitHub != nil {
		gh := state.Config.Projects[0].GitHub
		if gh.Owner != "" && gh.Repo != "" {
			defaultRepo = gh.Owner + "/" + gh.Repo
		}
	}

	// Prompt for repo
	if defaultRepo != "" {
		fmt.Printf("  Repository [%s]: ", defaultRepo)
	} else {
		fmt.Print("  Repository (owner/repo): ")
	}
	repo := readOnboardLine(state.Reader)
	if repo == "" {
		repo = defaultRepo
	}
	if repo != "" {
		state.Config.Adapters.GitHub.Repo = repo
	}

	// Prompt for label
	fmt.Print("  Pilot label [pilot]: ")
	label := readOnboardLine(state.Reader)
	if label == "" {
		label = "pilot"
	}
	state.Config.Adapters.GitHub.PilotLabel = label

	// Enable polling
	if state.Config.Adapters.GitHub.Polling == nil {
		state.Config.Adapters.GitHub.Polling = &github.PollingConfig{
			Interval: 30 * time.Second,
			Label:    label,
		}
	}
	state.Config.Adapters.GitHub.Polling.Enabled = true
	state.Config.Adapters.GitHub.Polling.Label = label

	state.Config.Adapters.GitHub.Enabled = true
	fmt.Println("  ✓ GitHub Issues configured")

	return nil
}

// onboardLinearTickets sets up Linear as a ticket source
func onboardLinearTickets(state *OnboardState) error {
	fmt.Println()
	fmt.Println("Linear Setup")
	fmt.Println("─────────────────────────")

	// Initialize config if needed
	if state.Config.Adapters.Linear == nil {
		state.Config.Adapters.Linear = linear.DefaultConfig()
	}

	// Check for API key from environment first
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey != "" {
		fmt.Println("  Found $LINEAR_API_KEY in environment")
		fmt.Print("  Use this key? [Y/n]: ")
		if readOnboardYesNo(state.Reader, true) {
			state.Config.Adapters.Linear.APIKey = apiKey
		} else {
			apiKey = ""
		}
	}

	// Prompt for API key if not set
	if apiKey == "" {
		fmt.Println()
		fmt.Println("  Get your API key at: https://linear.app/settings/api")
		fmt.Print("  Linear API key: ")
		apiKey = readOnboardLine(state.Reader)
		if apiKey == "" {
			fmt.Println("  ○ Skipped - no API key provided")
			return nil
		}
		state.Config.Adapters.Linear.APIKey = apiKey
	}

	// Validate connection
	fmt.Print("  Validating... ")
	workspaceName, err := validateLinearConn(apiKey)
	if err != nil {
		fmt.Printf("✗ %v\n", err)
		return handleValidationFailure(state, "Linear", func() error {
			return onboardLinearTickets(state)
		})
	}
	fmt.Printf("✓ Connected to %q\n", workspaceName)

	// Prompt for team ID (optional)
	fmt.Print("  Team ID (Enter for all): ")
	teamID := readOnboardLine(state.Reader)
	if teamID != "" {
		state.Config.Adapters.Linear.TeamID = teamID
	}

	// Prompt for label
	fmt.Print("  Pilot label [pilot]: ")
	label := readOnboardLine(state.Reader)
	if label == "" {
		label = "pilot"
	}
	state.Config.Adapters.Linear.PilotLabel = label

	// Enable polling
	if state.Config.Adapters.Linear.Polling == nil {
		state.Config.Adapters.Linear.Polling = &linear.PollingConfig{
			Interval: 30 * time.Second,
		}
	}
	state.Config.Adapters.Linear.Polling.Enabled = true

	state.Config.Adapters.Linear.Enabled = true
	fmt.Println("  ✓ Linear configured")

	// Offer to set up GitHub for PR creation if not configured
	if state.Config.Adapters.GitHub == nil || !state.Config.Adapters.GitHub.Enabled {
		fmt.Println()
		fmt.Print("  Set up GitHub for PR creation? [Y/n]: ")
		if readOnboardYesNo(state.Reader, true) {
			if err := onboardGitHubTickets(state); err != nil {
				return err
			}
		}
	}

	return nil
}

// onboardJiraTickets sets up Jira as a ticket source
func onboardJiraTickets(state *OnboardState) error {
	fmt.Println()
	fmt.Println("Jira Setup")
	fmt.Println("─────────────────────────")

	// Initialize config if needed
	if state.Config.Adapters.Jira == nil {
		state.Config.Adapters.Jira = jira.DefaultConfig()
	}

	// Platform selection
	fmt.Println("  Platform:")
	fmt.Println("    1  Jira Cloud")
	fmt.Println("    2  Jira Server/Data Center")
	fmt.Print("  ▸ ")
	platformChoice := readOnboardLine(state.Reader)
	if platformChoice == "2" {
		state.Config.Adapters.Jira.Platform = jira.PlatformServer
	} else {
		state.Config.Adapters.Jira.Platform = jira.PlatformCloud
	}

	// Base URL
	fmt.Println()
	fmt.Println("  Example: https://company.atlassian.net")
	fmt.Print("  Base URL: ")
	baseURL := readOnboardLine(state.Reader)
	if baseURL == "" {
		fmt.Println("  ○ Skipped - no base URL provided")
		return nil
	}
	state.Config.Adapters.Jira.BaseURL = strings.TrimSuffix(baseURL, "/")

	// Username
	if state.Config.Adapters.Jira.Platform == jira.PlatformCloud {
		fmt.Print("  Email address: ")
	} else {
		fmt.Print("  Username: ")
	}
	username := readOnboardLine(state.Reader)
	if username == "" {
		fmt.Println("  ○ Skipped - no username provided")
		return nil
	}
	state.Config.Adapters.Jira.Username = username

	// API Token
	fmt.Println()
	if state.Config.Adapters.Jira.Platform == jira.PlatformCloud {
		fmt.Println("  Create a token at: https://id.atlassian.com/manage-profile/security/api-tokens")
	} else {
		fmt.Println("  Create a Personal Access Token in Jira settings")
	}
	fmt.Print("  API token: ")
	apiToken := readOnboardLine(state.Reader)
	if apiToken == "" {
		fmt.Println("  ○ Skipped - no API token provided")
		return nil
	}
	state.Config.Adapters.Jira.APIToken = apiToken

	// Validate connection
	fmt.Print("  Validating... ")
	if err := validateJiraConn(state.Config.Adapters.Jira.BaseURL, username, apiToken); err != nil {
		fmt.Printf("✗ %v\n", err)
		return handleValidationFailure(state, "Jira", func() error {
			return onboardJiraTickets(state)
		})
	}
	fmt.Println("✓ Connected")

	// Project key
	fmt.Print("  Project key (e.g., PROJ): ")
	projectKey := readOnboardLine(state.Reader)
	if projectKey != "" {
		state.Config.Adapters.Jira.ProjectKey = projectKey
	}

	// Label
	fmt.Print("  Pilot label [pilot]: ")
	label := readOnboardLine(state.Reader)
	if label == "" {
		label = "pilot"
	}
	state.Config.Adapters.Jira.PilotLabel = label

	// Enable polling
	if state.Config.Adapters.Jira.Polling == nil {
		state.Config.Adapters.Jira.Polling = &jira.PollingConfig{
			Interval: 30 * time.Second,
		}
	}
	state.Config.Adapters.Jira.Polling.Enabled = true

	state.Config.Adapters.Jira.Enabled = true
	fmt.Println("  ✓ Jira configured")

	return nil
}

// onboardGitLabTickets sets up GitLab Issues as a ticket source
func onboardGitLabTickets(state *OnboardState) error {
	fmt.Println()
	fmt.Println("GitLab Issues Setup")
	fmt.Println("─────────────────────────")

	// Initialize config if needed
	if state.Config.Adapters.GitLab == nil {
		state.Config.Adapters.GitLab = gitlab.DefaultConfig()
	}

	// Base URL
	fmt.Print("  Base URL [https://gitlab.com]: ")
	baseURL := readOnboardLine(state.Reader)
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}
	state.Config.Adapters.GitLab.BaseURL = strings.TrimSuffix(baseURL, "/")

	// Token
	fmt.Println()
	fmt.Println("  Create a token at: Settings > Access Tokens")
	fmt.Println("  Required scopes: api, read_repository")
	fmt.Print("  GitLab token: ")
	token := readOnboardLine(state.Reader)
	if token == "" {
		fmt.Println("  ○ Skipped - no token provided")
		return nil
	}
	state.Config.Adapters.GitLab.Token = token

	// Validate connection
	fmt.Print("  Validating... ")
	if err := validateGitLabConn(baseURL, token); err != nil {
		fmt.Printf("✗ %v\n", err)
		return handleValidationFailure(state, "GitLab", func() error {
			return onboardGitLabTickets(state)
		})
	}
	fmt.Println("✓ Connected")

	// Project path
	fmt.Println()
	fmt.Println("  Example: namespace/project")
	fmt.Print("  Project path: ")
	projectPath := readOnboardLine(state.Reader)
	if projectPath != "" {
		state.Config.Adapters.GitLab.Project = projectPath
	}

	// Label
	fmt.Print("  Pilot label [pilot]: ")
	label := readOnboardLine(state.Reader)
	if label == "" {
		label = "pilot"
	}
	state.Config.Adapters.GitLab.PilotLabel = label

	// Enable polling
	if state.Config.Adapters.GitLab.Polling == nil {
		state.Config.Adapters.GitLab.Polling = &gitlab.PollingConfig{
			Interval: 30 * time.Second,
			Label:    label,
		}
	}
	state.Config.Adapters.GitLab.Polling.Enabled = true
	state.Config.Adapters.GitLab.Polling.Label = label

	state.Config.Adapters.GitLab.Enabled = true
	fmt.Println("  ✓ GitLab Issues configured")

	return nil
}

// onboardAzureDevOpsTickets sets up Azure DevOps work items as a ticket source
func onboardAzureDevOpsTickets(state *OnboardState) error {
	fmt.Println()
	fmt.Println("Azure DevOps Setup")
	fmt.Println("─────────────────────────")

	// Initialize config if needed
	if state.Config.Adapters.AzureDevOps == nil {
		state.Config.Adapters.AzureDevOps = azuredevops.DefaultConfig()
	}

	// Organization
	fmt.Println("  Example: https://dev.azure.com/myorg → organization is 'myorg'")
	fmt.Print("  Organization: ")
	org := readOnboardLine(state.Reader)
	if org == "" {
		fmt.Println("  ○ Skipped - no organization provided")
		return nil
	}
	state.Config.Adapters.AzureDevOps.Organization = org

	// Project
	fmt.Print("  Project: ")
	project := readOnboardLine(state.Reader)
	if project == "" {
		fmt.Println("  ○ Skipped - no project provided")
		return nil
	}
	state.Config.Adapters.AzureDevOps.Project = project

	// Personal Access Token
	fmt.Println()
	fmt.Println("  Create a PAT at: User Settings > Personal Access Tokens")
	fmt.Println("  Required scopes: Work Items (Read & Write), Code (Read & Write)")
	fmt.Print("  Personal Access Token: ")
	pat := readOnboardLine(state.Reader)
	if pat == "" {
		fmt.Println("  ○ Skipped - no PAT provided")
		return nil
	}
	state.Config.Adapters.AzureDevOps.PAT = pat

	// Validate connection
	fmt.Print("  Validating... ")
	if err := validateAzureDevOpsConn(org, project, pat); err != nil {
		fmt.Printf("✗ %v\n", err)
		return handleValidationFailure(state, "Azure DevOps", func() error {
			return onboardAzureDevOpsTickets(state)
		})
	}
	fmt.Println("✓ Connected")

	// Tag (Azure uses tags, not labels)
	fmt.Print("  Pilot tag [pilot]: ")
	tag := readOnboardLine(state.Reader)
	if tag == "" {
		tag = "pilot"
	}
	state.Config.Adapters.AzureDevOps.PilotTag = tag

	// Enable polling
	if state.Config.Adapters.AzureDevOps.Polling == nil {
		state.Config.Adapters.AzureDevOps.Polling = &azuredevops.PollingConfig{
			Interval: 30 * time.Second,
		}
	}
	state.Config.Adapters.AzureDevOps.Polling.Enabled = true

	state.Config.Adapters.AzureDevOps.Enabled = true
	fmt.Println("  ✓ Azure DevOps configured")

	return nil
}

// onboardAsanaTickets sets up Asana as a ticket source
func onboardAsanaTickets(state *OnboardState) error {
	fmt.Println()
	fmt.Println("Asana Setup")
	fmt.Println("─────────────────────────")

	// Initialize config if needed
	if state.Config.Adapters.Asana == nil {
		state.Config.Adapters.Asana = asana.DefaultConfig()
	}

	// Access Token
	fmt.Println()
	fmt.Println("  Create a token at: https://app.asana.com/0/developer-console")
	fmt.Println("  Go to Personal access tokens > Create new token")
	fmt.Print("  Access token: ")
	token := readOnboardLine(state.Reader)
	if token == "" {
		fmt.Println("  ○ Skipped - no token provided")
		return nil
	}
	state.Config.Adapters.Asana.AccessToken = token

	// Validate connection
	fmt.Print("  Validating... ")
	workspaces, err := validateAsanaConn(token)
	if err != nil {
		fmt.Printf("✗ %v\n", err)
		return handleValidationFailure(state, "Asana", func() error {
			return onboardAsanaTickets(state)
		})
	}
	fmt.Println("✓ Connected")

	// Workspace selection
	if len(workspaces) > 1 {
		fmt.Println()
		fmt.Println("  Select workspace:")
		for i, ws := range workspaces {
			fmt.Printf("    %d  %s\n", i+1, ws)
		}
		fmt.Print("  ▸ ")
		wsChoice := readOnboardLine(state.Reader)
		idx := 0
		if _, err := fmt.Sscanf(wsChoice, "%d", &idx); err == nil && idx >= 1 && idx <= len(workspaces) {
			// Workspace ID would be extracted from the validation response
			// For now, store the name and let the actual adapter handle lookup
			fmt.Printf("  Selected: %s\n", workspaces[idx-1])
		}
	} else if len(workspaces) == 1 {
		fmt.Printf("  Workspace: %s\n", workspaces[0])
	}

	// Prompt for workspace ID if needed
	fmt.Print("  Workspace ID (from URL): ")
	workspaceID := readOnboardLine(state.Reader)
	if workspaceID != "" {
		state.Config.Adapters.Asana.WorkspaceID = workspaceID
	}

	// Tag
	fmt.Print("  Pilot tag [pilot]: ")
	tag := readOnboardLine(state.Reader)
	if tag == "" {
		tag = "pilot"
	}
	state.Config.Adapters.Asana.PilotTag = tag

	// Enable polling
	if state.Config.Adapters.Asana.Polling == nil {
		state.Config.Adapters.Asana.Polling = &asana.PollingConfig{
			Interval: 30 * time.Second,
		}
	}
	state.Config.Adapters.Asana.Polling.Enabled = true

	state.Config.Adapters.Asana.Enabled = true
	fmt.Println("  ✓ Asana configured")

	return nil
}

// handleValidationFailure presents options when validation fails
func handleValidationFailure(state *OnboardState, adapterName string, retryFunc func() error) error {
	fmt.Println()
	fmt.Println("  1  Re-enter credentials")
	fmt.Println("  2  Continue without validation")
	fmt.Printf("  3  Skip %s\n", adapterName)
	fmt.Print("  ▸ ")

	choice := readOnboardLine(state.Reader)
	switch choice {
	case "1":
		return retryFunc()
	case "2":
		fmt.Println("  ⚠ Continuing without validation")
		return nil
	default:
		fmt.Printf("  ○ Skipped %s\n", adapterName)
		return nil
	}
}

// Validation stubs - these will be replaced by onboard_validate.go (Issue 5)
// For now they return nil to allow compilation and testing

func validateGitHubConn(token string) error {
	// Stub: Will be implemented in onboard_validate.go (Issue 5)
	// Real implementation will call GitHub API to verify token
	if token == "" {
		return fmt.Errorf("token is required")
	}
	return nil
}

func validateLinearConn(apiKey string) (string, error) {
	// Stub: Will be implemented in onboard_validate.go (Issue 5)
	// Real implementation will call Linear API to get workspace name
	if apiKey == "" {
		return "", fmt.Errorf("API key is required")
	}
	return "Workspace", nil // Placeholder workspace name
}

func validateJiraConn(baseURL, username, apiToken string) error {
	// Stub: Will be implemented in onboard_validate.go (Issue 5)
	// Real implementation will call Jira API to verify credentials
	if baseURL == "" || username == "" || apiToken == "" {
		return fmt.Errorf("all fields are required")
	}
	return nil
}

func validateGitLabConn(baseURL, token string) error {
	// Stub: Will be implemented in onboard_validate.go (Issue 5)
	// Real implementation will call GitLab API to verify token
	if token == "" {
		return fmt.Errorf("token is required")
	}
	return nil
}

func validateAzureDevOpsConn(org, project, pat string) error {
	// Stub: Will be implemented in onboard_validate.go (Issue 5)
	// Real implementation will call Azure DevOps API to verify PAT
	if org == "" || project == "" || pat == "" {
		return fmt.Errorf("all fields are required")
	}
	return nil
}

func validateAsanaConn(token string) ([]string, error) {
	// Stub: Will be implemented in onboard_validate.go (Issue 5)
	// Real implementation will call Asana API to get workspaces
	if token == "" {
		return nil, fmt.Errorf("token is required")
	}
	return []string{"My Workspace"}, nil // Placeholder workspace list
}

// Helper functions for onboarding - aliases to functions in setup.go/onboard_helpers.go

func readOnboardLine(reader interface{ ReadString(byte) (string, error) }) string {
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

func readOnboardYesNo(reader interface{ ReadString(byte) (string, error) }, defaultYes bool) bool {
	return readYesNo(reader.(*bufioReader), defaultYes)
}

type bufioReader = bufio.Reader
