package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/config"
)

// ghRunner executes a gh CLI subcommand and returns stdout.
// It is a package-level variable so tests can inject a fake implementation.
var ghRunner = func(args ...string) ([]byte, error) {
	return exec.Command("gh", args...).Output() //nolint:gosec
}

// ghRepoEntry represents one entry from `gh repo list --json`.
type ghRepoEntry struct {
	NameWithOwner    string `json:"nameWithOwner"`
	Description      string `json:"description"`
	IsPrivate        bool   `json:"isPrivate"`
	DefaultBranchRef *struct {
		Name string `json:"name"`
	} `json:"defaultBranchRef"`
}

// ghAuthStatus checks whether the gh CLI is installed and the user is authenticated.
// Returns (username, true) on success; ("", false) when gh is absent or unauthenticated.
func ghAuthStatus() (string, bool) {
	if _, err := ghRunner("auth", "status"); err != nil {
		return "", false
	}
	out, err := ghRunner("api", "user", "-q", ".login")
	if err != nil {
		// Authenticated but couldn't fetch username (network offline, etc.).
		return "", true
	}
	return strings.TrimSpace(string(out)), true
}

// ghAuthToken returns the token stored in the gh CLI keychain.
func ghAuthToken() (string, error) {
	out, err := ghRunner("auth", "token")
	if err != nil {
		return "", fmt.Errorf("gh auth token: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ghRepoList fetches up to limit repos from the gh CLI.
func ghRepoList(limit int) ([]ghRepoEntry, error) {
	out, err := ghRunner(
		"repo", "list",
		"--limit", fmt.Sprintf("%d", limit),
		"--json", "nameWithOwner,description,isPrivate,defaultBranchRef",
	)
	if err != nil {
		return nil, fmt.Errorf("gh repo list: %w", err)
	}
	var repos []ghRepoEntry
	if err := json.Unmarshal(out, &repos); err != nil {
		return nil, fmt.Errorf("gh repo list: parse: %w", err)
	}
	return repos, nil
}

// isInteractiveTTY returns true when stdin is connected to a terminal.
func isInteractiveTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// runProjectAddWizard runs the interactive wizard for `pilot project add` (no flags).
// It guides the user through gh auth detection, repo picking, and config writing.
func runProjectAddWizard(_ *cobra.Command) error {
	reader := bufio.NewReader(os.Stdin)

	configPath := cfgFile
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	fmt.Println()
	fmt.Println(onboardLabelStyle.Render("  pilot project add — interactive wizard"))
	fmt.Println()

	// ── gh auth detection ────────────────────────────────────────────────────
	username, ghOK := ghAuthStatus()
	if !ghOK {
		fmt.Printf("  %s gh CLI not found or unauthenticated.\n",
			onboardDimStyle.Render("!"))
		fmt.Println("  Hint: install gh (https://cli.github.com) for the interactive picker.")
		fmt.Println()
		fmt.Println("  Use: pilot project add --name <name> [--github owner/repo] [--path <dir>]")
		return nil
	}

	if username != "" {
		fmt.Printf("  %s gh authenticated (user: %s)\n",
			onboardSuccessStyle.Render("✓"), username)
	} else {
		fmt.Printf("  %s gh authenticated\n", onboardSuccessStyle.Render("✓"))
	}

	// Offer token seeding if no token is configured yet
	var seedToken bool
	hasToken := cfg.Adapters != nil && cfg.Adapters.GitHub != nil &&
		cfg.Adapters.GitHub.Token != ""
	if !hasToken {
		fmt.Printf("  %s No GitHub token in %s\n",
			onboardDimStyle.Render("○"), configPath)
		fmt.Println()
		fmt.Print("  Use your gh CLI token for Pilot? [Y/n] ")
		seedToken = readYesNo(reader, true)
		fmt.Println()
	}

	// ── repo picker ──────────────────────────────────────────────────────────
	var selectedRepo *ghRepoEntry
	var ghConfig *config.ProjectGitHubConfig

	repos, listErr := ghRepoList(50)
	if listErr != nil || len(repos) == 0 {
		fmt.Printf("  %s Could not list repos — will auto-detect from git remote.\n",
			onboardDimStyle.Render("!"))
	} else {
		fmt.Println()
		fmt.Println("  Pick a repo:")
		fmt.Println()

		displayCount := len(repos)
		if displayCount > 20 {
			displayCount = 20
		}
		for i, r := range repos[:displayCount] {
			visibility := "public"
			if r.IsPrivate {
				visibility = "private"
			}
			desc := r.Description
			if len(desc) > 48 {
				desc = desc[:45] + "..."
			}
			if desc != "" {
				fmt.Printf("    %s  %-35s %s  %s\n",
					onboardValueStyle.Render(fmt.Sprintf("[%d]", i+1)),
					r.NameWithOwner,
					onboardDimStyle.Render("("+visibility+")"),
					onboardDimStyle.Render(desc),
				)
			} else {
				fmt.Printf("    %s  %-35s %s\n",
					onboardValueStyle.Render(fmt.Sprintf("[%d]", i+1)),
					r.NameWithOwner,
					onboardDimStyle.Render("("+visibility+")"),
				)
			}
		}
		if len(repos) > displayCount {
			fmt.Printf("    %s\n",
				onboardDimStyle.Render(fmt.Sprintf("↓ %d more (use --github to specify)", len(repos)-displayCount)))
		}
		fmt.Println()
		fmt.Printf("  Select repo number (or Enter to skip) %s ", onboardCursorStyle.Render("▸"))

		line := readLine(reader)
		if line != "" {
			var idx int
			if n, _ := fmt.Sscanf(line, "%d", &idx); n == 1 && idx >= 1 && idx <= displayCount {
				entry := repos[idx-1]
				selectedRepo = &entry
				parts := strings.SplitN(selectedRepo.NameWithOwner, "/", 2)
				if len(parts) == 2 {
					ghConfig = &config.ProjectGitHubConfig{
						Owner: parts[0],
						Repo:  parts[1],
					}
				}
			}
		}
		fmt.Println()
	}

	// ── project name ─────────────────────────────────────────────────────────
	cwd, _ := os.Getwd()
	defaultName := filepath.Base(cwd)
	if selectedRepo != nil {
		parts := strings.SplitN(selectedRepo.NameWithOwner, "/", 2)
		if len(parts) == 2 {
			defaultName = parts[1]
		}
	}
	name := readLineWithDefault(reader, "Project name", defaultName)
	if name == "" {
		name = defaultName
	}

	// ── local path ───────────────────────────────────────────────────────────
	projectPath := readLineWithDefault(reader, "Local path", cwd)
	if projectPath == "" {
		projectPath = cwd
	}
	projectPath = expandProjectPath(projectPath)

	info, err := os.Stat(projectPath)
	if err != nil {
		return fmt.Errorf("path does not exist: %s", projectPath)
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory: %s", projectPath)
	}

	// ── duplicate checks ─────────────────────────────────────────────────────
	if cfg.GetProjectByName(name) != nil {
		return fmt.Errorf("project '%s' already exists", name)
	}
	if cfg.GetProject(projectPath) != nil {
		return fmt.Errorf("path already configured: %s", projectPath)
	}

	// Fall back to git-remote detection if no repo was picked
	if ghConfig == nil {
		ghConfig = detectGitHubFromRemote(projectPath)
	}

	// Branch: prefer the selected repo's default branch, then auto-detect
	branch := ""
	if selectedRepo != nil && selectedRepo.DefaultBranchRef != nil {
		branch = selectedRepo.DefaultBranchRef.Name
	}
	if branch == "" {
		branch = detectDefaultBranch(projectPath)
	}

	navigator := detectNavigator(projectPath)

	// ── set as default? ──────────────────────────────────────────────────────
	fmt.Println()
	fmt.Print("  Set as default project? [Y/n] ")
	setDefault := readYesNo(reader, true)

	// ── summary + confirm ────────────────────────────────────────────────────
	navStr := "disabled"
	if navigator {
		navStr = "enabled"
	}
	defaultStr := "no"
	if setDefault || len(cfg.Projects) == 0 {
		defaultStr = "yes"
	}

	fmt.Println()
	fmt.Println("  " + onboardLabelStyle.Render("Summary"))
	fmt.Println()
	fmt.Printf("    Name:       %s\n", onboardValueStyle.Render(name))
	fmt.Printf("    Path:       %s\n", onboardValueStyle.Render(projectPath))
	if ghConfig != nil {
		fmt.Printf("    GitHub:     %s/%s\n",
			onboardValueStyle.Render(ghConfig.Owner), onboardValueStyle.Render(ghConfig.Repo))
	}
	fmt.Printf("    Branch:     %s\n", onboardValueStyle.Render(branch))
	fmt.Printf("    Navigator:  %s\n", onboardValueStyle.Render(navStr))
	fmt.Printf("    Default:    %s\n", onboardValueStyle.Render(defaultStr))
	if seedToken {
		fmt.Printf("    gh token:   %s\n", onboardValueStyle.Render("will be seeded"))
	}
	fmt.Println()
	fmt.Print("  Save to config? [Y/n] ")
	if !readYesNo(reader, true) {
		fmt.Println("  Cancelled.")
		return nil
	}
	fmt.Println()

	// ── seed token ───────────────────────────────────────────────────────────
	if seedToken {
		token, err := ghAuthToken()
		if err != nil {
			fmt.Printf("  %s Could not get gh token: %v\n", onboardFailStyle.Render("✗"), err)
		} else if token != "" {
			if cfg.Adapters == nil {
				cfg.Adapters = &config.AdaptersConfig{}
			}
			if cfg.Adapters.GitHub == nil {
				cfg.Adapters.GitHub = github.DefaultConfig()
			}
			cfg.Adapters.GitHub.Token = token
			fmt.Printf("  %s Wrote adapters.github.token\n", onboardSuccessStyle.Render("✓"))
		}
	}

	// ── add project ───────────────────────────────────────────────────────────
	proj := &config.ProjectConfig{
		Name:          name,
		Path:          projectPath,
		Navigator:     navigator,
		DefaultBranch: branch,
		GitHub:        ghConfig,
	}
	cfg.Projects = append(cfg.Projects, proj)
	if setDefault || len(cfg.Projects) == 1 {
		cfg.DefaultProject = name
	}

	if err := config.Save(cfg, configPath); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// ── success ───────────────────────────────────────────────────────────────
	fmt.Printf("  %s Project added: %s\n", onboardSuccessStyle.Render("✓"), name)
	fmt.Printf("      Path:      %s\n", projectPath)
	if ghConfig != nil {
		fmt.Printf("      GitHub:    %s/%s\n", ghConfig.Owner, ghConfig.Repo)
	}
	fmt.Printf("      Branch:    %s\n", branch)
	fmt.Printf("      Navigator: %s\n", navStr)
	if defaultStr == "yes" {
		fmt.Println("      Default:   yes")
	}
	fmt.Println()
	fmt.Printf("  → Start working:  pilot start --project %s --github\n", name)

	return nil
}
