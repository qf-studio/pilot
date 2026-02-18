// Package executor provides task execution with Navigator integration.
package executor

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed templates/*
var embeddedTemplates embed.FS

// NavigatorConfig holds auto-init configuration.
type NavigatorConfig struct {
	AutoInit      bool   `yaml:"auto_init"`      // Enable auto-init (default: true)
	TemplatesPath string `yaml:"templates_path"` // Override plugin templates location
}

// DefaultNavigatorConfig returns default Navigator configuration.
func DefaultNavigatorConfig() *NavigatorConfig {
	return &NavigatorConfig{
		AutoInit: true,
	}
}

// ProjectInfo holds detected project metadata.
type ProjectInfo struct {
	Name         string `json:"name"`
	TechStack    string `json:"tech_stack"`
	DetectedFrom string `json:"detected_from"`
}

// NavigatorInitializer handles Navigator structure creation.
type NavigatorInitializer struct {
	templatesPath string
	log           *slog.Logger
}

// NewNavigatorInitializer creates an initializer with plugin templates.
func NewNavigatorInitializer(log *slog.Logger) (*NavigatorInitializer, error) {
	templatesPath, err := FindTemplatesPath()
	if err != nil {
		// Fall back to embedded templates
		log.Debug("Using embedded templates", slog.Any("error", err))
		return &NavigatorInitializer{
			templatesPath: "", // Empty means use embedded
			log:           log,
		}, nil
	}

	return &NavigatorInitializer{
		templatesPath: templatesPath,
		log:           log,
	}, nil
}

// FindTemplatesPath locates Navigator plugin templates.
func FindTemplatesPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot get home directory: %w", err)
	}

	// Check standard plugin locations
	pluginBase := filepath.Join(homeDir, ".claude", "plugins", "cache", "navigator-marketplace", "navigator")

	// Find latest version directory
	entries, err := os.ReadDir(pluginBase)
	if err != nil {
		return "", fmt.Errorf("navigator plugin not found: %w", err)
	}

	var latestVersion string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "6.") {
			if entry.Name() > latestVersion {
				latestVersion = entry.Name()
			}
		}
	}

	if latestVersion == "" {
		return "", fmt.Errorf("no navigator version found in %s", pluginBase)
	}

	templatesPath := filepath.Join(pluginBase, latestVersion, "templates")
	if _, err := os.Stat(templatesPath); err != nil {
		return "", fmt.Errorf("templates not found: %w", err)
	}

	return templatesPath, nil
}

// IsInitialized checks if .agent/ exists and has valid structure.
func (n *NavigatorInitializer) IsInitialized(projectPath string) bool {
	agentDir := filepath.Join(projectPath, ".agent")
	info, err := os.Stat(agentDir)
	if err != nil || !info.IsDir() {
		return false
	}

	// Check for required files
	devReadme := filepath.Join(agentDir, "DEVELOPMENT-README.md")
	if _, err := os.Stat(devReadme); err != nil {
		return false
	}

	return true
}

// Initialize creates .agent/ structure from templates.
func (n *NavigatorInitializer) Initialize(projectPath string) error {
	if n.IsInitialized(projectPath) {
		n.log.Debug("Navigator already initialized", slog.String("path", projectPath))
		return nil
	}

	// Detect project info
	info, err := n.DetectProjectInfo(projectPath)
	if err != nil {
		n.log.Warn("Project detection failed, using defaults", slog.Any("error", err))
		info = &ProjectInfo{
			Name:         filepath.Base(projectPath),
			TechStack:    "Unknown",
			DetectedFrom: "directory_name",
		}
	}

	n.log.Info("Initializing Navigator",
		slog.String("project", info.Name),
		slog.String("tech_stack", info.TechStack),
		slog.String("detected_from", info.DetectedFrom),
	)

	// Create directory structure
	agentDir := filepath.Join(projectPath, ".agent")
	dirs := []string{
		agentDir,
		filepath.Join(agentDir, "tasks"),
		filepath.Join(agentDir, "system"),
		filepath.Join(agentDir, "sops"),
		filepath.Join(agentDir, "sops", "integrations"),
		filepath.Join(agentDir, "sops", "debugging"),
		filepath.Join(agentDir, "sops", "development"),
		filepath.Join(agentDir, "sops", "deployment"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Create .gitkeep files for empty directories
	gitkeepDirs := []string{
		filepath.Join(agentDir, "tasks"),
		filepath.Join(agentDir, "system"),
		filepath.Join(agentDir, "sops", "integrations"),
		filepath.Join(agentDir, "sops", "debugging"),
		filepath.Join(agentDir, "sops", "development"),
		filepath.Join(agentDir, "sops", "deployment"),
	}

	for _, dir := range gitkeepDirs {
		gitkeep := filepath.Join(dir, ".gitkeep")
		if err := os.WriteFile(gitkeep, []byte{}, 0644); err != nil {
			n.log.Warn("Failed to create .gitkeep", slog.String("path", gitkeep))
		}
	}

	// Copy and customize templates — continue on individual failures
	var initErrors []string

	if err := n.copyTemplate("DEVELOPMENT-README.md", filepath.Join(agentDir, "DEVELOPMENT-README.md"), info); err != nil {
		n.log.Warn("Failed to copy DEVELOPMENT-README.md", slog.Any("error", err))
		initErrors = append(initErrors, err.Error())
	}

	// Create .nav-config.json
	if err := n.createNavConfig(agentDir, info); err != nil {
		n.log.Warn("Failed to create .nav-config.json", slog.Any("error", err))
		initErrors = append(initErrors, err.Error())
	}

	// Create .gitignore for .agent/
	gitignoreContent := `# Navigator session-specific data
.context-markers/
*.log
`
	if err := os.WriteFile(filepath.Join(agentDir, ".gitignore"), []byte(gitignoreContent), 0644); err != nil {
		n.log.Warn("Failed to create .gitignore", slog.Any("error", err))
	}

	if len(initErrors) > 0 {
		return fmt.Errorf("partial init (%d errors): %s", len(initErrors), strings.Join(initErrors, "; "))
	}

	n.log.Info("Navigator initialized successfully",
		slog.String("path", agentDir),
	)

	return nil
}

// copyTemplate copies a template file with variable substitution.
func (n *NavigatorInitializer) copyTemplate(templateName, destPath string, info *ProjectInfo) error {
	var content []byte
	var err error

	if n.templatesPath != "" {
		// Read from plugin templates
		content, err = os.ReadFile(filepath.Join(n.templatesPath, templateName))
	} else {
		// Read from embedded templates — use forward slashes (embed.FS requirement, not filepath)
		content, err = fs.ReadFile(embeddedTemplates, "templates/"+templateName)
	}

	if err != nil {
		return fmt.Errorf("failed to read template %s: %w", templateName, err)
	}

	// Customize template
	customized := n.customizeTemplate(string(content), info)

	return os.WriteFile(destPath, []byte(customized), 0644)
}

// customizeTemplate replaces placeholders with project info.
func (n *NavigatorInitializer) customizeTemplate(content string, info *ProjectInfo) string {
	now := time.Now()

	replacements := map[string]string{
		"[Project Name]":              info.Name,
		"${PROJECT_NAME}":             info.Name,
		"${project_name}":             strings.ToLower(strings.ReplaceAll(info.Name, " ", "-")),
		"[Your tech stack]":           info.TechStack,
		"${TECH_STACK}":               info.TechStack,
		"[Date]":                      now.Format("2006-01-02"),
		"${DATE}":                     now.Format("2006-01-02"),
		"${YEAR}":                     now.Format("2006"),
		"${DETECTED_FROM}":            info.DetectedFrom,
		"[Brief project description]": fmt.Sprintf("Autonomous development with %s", info.TechStack),
	}

	result := content
	for placeholder, value := range replacements {
		result = strings.ReplaceAll(result, placeholder, value)
	}

	return result
}

// createNavConfig creates the .nav-config.json file.
func (n *NavigatorInitializer) createNavConfig(agentDir string, info *ProjectInfo) error {
	config := map[string]interface{}{
		"version":             "6.1.0",
		"project_name":        info.Name,
		"tech_stack":          info.TechStack,
		"project_management":  "github", // Pilot uses GitHub by default
		"task_prefix":         "GH",
		"team_chat":           "none",
		"auto_load_navigator": true,
		"compact_strategy":    "conservative",
		"auto_update": map[string]interface{}{
			"enabled":              true,
			"check_interval_hours": 1,
		},
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(agentDir, ".nav-config.json"), data, 0644)
}

// DetectProjectInfo extracts project name and tech stack from config files.
func (n *NavigatorInitializer) DetectProjectInfo(projectPath string) (*ProjectInfo, error) {
	// Try detection methods in order
	detectors := []func(string) *ProjectInfo{
		n.detectFromGoMod,
		n.detectFromPackageJSON,
		n.detectFromPyprojectToml,
		n.detectFromCargoToml,
	}

	for _, detector := range detectors {
		if info := detector(projectPath); info != nil {
			return info, nil
		}
	}

	// Fallback: use directory name
	return &ProjectInfo{
		Name:         filepath.Base(projectPath),
		TechStack:    "Unknown",
		DetectedFrom: "directory_name",
	}, nil
}

// detectFromGoMod detects Go projects.
func (n *NavigatorInitializer) detectFromGoMod(projectPath string) *ProjectInfo {
	goModPath := filepath.Join(projectPath, "go.mod")
	content, err := os.ReadFile(goModPath)
	if err != nil {
		return nil
	}

	// Extract module name
	moduleRe := regexp.MustCompile(`module\s+([^\s]+)`)
	match := moduleRe.FindStringSubmatch(string(content))
	name := filepath.Base(projectPath)
	if len(match) > 1 {
		parts := strings.Split(match[1], "/")
		name = parts[len(parts)-1]
	}

	// Detect stack
	stackParts := []string{"Go"}
	contentStr := string(content)

	if strings.Contains(contentStr, "gin-gonic/gin") {
		stackParts = append(stackParts, "Gin")
	} else if strings.Contains(contentStr, "gorilla/mux") {
		stackParts = append(stackParts, "Gorilla Mux")
	} else if strings.Contains(contentStr, "fiber") {
		stackParts = append(stackParts, "Fiber")
	}

	if strings.Contains(contentStr, "gorm") {
		stackParts = append(stackParts, "GORM")
	}

	if strings.Contains(contentStr, "mattn/go-sqlite3") || strings.Contains(contentStr, "modernc.org/sqlite") {
		stackParts = append(stackParts, "SQLite")
	}

	return &ProjectInfo{
		Name:         name,
		TechStack:    strings.Join(stackParts, ", "),
		DetectedFrom: "go.mod",
	}
}

// detectFromPackageJSON detects Node.js projects.
func (n *NavigatorInitializer) detectFromPackageJSON(projectPath string) *ProjectInfo {
	pkgPath := filepath.Join(projectPath, "package.json")
	content, err := os.ReadFile(pkgPath)
	if err != nil {
		return nil
	}

	var pkg struct {
		Name            string            `json:"name"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}

	if err := json.Unmarshal(content, &pkg); err != nil {
		return nil
	}

	name := pkg.Name
	if name == "" {
		name = filepath.Base(projectPath)
	}

	// Merge deps
	deps := make(map[string]bool)
	for k := range pkg.Dependencies {
		deps[k] = true
	}
	for k := range pkg.DevDependencies {
		deps[k] = true
	}

	// Detect stack
	var stackParts []string

	if deps["next"] {
		stackParts = append(stackParts, "Next.js")
	} else if deps["react"] {
		stackParts = append(stackParts, "React")
	} else if deps["vue"] {
		stackParts = append(stackParts, "Vue")
	} else if deps["express"] {
		stackParts = append(stackParts, "Express")
	} else {
		stackParts = append(stackParts, "Node.js")
	}

	if deps["typescript"] {
		stackParts = append(stackParts, "TypeScript")
	}

	if deps["prisma"] {
		stackParts = append(stackParts, "Prisma")
	}

	return &ProjectInfo{
		Name:         name,
		TechStack:    strings.Join(stackParts, ", "),
		DetectedFrom: "package.json",
	}
}

// detectFromPyprojectToml detects Python projects.
func (n *NavigatorInitializer) detectFromPyprojectToml(projectPath string) *ProjectInfo {
	pyprojectPath := filepath.Join(projectPath, "pyproject.toml")
	content, err := os.ReadFile(pyprojectPath)
	if err != nil {
		return nil
	}

	contentStr := string(content)

	// Extract name
	nameRe := regexp.MustCompile(`name\s*=\s*["']([^"']+)["']`)
	match := nameRe.FindStringSubmatch(contentStr)
	name := filepath.Base(projectPath)
	if len(match) > 1 {
		name = match[1]
	}

	// Detect stack
	stackParts := []string{"Python"}
	contentLower := strings.ToLower(contentStr)

	if strings.Contains(contentLower, "fastapi") {
		stackParts = append(stackParts, "FastAPI")
	} else if strings.Contains(contentLower, "django") {
		stackParts = append(stackParts, "Django")
	} else if strings.Contains(contentLower, "flask") {
		stackParts = append(stackParts, "Flask")
	}

	return &ProjectInfo{
		Name:         name,
		TechStack:    strings.Join(stackParts, ", "),
		DetectedFrom: "pyproject.toml",
	}
}

// detectFromCargoToml detects Rust projects.
func (n *NavigatorInitializer) detectFromCargoToml(projectPath string) *ProjectInfo {
	cargoPath := filepath.Join(projectPath, "Cargo.toml")
	content, err := os.ReadFile(cargoPath)
	if err != nil {
		return nil
	}

	contentStr := string(content)

	// Extract name
	nameRe := regexp.MustCompile(`name\s*=\s*["']([^"']+)["']`)
	match := nameRe.FindStringSubmatch(contentStr)
	name := filepath.Base(projectPath)
	if len(match) > 1 {
		name = match[1]
	}

	// Detect stack
	stackParts := []string{"Rust"}

	if strings.Contains(contentStr, "actix-web") {
		stackParts = append(stackParts, "Actix Web")
	} else if strings.Contains(contentStr, "axum") {
		stackParts = append(stackParts, "Axum")
	}

	return &ProjectInfo{
		Name:         name,
		TechStack:    strings.Join(stackParts, ", "),
		DetectedFrom: "Cargo.toml",
	}
}
