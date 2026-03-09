package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alekspetrov/pilot/internal/config"
)

// ProjectTemplate represents the language/framework template for CLAUDE.md generation.
type ProjectTemplate int

const (
	TemplateGo         ProjectTemplate = iota
	TemplateTypeScript                 // TypeScript/Node
	TemplatePython
	TemplateGeneric
)

// String returns the display name of the template.
func (t ProjectTemplate) String() string {
	switch t {
	case TemplateGo:
		return "Go"
	case TemplateTypeScript:
		return "TypeScript/Node"
	case TemplatePython:
		return "Python"
	default:
		return "Generic"
	}
}

// initProjectData holds all data collected during the project init wizard.
type initProjectData struct {
	ProjectName     string
	ProjectPath     string
	Template        ProjectTemplate
	Conventions     []string
	CommitFormat    string
	Reviewers       []string
	LintCmd         string
	TestCmd         string
	BuildCmd        string
	CreateNavigator bool
}

// runInitProject runs the interactive project scaffolding wizard.
// It generates CLAUDE.md in the current directory, updates ~/.pilot/config.yaml,
// and optionally creates a .agent/ Navigator structure.
func runInitProject(projectPath string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	printStageHeader("PROJECT INIT", 1, 1)
	fmt.Println()

	data := &initProjectData{
		ProjectPath: projectPath,
	}

	// Step 1: Project name
	defaultName := filepath.Base(projectPath)
	data.ProjectName = readLineWithDefault(reader, "Project name", defaultName)

	// Step 2: Language template (auto-detect, offer change)
	detected := detectProjectTemplate(projectPath)
	fmt.Println()
	fmt.Printf("  Detected template: %s\n", onboardValueStyle.Render(detected.String()))
	fmt.Println()
	fmt.Print("  Change template? [y/N] ")
	if readYesNo(reader, false) {
		idx := selectOption(reader, "Select template:", []string{
			"Go",
			"TypeScript/Node",
			"Python",
			"Generic",
		})
		data.Template = ProjectTemplate(idx - 1)
	} else {
		data.Template = detected
	}

	// Step 3: Coding conventions (one per line, blank line to finish)
	fmt.Println()
	fmt.Println("  " + onboardLabelStyle.Render("Coding conventions") + " " + onboardDimStyle.Render("(one per line, empty to finish)"))
	for {
		fmt.Printf("  %s ", onboardCursorStyle.Render("▸"))
		line := readLine(reader)
		if line == "" {
			break
		}
		data.Conventions = append(data.Conventions, line)
	}

	// Step 4: Commit message format
	fmt.Println()
	data.CommitFormat = readLineWithDefault(reader, "Commit format", "type(scope): description")

	// Step 5: PR reviewers (GitHub usernames, comma-separated)
	fmt.Println()
	fmt.Println("  " + onboardLabelStyle.Render("PR reviewers") + " " + onboardDimStyle.Render("(GitHub usernames, comma-separated, optional)"))
	fmt.Printf("  %s ", onboardCursorStyle.Render("▸"))
	reviewersLine := readLine(reader)
	if reviewersLine != "" {
		for _, r := range strings.Split(reviewersLine, ",") {
			r = strings.TrimSpace(r)
			if r != "" {
				data.Reviewers = append(data.Reviewers, r)
			}
		}
	}

	// Step 6: Quality gates
	fmt.Println()
	fmt.Println("  " + onboardLabelStyle.Render("Quality gates"))
	data.LintCmd = readLineWithDefault(reader, "  Lint command", defaultLintCmd(data.Template))
	data.TestCmd = readLineWithDefault(reader, "  Test command", defaultTestCmd(data.Template))
	data.BuildCmd = readLineWithDefault(reader, "  Build command", defaultBuildCmd(data.Template))

	// Step 7: Navigator structure (only ask if not already present)
	if !detectNavigator(projectPath) {
		fmt.Println()
		fmt.Print("  Create .agent/ Navigator structure? [y/N] ")
		data.CreateNavigator = readYesNo(reader, false)
	}

	fmt.Println()
	printStageFooter()
	fmt.Println()

	// Generate CLAUDE.md
	claudePath := filepath.Join(projectPath, "CLAUDE.md")
	claudeContent := buildClaudeMD(data)
	if err := os.WriteFile(claudePath, []byte(claudeContent), 0o644); err != nil {
		return fmt.Errorf("failed to write CLAUDE.md: %w", err)
	}
	fmt.Printf("  %s CLAUDE.md created\n", onboardSuccessStyle.Render("✓"))

	// Create .agent/ structure if requested
	if data.CreateNavigator {
		if err := createNavigatorStructure(projectPath, data); err != nil {
			fmt.Printf("  %s Warning: failed to create .agent/: %v\n", onboardFailStyle.Render("!"), err)
		} else {
			fmt.Printf("  %s .agent/ Navigator structure created\n", onboardSuccessStyle.Render("✓"))
		}
	}

	// Update ~/.pilot/config.yaml
	if err := addProjectToConfig(data); err != nil {
		fmt.Printf("  %s Warning: failed to update Pilot config: %v\n", onboardFailStyle.Render("!"), err)
	} else {
		fmt.Printf("  %s Project added to ~/.pilot/config.yaml\n", onboardSuccessStyle.Render("✓"))
	}

	fmt.Println()
	fmt.Println("  Next steps:")
	fmt.Println("  1. Review and customize CLAUDE.md")
	fmt.Println("  2. Run " + onboardValueStyle.Render("pilot start") + " to begin processing tasks")

	return nil
}

