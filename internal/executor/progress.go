package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Styles for progress display
var (
	phaseStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#10B981"))

	progressBarFilled = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7C3AED"))

	progressBarEmpty = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#374151"))

	logStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#EF4444"))

	// Navigator-specific styles
	navigatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F59E0B")).
			Bold(true)

	headerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#818CF8"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))

	valueStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E5E7EB"))

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#10B981")).
			Bold(true)

	costStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FBBF24"))
)

// PhaseTimestamp records when a phase started
type PhaseTimestamp struct {
	Phase     string
	StartTime time.Time
	EndTime   time.Time
}

// ProgressDisplay handles real-time progress rendering
type ProgressDisplay struct {
	taskID       string
	taskTitle    string
	phase        string
	progress     int
	logs         []string
	startTime    time.Time
	mu           sync.Mutex
	maxLogs      int
	enabled      bool
	hasNavigator bool
	navMode      string // "nav-task", "nav-loop", etc.
	// Phase tracking for end report
	phaseHistory []PhaseTimestamp
	currentPhase *PhaseTimestamp
	// Files changed tracking
	filesChanged []string
}

// NewProgressDisplay creates a new progress display
func NewProgressDisplay(taskID, taskTitle string, enabled bool) *ProgressDisplay {
	return &ProgressDisplay{
		taskID:       taskID,
		taskTitle:    taskTitle,
		phase:        "Starting",
		progress:     0,
		logs:         []string{},
		startTime:    time.Now(),
		maxLogs:      5,
		enabled:      enabled,
		phaseHistory: []PhaseTimestamp{},
		filesChanged: []string{},
	}
}

// SetNavigator marks this execution as using Navigator
func (p *ProgressDisplay) SetNavigator(detected bool, mode string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hasNavigator = detected
	p.navMode = mode
}

// AddFileChanged records a file that was modified
func (p *ProgressDisplay) AddFileChanged(filePath string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Avoid duplicates
	for _, f := range p.filesChanged {
		if f == filePath {
			return
		}
	}
	p.filesChanged = append(p.filesChanged, filePath)
}

