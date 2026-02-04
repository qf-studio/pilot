package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/alekspetrov/pilot/internal/config"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage Pilot projects",
		Long:  `Add, list, remove, and configure projects for Pilot.`,
	}

	cmd.AddCommand(
		newProjectListCmd(),
		newProjectAddCmd(),
		newProjectRemoveCmd(),
		newProjectSetDefaultCmd(),
		newProjectShowCmd(),
	)

	return cmd
}

func newProjectListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all configured projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			if len(cfg.Projects) == 0 {
				fmt.Println("No projects configured.")
				fmt.Println()
				fmt.Println("Add a project with: pilot project add --name <name>")
				return nil
			}

			fmt.Printf("PROJECTS (%d configured)\n\n", len(cfg.Projects))

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintf(w, "  NAME\tPATH\tGITHUB\tBRANCH\tNAV\tDEFAULT\n")

			for _, proj := range cfg.Projects {
				navIcon := ""
				if proj.Navigator {
					navIcon = "*"
				}
				defaultIcon := ""
				if proj.Name == cfg.DefaultProject {
					defaultIcon = "*"
				}
				githubStr := ""
				if proj.GitHub != nil {
					githubStr = fmt.Sprintf("%s/%s", proj.GitHub.Owner, proj.GitHub.Repo)
				}
				_, _ = fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
					proj.Name,
					truncatePath(proj.Path, 40),
					githubStr,
					proj.DefaultBranch,
					navIcon,
					defaultIcon,
				)
			}
			_ = w.Flush()

			fmt.Println()
			fmt.Println("Use 'pilot project show <name>' for details")

			return nil
		},
	}
}

func newProjectAddCmd() *cobra.Command {
	var (
		name       string
		path       string
		github     string
		branch     string
		navigator  bool
		setDefault bool
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a new project",
		Long: `Add a new project to Pilot configuration.

Auto-detection:
  - If --path is omitted, uses current working directory
  - If --branch is omitted, detects from git remote
  - If --navigator is omitted, checks for .agent/ directory
  - If --github is omitted, parses from git remote origin

Examples:
  pilot project add --name my-app
  pilot project add --name my-app --github owner/repo
  pilot project add -n my-app -p /path/to/project -g owner/repo -b main`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}

			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Check for duplicate name
			if cfg.GetProjectByName(name) != nil {
				return fmt.Errorf("project '%s' already exists", name)
			}

			// Use current directory if path not specified
			if path == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("failed to get current directory: %w", err)
				}
				path = cwd
			}

			// Expand and validate path
			path = expandProjectPath(path)
			info, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("path does not exist: %s", path)
				}
				return fmt.Errorf("failed to access path: %w", err)
			}
			if !info.IsDir() {
				return fmt.Errorf("path is not a directory: %s", path)
			}

			// Check for duplicate path
			if cfg.GetProject(path) != nil {
				return fmt.Errorf("path already configured: %s", path)
			}

			// Auto-detect GitHub if not specified
			var ghConfig *config.ProjectGitHubConfig
			if github != "" {
				parts := strings.SplitN(github, "/", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid GitHub format, expected owner/repo: %s", github)
				}
				ghConfig = &config.ProjectGitHubConfig{
					Owner: parts[0],
					Repo:  parts[1],
				}
			} else {
				// Try to auto-detect from git remote
				ghConfig = detectGitHubFromRemote(path)
			}

			// Auto-detect branch if not specified
			if branch == "" {
				branch = detectDefaultBranch(path)
			}

			// Auto-detect navigator if flag not explicitly set
			if !cmd.Flags().Changed("navigator") {
				navigator = detectNavigator(path)
			}

			// Create project config
			proj := &config.ProjectConfig{
				Name:          name,
				Path:          path,
				Navigator:     navigator,
				DefaultBranch: branch,
				GitHub:        ghConfig,
			}

			// Add to config
			cfg.Projects = append(cfg.Projects, proj)

			// Set as default if requested or if it's the only project
			if setDefault || len(cfg.Projects) == 1 {
				cfg.DefaultProject = name
			}

			// Save config
			if err := config.Save(cfg, configPath); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			// Print success message
			fmt.Printf("Project added: %s\n", name)
			fmt.Printf("   Path:      %s\n", path)
			if ghConfig != nil {
				fmt.Printf("   GitHub:    %s/%s\n", ghConfig.Owner, ghConfig.Repo)
			}
			if branch != "" {
				fmt.Printf("   Branch:    %s\n", branch)
			}
			navStr := "disabled"
			if navigator {
				navStr = "enabled"
			}
			fmt.Printf("   Navigator: %s\n", navStr)
			fmt.Println()
			fmt.Printf("   Start working: pilot start --project %s\n", name)

			return nil
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Project name (required)")
	cmd.Flags().StringVarP(&path, "path", "p", "", "Project path (default: current directory)")
	cmd.Flags().StringVarP(&github, "github", "g", "", "GitHub repo (owner/repo)")
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "Default branch (auto-detected)")
	cmd.Flags().BoolVar(&navigator, "navigator", false, "Enable Navigator (auto-detected)")
	cmd.Flags().BoolVarP(&setDefault, "set-default", "d", false, "Set as default project")

	return cmd
}