// detectProjectTemplate auto-detects the language template from project files.
func detectProjectTemplate(projectPath string) ProjectTemplate {
	// Go: go.mod present
	if fileExists(filepath.Join(projectPath, "go.mod")) {
		return TemplateGo
	}
	// TypeScript: tsconfig.json or package.json
	if fileExists(filepath.Join(projectPath, "tsconfig.json")) {
		return TemplateTypeScript
	}
	if fileExists(filepath.Join(projectPath, "package.json")) {
		return TemplateTypeScript
	}
	// Python: pyproject.toml, setup.py, or requirements.txt
	if fileExists(filepath.Join(projectPath, "pyproject.toml")) ||
		fileExists(filepath.Join(projectPath, "setup.py")) ||
		fileExists(filepath.Join(projectPath, "requirements.txt")) {
		return TemplatePython
	}
	return TemplateGeneric
}

// fileExists returns true if the path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// defaultLintCmd returns the default lint command for a given template.
func defaultLintCmd(t ProjectTemplate) string {
	switch t {
	case TemplateGo:
		return "golangci-lint run ./..."
	case TemplateTypeScript:
		return "npm run lint"
	case TemplatePython:
		return "ruff check ."
	default:
		return ""
	}
}

// defaultTestCmd returns the default test command for a given template.
func defaultTestCmd(t ProjectTemplate) string {
	switch t {
	case TemplateGo:
		return "go test ./..."
	case TemplateTypeScript:
		return "npm test"
	case TemplatePython:
		return "pytest"
	default:
		return ""
	}
}

// defaultBuildCmd returns the default build command for a given template.
func defaultBuildCmd(t ProjectTemplate) string {
	switch t {
	case TemplateGo:
		return "go build ./..."
	case TemplateTypeScript:
		return "npm run build"
	case TemplatePython:
		return ""
	default:
		return ""
	}
}