// Update updates the progress and re-renders
func (p *ProgressDisplay) Update(phase string, progress int, message string) {
	if !p.enabled {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Track phase transitions for timing
	if phase != p.phase {
		now := time.Now()
		// End current phase
		if p.currentPhase != nil {
			p.currentPhase.EndTime = now
			p.phaseHistory = append(p.phaseHistory, *p.currentPhase)
		}
		// Start new phase
		p.currentPhase = &PhaseTimestamp{
			Phase:     phase,
			StartTime: now,
		}
	}

	p.phase = phase
	p.progress = progress

	if message != "" {
		timestamp := time.Now().Format("15:04:05")
		logEntry := fmt.Sprintf("[%s] %s", timestamp, message)
		p.logs = append(p.logs, logEntry)
		if len(p.logs) > p.maxLogs {
			p.logs = p.logs[1:]
		}
	}

	p.render()
}

// render outputs the progress display
func (p *ProgressDisplay) render() {
	// Clear previous output (move up and clear lines)
	// Navigator header (if present) + 1 task line + maxLogs log lines + 1 blank
	linesToClear := p.maxLogs + 2
	if p.hasNavigator {
		linesToClear++ // Extra line for Navigator indicator
	}
	for i := 0; i < linesToClear; i++ {
		fmt.Print("\033[A\033[K") // Move up and clear line
	}

	// Navigator indicator line (if detected)
	if p.hasNavigator {
		navIndicator := navigatorStyle.Render("🧭 Navigator")
		mode := p.navMode
		if mode == "" {
			mode = "active"
		}
		fmt.Printf("   %s: %s\n", navIndicator, dimStyle.Render(mode))
	}

	// Task line with progress bar
	duration := time.Since(p.startTime).Round(time.Second)
	progressBar := p.renderProgressBar()

	// Show Navigator phase prefix if applicable
	phaseDisplay := p.phase
	if p.hasNavigator && isNavigatorPhase(p.phase) {
		phaseDisplay = fmt.Sprintf("PHASE: %s", p.phase)
	}

	taskLine := fmt.Sprintf("   %s %s %s %3d%% %s",
		phaseStyle.Render(fmt.Sprintf("%-16s", phaseDisplay)),
		progressBar,
		p.taskID,
		p.progress,
		logStyle.Render(duration.String()),
	)
	fmt.Println(taskLine)

	// Log lines
	fmt.Println()
	for _, log := range p.logs {
		fmt.Printf("   %s\n", logStyle.Render(log))
	}
	// Pad remaining log lines
	for i := len(p.logs); i < p.maxLogs; i++ {
		fmt.Println()
	}
}

// isNavigatorPhase checks if a phase is a Navigator-specific phase
func isNavigatorPhase(phase string) bool {
	navPhases := []string{"Research", "Implement", "Verify", "Init", "Complete", "Loop Mode", "Task Mode"}
	for _, np := range navPhases {
		if strings.EqualFold(phase, np) {
			return true
		}
	}
	return false
}

// renderProgressBar creates a visual progress bar
func (p *ProgressDisplay) renderProgressBar() string {
	width := 20
	filled := p.progress * width / 100
	empty := width - filled

	bar := progressBarFilled.Render(strings.Repeat("█", filled)) +
		progressBarEmpty.Render(strings.Repeat("░", empty))

	return "[" + bar + "]"
}

// Start prints initial state and reserves space
func (p *ProgressDisplay) Start() {
	if !p.enabled {
		return
	}

	// Print initial lines to reserve space
	if p.hasNavigator {
		fmt.Println() // Navigator indicator placeholder
	}
	fmt.Println() // Task line placeholder
	fmt.Println() // Blank line
	for i := 0; i < p.maxLogs; i++ {
		fmt.Println() // Log line placeholders
	}

	// Render initial state
	p.render()
}

// StartWithNavigatorCheck prints Navigator detection status before progress
func (p *ProgressDisplay) StartWithNavigatorCheck(projectPath string) {
	if !p.enabled {
		return
	}

	// Check for .agent/ directory
	agentDir := filepath.Join(projectPath, ".agent")
	if isDir, err := osStatFunc(agentDir); err == nil && isDir {
		p.hasNavigator = true
		fmt.Printf("🧭 %s: %s (.agent/ exists)\n",
			navigatorStyle.Render("Navigator"),
			successStyle.Render("✓ detected"))
		fmt.Printf("   %s: %s\n",
			dimStyle.Render("Mode"),
			valueStyle.Render("awaiting skill activation"))
	} else {
		fmt.Printf("⚠️  %s: %s (running raw Claude Code)\n",
			navigatorStyle.Render("Navigator"),
			dimStyle.Render("not found"))
	}
	fmt.Println()

	// Continue with normal start
	p.Start()
}

// osStatFunc allows mocking os.Stat in tests
var osStatFunc = defaultOsStat

func defaultOsStat(name string) (bool, error) {
	fi, err := os.Stat(name)
	if err != nil {
		return false, err
	}
	return fi.IsDir(), nil
}

// ExecutionReport contains all data for the end-of-execution report
type ExecutionReport struct {
	TaskID           string
	TaskTitle        string
	Success          bool
	Duration         time.Duration
	Branch           string
	CommitSHA        string
	PRUrl            string
	HasNavigator     bool
	NavMode          string
	TokensInput      int64
	TokensOutput     int64
	EstimatedCostUSD float64
	FilesChanged     []string
	ModelName        string
	ErrorMessage     string
	// QualityGates contains results from quality gate checks (GH-209)
	QualityGates *QualityGatesResult
}

// Finish prints final status (simple version for backward compatibility)
func (p *ProgressDisplay) Finish(success bool, result string) {
	if !p.enabled {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Clear progress display
	linesToClear := p.maxLogs + 2
	if p.hasNavigator {
		linesToClear++
	}
	for i := 0; i < linesToClear; i++ {
		fmt.Print("\033[A\033[K")
	}

	// Print final summary
	duration := time.Since(p.startTime).Round(time.Second)
	if success {
		fmt.Printf("   %s %s in %s\n",
			phaseStyle.Render("✓ Completed"),
			p.taskID,
			duration.String(),
		)
	} else {
		fmt.Printf("   %s %s in %s\n",
			errorStyle.Render("✗ Failed"),
			p.taskID,
			duration.String(),
		)
	}

	// Print truncated result
	if result != "" {
		lines := strings.Split(result, "\n")
		maxLines := 10
		if len(lines) > maxLines {
			lines = lines[:maxLines]
			lines = append(lines, "...")
		}
		fmt.Println()
		for _, line := range lines {
			if line != "" {
				fmt.Printf("   %s\n", line)
			}
		}
	}
}

// FinishWithReport prints a comprehensive execution report
func (p *ProgressDisplay) FinishWithReport(report *ExecutionReport) {
	if !p.enabled {
		return
	}

	p.mu.Lock()

	// Close out the current phase
	if p.currentPhase != nil {
		p.currentPhase.EndTime = time.Now()
		p.phaseHistory = append(p.phaseHistory, *p.currentPhase)
	}

	// Merge tracked files with report files
	allFiles := make(map[string]bool)
	for _, f := range p.filesChanged {
		allFiles[f] = true
	}
	for _, f := range report.FilesChanged {
		allFiles[f] = true
	}

	p.mu.Unlock()

	// Clear progress display
	linesToClear := p.maxLogs + 2
	if p.hasNavigator {
		linesToClear++
	}
	for i := 0; i < linesToClear; i++ {
		fmt.Print("\033[A\033[K")
	}

	// Print structured report
	p.printExecutionReport(report, allFiles)
}

// printExecutionReport outputs the formatted execution report
func (p *ProgressDisplay) printExecutionReport(report *ExecutionReport, allFiles map[string]bool) {
	divider := "───────────────────────────────────────"

	fmt.Println(divider)
	fmt.Printf("%s\n", headerStyle.Render("📊 EXECUTION REPORT"))
	fmt.Println(divider)

	// Task info
	fmt.Printf("Task:       %s\n", valueStyle.Render(report.TaskID))
	if report.TaskTitle != "" && report.TaskTitle != report.TaskID {
		fmt.Printf("Title:      %s\n", dimStyle.Render(truncateForReport(report.TaskTitle, 50)))
	}

	// Status
	if report.Success {
		fmt.Printf("Status:     %s\n", successStyle.Render("✅ Success"))
	} else {
		fmt.Printf("Status:     %s\n", errorStyle.Render("❌ Failed"))
		if report.ErrorMessage != "" {
			fmt.Printf("Error:      %s\n", errorStyle.Render(truncateForReport(report.ErrorMessage, 60)))
		}
	}

	// Duration
	fmt.Printf("Duration:   %s\n", valueStyle.Render(report.Duration.Round(time.Second).String()))

	// Git info
	if report.Branch != "" {
		fmt.Printf("Branch:     %s\n", valueStyle.Render(report.Branch))
	}
	if report.CommitSHA != "" {
		shortSHA := report.CommitSHA
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		fmt.Printf("Commit:     %s\n", valueStyle.Render(shortSHA))
	}
	if report.PRUrl != "" {
		fmt.Printf("PR:         %s\n", valueStyle.Render(report.PRUrl))
	}

	// Navigator info
	if report.HasNavigator {
		fmt.Println()
		fmt.Printf("🧭 %s\n", navigatorStyle.Render("Navigator: Active"))
		if report.NavMode != "" {
			fmt.Printf("   Mode:    %s\n", valueStyle.Render(report.NavMode))
		}
	}

	// Quality Gates section (GH-209)
	if report.QualityGates != nil && report.QualityGates.Enabled {
		fmt.Println()
		fmt.Printf("%s\n", headerStyle.Render("🔒 Quality Gates:"))
		for _, gate := range report.QualityGates.Gates {
			var icon string
			var statusStyle lipgloss.Style
			if gate.Passed {
				icon = "✅"
				statusStyle = successStyle
			} else {
				icon = "❌"
				statusStyle = errorStyle
			}

			// Format: "  ✅ build     12s"
			durationStr := gate.Duration.Round(time.Second).String()
			fmt.Printf("  %s %-10s %s", icon, statusStyle.Render(gate.Name), dimStyle.Render(durationStr))

			// Add retry count if any
			if gate.RetryCount > 0 {
				fmt.Printf(" (%d retry)", gate.RetryCount)
			}
			fmt.Println()

			// Show error snippet for failed gates
			if !gate.Passed && gate.Error != "" {
				errSnippet := truncateForReport(gate.Error, 50)
				fmt.Printf("     %s\n", errorStyle.Render(errSnippet))
			}
		}

		// Show total retries if any
		if report.QualityGates.TotalRetries > 0 {
			fmt.Printf("  Retries:    %s\n", valueStyle.Render(fmt.Sprintf("%d", report.QualityGates.TotalRetries)))
		}
	}

	// Phase timing breakdown
	if len(p.phaseHistory) > 0 {
		fmt.Println()
		fmt.Printf("%s\n", headerStyle.Render("📈 Phases:"))
		totalDuration := report.Duration
		for _, ph := range p.phaseHistory {
			phaseDuration := ph.EndTime.Sub(ph.StartTime)
			if phaseDuration < 0 {
				continue
			}
			pct := 0
			if totalDuration > 0 {
				pct = int(float64(phaseDuration) / float64(totalDuration) * 100)
			}
			fmt.Printf("  %-12s %6s   (%2d%%)\n",
				ph.Phase,
				phaseDuration.Round(time.Second).String(),
				pct)
		}
	}

	// Files changed
	if len(allFiles) > 0 {
		fmt.Println()
		fmt.Printf("%s\n", headerStyle.Render("📁 Files Changed:"))
		count := 0
		for f := range allFiles {
			if count >= 10 {
				fmt.Printf("  ... and %d more\n", len(allFiles)-10)
				break
			}
			// Determine prefix (M for modified, A for added - simplified)
			prefix := "M"
			fmt.Printf("  %s %s\n", dimStyle.Render(prefix), filepath.Base(f))
			count++
		}
	}

	// Token usage and cost
	if report.TokensInput > 0 || report.TokensOutput > 0 {
		fmt.Println()
		fmt.Printf("%s\n", headerStyle.Render("💰 Tokens:"))
		fmt.Printf("  Input:    %s\n", valueStyle.Render(formatTokenCount(report.TokensInput)))
		fmt.Printf("  Output:   %s\n", valueStyle.Render(formatTokenCount(report.TokensOutput)))
		if report.EstimatedCostUSD > 0 {
			fmt.Printf("  Cost:     %s\n", costStyle.Render(fmt.Sprintf("~$%.2f", report.EstimatedCostUSD)))
		}
		if report.ModelName != "" {
			fmt.Printf("  Model:    %s\n", dimStyle.Render(report.ModelName))
		}
	}

	fmt.Println(divider)
}

// formatTokenCount formats a token count with thousands separators
func formatTokenCount(count int64) string {
	if count < 1000 {
		return fmt.Sprintf("%d", count)
	}
	if count < 1000000 {
		return fmt.Sprintf("%dk", count/1000)
	}
	return fmt.Sprintf("%.1fM", float64(count)/1000000)
}

// truncateForReport truncates text for report display
func truncateForReport(text string, maxLen int) string {
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.TrimSpace(text)
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen-3] + "..."
}