func newProjectRemoveCmd() *cobra.Command {
	var (
		name  string
		force bool
	)

	cmd := &cobra.Command{
		Use:   "remove [name]",
		Short: "Remove a project",
		Long: `Remove a project from Pilot configuration.

Examples:
  pilot project remove my-app
  pilot project remove --name my-app
  pilot project remove my-app --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Get name from positional arg or flag
			if len(args) > 0 {
				name = args[0]
			}
			if name == "" {
				return fmt.Errorf("project name required")
			}

			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Find project
			proj := cfg.GetProjectByName(name)
			if proj == nil {
				return fmt.Errorf("project not found: %s", name)
			}

			// Confirm removal unless --force
			if !force {
				fmt.Printf("Remove project '%s'? [y/N] ", name)
				reader := bufio.NewReader(os.Stdin)
				response, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("failed to read response: %w", err)
				}
				response = strings.TrimSpace(strings.ToLower(response))
				if response != "y" && response != "yes" {
					fmt.Println("Cancelled.")
					return nil
				}
			}

			// Remove project
			newProjects := make([]*config.ProjectConfig, 0, len(cfg.Projects)-1)
			for _, p := range cfg.Projects {
				if !strings.EqualFold(p.Name, name) {
					newProjects = append(newProjects, p)
				}
			}
			cfg.Projects = newProjects

			// Clear default if it was the removed project
			if strings.EqualFold(cfg.DefaultProject, name) {
				cfg.DefaultProject = ""
				if len(cfg.Projects) > 0 {
					cfg.DefaultProject = cfg.Projects[0].Name
				}
			}

			// Save config
			if err := config.Save(cfg, configPath); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			fmt.Printf("Project removed: %s\n", name)
			return nil
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Project name")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")

	return cmd
}

func newProjectSetDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-default <name>",
		Short: "Set the default project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Find project
			proj := cfg.GetProjectByName(name)
			if proj == nil {
				return fmt.Errorf("project not found: %s", name)
			}

			// Set default
			cfg.DefaultProject = proj.Name

			// Save config
			if err := config.Save(cfg, configPath); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			fmt.Printf("Default project set: %s\n", proj.Name)
			return nil
		},
	}
}

func newProjectShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show [name]",
		Short: "Show project details",
		Long: `Show details for a project.

Without arguments, shows the default project.

Examples:
  pilot project show my-app
  pilot project show`,
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			var proj *config.ProjectConfig
			if len(args) > 0 {
				proj = cfg.GetProjectByName(args[0])
				if proj == nil {
					return fmt.Errorf("project not found: %s", args[0])
				}
			} else {
				proj = cfg.GetDefaultProject()
				if proj == nil {
					return fmt.Errorf("no default project configured")
				}
			}

			fmt.Printf("PROJECT: %s\n\n", proj.Name)
			fmt.Printf("  Path:       %s\n", proj.Path)
			if proj.GitHub != nil {
				fmt.Printf("  GitHub:     %s/%s\n", proj.GitHub.Owner, proj.GitHub.Repo)
			}
			if proj.DefaultBranch != "" {
				fmt.Printf("  Branch:     %s\n", proj.DefaultBranch)
			}
			navStr := "disabled"
			if proj.Navigator {
				navStr = "enabled"
			}
			fmt.Printf("  Navigator:  %s\n", navStr)
			defaultStr := "no"
			if proj.Name == cfg.DefaultProject {
				defaultStr = "yes"
			}
			fmt.Printf("  Default:    %s\n", defaultStr)

			return nil
		},
	}
}

// Helper functions

func truncatePath(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	// Try to show the end of the path with ...
	return "..." + path[len(path)-(maxLen-3):]
}

func expandProjectPath(path string) string {
	if strings.HasPrefix(path, "~") {
		homeDir, _ := os.UserHomeDir()
		return filepath.Join(homeDir, path[1:])
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return absPath
}

func detectGitHubFromRemote(path string) *config.ProjectGitHubConfig {
	cmd := exec.Command("git", "-C", path, "remote", "get-url", "origin")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	url := strings.TrimSpace(string(output))
	return parseGitHubURL(url)
}

func parseGitHubURL(url string) *config.ProjectGitHubConfig {
	// Handle SSH format: git@github.com:owner/repo.git
	if strings.HasPrefix(url, "git@github.com:") {
		url = strings.TrimPrefix(url, "git@github.com:")
		url = strings.TrimSuffix(url, ".git")
		parts := strings.SplitN(url, "/", 2)
		if len(parts) == 2 {
			return &config.ProjectGitHubConfig{
				Owner: parts[0],
				Repo:  parts[1],
			}
		}
	}

	// Handle HTTPS format: https://github.com/owner/repo.git
	if strings.Contains(url, "github.com/") {
		idx := strings.Index(url, "github.com/")
		url = url[idx+len("github.com/"):]
		url = strings.TrimSuffix(url, ".git")
		parts := strings.SplitN(url, "/", 2)
		if len(parts) == 2 {
			return &config.ProjectGitHubConfig{
				Owner: parts[0],
				Repo:  parts[1],
			}
		}
	}

	return nil
}

func detectDefaultBranch(path string) string {
	// Try to get default branch from remote
	cmd := exec.Command("git", "-C", path, "symbolic-ref", "refs/remotes/origin/HEAD")
	output, err := cmd.Output()
	if err == nil {
		ref := strings.TrimSpace(string(output))
		// Extract branch name from refs/remotes/origin/main
		parts := strings.Split(ref, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}

	// Fallback: check if main or master exists
	for _, branch := range []string{"main", "master"} {
		cmd := exec.Command("git", "-C", path, "rev-parse", "--verify", fmt.Sprintf("refs/heads/%s", branch))
		if err := cmd.Run(); err == nil {
			return branch
		}
	}

	return "main" // Default fallback
}

func detectNavigator(path string) bool {
	agentPath := filepath.Join(path, ".agent")
	info, err := os.Stat(agentPath)
	return err == nil && info.IsDir()
}
