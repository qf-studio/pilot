package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/alekspetrov/pilot/internal/config"
)

// onboardProjectSetup implements the project setup stage of onboarding.
// It auto-detects git repos in the current directory, allows manual path entry,
// and configures project settings including Navigator detection.
func onboardProjectSetup(state *OnboardState) error {
	printStageHeader("PROJECT", state.CurrentStage, state.StagesTotal)
	fmt.Println()

	cfg := state.Config
	reader := state.Reader

	// Check if projects already configured
	if len(cfg.Projects) > 0 {
		fmt.Println("  Existing projects:")
		fmt.Println()
		for _, proj := range cfg.Projects {
			nav := ""
			if proj.Navigator {
				nav = " [Navigator]"
			}
			fmt.Printf("    %s %s%s\n",
				onboardSuccessStyle.Render("•"),
				proj.Name,
				onboardDimStyle.Render(nav))
		}
		fmt.Println()

		// Ask to add more
		fmt.Print("  Add more projects? [y/N] ")
		if !readYesNo(reader, false) {
			fmt.Println()
			printStageFooter()
			return nil
		}
		fmt.Println()
	}

	// Auto-detect from current directory
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}

	// Loop to add projects
	for {
		var projectPath string
		var useDetected bool

		// Try auto-detection if we have a cwd
		if cwd != "" && isGitRepo(cwd) {
			owner, repo, _ := detectGitRemote(cwd)
			branch := detectDefaultBranch(cwd)

			fmt.Println("  Detected repo in current directory:")
			fmt.Println()
			fmt.Printf("    %s     %s\n", onboardLabelStyle.Render("Path"), onboardValueStyle.Render(cwd))
			if owner != "" && repo != "" {
				fmt.Printf("    %s   %s/%s\n", onboardLabelStyle.Render("Remote"), onboardValueStyle.Render(owner), onboardValueStyle.Render(repo))
			}
			fmt.Printf("    %s   %s\n", onboardLabelStyle.Render("Branch"), onboardValueStyle.Render(branch))
			fmt.Println()

			fmt.Print("  Use this project? [Y/n] ")
			if readYesNo(reader, true) {
				projectPath = cwd
				useDetected = true
			}
			fmt.Println()
		}

		// Manual entry if not using detected
		if !useDetected {
			fmt.Print("  Project path " + onboardCursorStyle.Render("▸") + " ")
			projectPath = readLine(reader)
			if projectPath == "" {
				// No more projects to add
				break
			}
			projectPath = expandPath(projectPath)

			// Validate path exists
			info, err := os.Stat(projectPath)
			if err != nil {
				fmt.Printf("    %s Path does not exist\n", onboardFailStyle.Render("✗"))
				fmt.Println()
				continue
			}
			if !info.IsDir() {
				fmt.Printf("    %s Path is not a directory\n", onboardFailStyle.Render("✗"))
				fmt.Println()
				continue
			}
		}

		// Get project name (default: directory name)
		defaultName := filepath.Base(projectPath)
		name := readLineWithDefault(reader, "Project name", defaultName)

		// Check for duplicate name
		for _, existing := range cfg.Projects {
			if existing.Name == name {
				fmt.Printf("    %s Project '%s' already exists\n", onboardFailStyle.Render("✗"), name)
				fmt.Println()
				continue
			}
		}

		// Detect Navigator (.agent directory)
		hasNavigator := detectNavigator(projectPath)

		// Detect default branch
		defaultBranch := detectDefaultBranch(projectPath)

		// Build project config
		proj := &config.ProjectConfig{
			Name:          name,
			Path:          projectPath,
			Navigator:     hasNavigator,
			DefaultBranch: defaultBranch,
		}

		// Parse git remote for owner/repo
		owner, repo, err := detectGitRemote(projectPath)
		if err == nil && owner != "" && repo != "" {
			proj.GitHub = &config.ProjectGitHubConfig{
				Owner: owner,
				Repo:  repo,
			}
		}

		// Add to config
		cfg.Projects = append(cfg.Projects, proj)

		// Show success
		fmt.Println()
		if hasNavigator {
			fmt.Printf("    %s Navigator detected\n", onboardSuccessStyle.Render("✓"))
		}
		fmt.Printf("    %s Project %q added\n", onboardSuccessStyle.Render("✓"), name)
		fmt.Println()

		// Ask to add another
		// Enterprise persona defaults to yes, others default to no
		defaultAddMore := state.Persona == PersonaEnterprise
		if defaultAddMore {
			fmt.Print("  Add another project? [Y/n] ")
		} else {
			fmt.Print("  Add another project? [y/N] ")
		}
		if !readYesNo(reader, defaultAddMore) {
			break
		}
		fmt.Println()

		// Clear cwd for next iteration to force manual entry
		cwd = ""
	}

	fmt.Println()
	printStageFooter()
	return nil
}

// isGitRepo checks if the given path is inside a git repository
func isGitRepo(path string) bool {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--git-dir")
	return cmd.Run() == nil
}

// Note: detectNavigator is defined in project.go