// buildClaudeMD generates the CLAUDE.md content from the collected wizard data.
func buildClaudeMD(data *initProjectData) string {
	var sb strings.Builder

	sb.WriteString("# " + data.ProjectName + "\n\n")

	// Language-specific header block
	switch data.Template {
	case TemplateGo:
		sb.WriteString("## Code Standards\n\n")
		sb.WriteString("- Follow standard Go conventions, `go fmt`, `golangci-lint`\n")
		sb.WriteString("- Table-driven tests for Go\n")
	case TemplateTypeScript:
		sb.WriteString("## Code Standards\n\n")
		sb.WriteString("- Follow TypeScript best practices, strict mode enabled\n")
		sb.WriteString("- Use ESLint + Prettier for formatting\n")
		sb.WriteString("- Write tests with Jest or Vitest\n")
	case TemplatePython:
		sb.WriteString("## Code Standards\n\n")
		sb.WriteString("- PEP 8, type hints, dataclasses\n")
		sb.WriteString("- Use Ruff for linting, Black for formatting\n")
		sb.WriteString("- Write tests with pytest\n")
	default:
		sb.WriteString("## Code Standards\n\n")
	}

	// Custom conventions
	for _, c := range data.Conventions {
		sb.WriteString("- " + c + "\n")
	}
	sb.WriteString("\n")

	// Commit format
	sb.WriteString("## Commit Format\n\n")
	sb.WriteString("```\n")
	sb.WriteString(data.CommitFormat + "\n")
	sb.WriteString("```\n\n")

	// Quality gates
	hasGates := data.LintCmd != "" || data.TestCmd != "" || data.BuildCmd != ""
	if hasGates {
		sb.WriteString("## Quality Gates\n\n")
		if data.LintCmd != "" {
			sb.WriteString("```bash\n" + data.LintCmd + "\n```\n\n")
		}
		if data.TestCmd != "" {
			sb.WriteString("```bash\n" + data.TestCmd + "\n```\n\n")
		}
		if data.BuildCmd != "" {
			sb.WriteString("```bash\n" + data.BuildCmd + "\n```\n\n")
		}
	}

	// PR reviewers
	if len(data.Reviewers) > 0 {
		sb.WriteString("## Review Assignments\n\n")
		for _, r := range data.Reviewers {
			sb.WriteString("- @" + r + "\n")
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// createNavigatorStructure creates the .agent/ directory with a basic README.
func createNavigatorStructure(projectPath string, data *initProjectData) error {
	agentDir := filepath.Join(projectPath, ".agent")
	tasksDir := filepath.Join(agentDir, "tasks")
	sopsDir := filepath.Join(agentDir, "sops")

	for _, dir := range []string{agentDir, tasksDir, sopsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	readmeContent := "# Navigator Index\n\n" +
		"## Project: " + data.ProjectName + "\n\n" +
		"## Key Docs\n\n" +
		"- `tasks/` — Implementation plans\n" +
		"- `sops/` — Standard Operating Procedures\n\n" +
		"## Quick Commands\n\n" +
		"```bash\n" +
		"/nav-start          # Start session, load context\n" +
		"/nav-task \"feature\" # Plan implementation\n" +
		"```\n"

	readmePath := filepath.Join(agentDir, "DEVELOPMENT-README.md")
	return os.WriteFile(readmePath, []byte(readmeContent), 0o644)
}

// addProjectToConfig adds the project to ~/.pilot/config.yaml.
// Creates the config file with defaults if it does not exist.
func addProjectToConfig(data *initProjectData) error {
	configPath := config.DefaultConfigPath()

	cfg, err := config.Load(configPath)
	if err != nil {
		cfg = config.DefaultConfig()
	}

	// Check for duplicate project path or name
	for _, existing := range cfg.Projects {
		if existing.Path == data.ProjectPath || existing.Name == data.ProjectName {
			return nil // Already registered, skip silently
		}
	}

	proj := &config.ProjectConfig{
		Name:          data.ProjectName,
		Path:          data.ProjectPath,
		Navigator:     data.CreateNavigator || detectNavigator(data.ProjectPath),
		DefaultBranch: detectDefaultBranch(data.ProjectPath),
	}

	// Parse git remote for GitHub owner/repo
	owner, repo, err := detectGitRemote(data.ProjectPath)
	if err == nil && owner != "" && repo != "" {
		proj.GitHub = &config.ProjectGitHubConfig{
			Owner: owner,
			Repo:  repo,
		}
	}

	cfg.Projects = append(cfg.Projects, proj)

	if len(cfg.Projects) == 1 {
		cfg.DefaultProject = data.ProjectName
	}

	return config.Save(cfg, configPath)
}
