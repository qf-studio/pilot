package executor

import (
	"fmt"
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
)

// ProgressDisplay handles real-time progress rendering
type ProgressDisplay struct {
	taskID    string
	taskTitle string
	phase     string
	progress  int
	logs      []string
	startTime time.Time
	mu        sync.Mutex
	maxLogs   int
	enabled   bool
}

// NewProgressDisplay creates a new progress display
func NewProgressDisplay(taskID, taskTitle string, enabled bool) *ProgressDisplay {
	return &ProgressDisplay{
		taskID:    taskID,
		taskTitle: taskTitle,
		phase:     "Starting",
		progress:  0,
		logs:      []string{},
		startTime: time.Now(),
		maxLogs:   5,
		enabled:   enabled,
	}
}

// Update updates the progress and re-renders
func (p *ProgressDisplay) Update(phase string, progress int, message string) {
	if !p.enabled {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

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
	// 1 task line + maxLogs log lines + 1 blank = maxLogs + 2 lines
	linesToClear := p.maxLogs + 2
	for i := 0; i < linesToClear; i++ {
		fmt.Print("\033[A\033[K") // Move up and clear line
	}

	// Task line with progress bar
	duration := time.Since(p.startTime).Round(time.Second)
	progressBar := p.renderProgressBar()

	taskLine := fmt.Sprintf("   %s %s %s %3d%% %s",
		phaseStyle.Render(fmt.Sprintf("%-12s", p.phase)),
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
	fmt.Println() // Task line placeholder
	fmt.Println() // Blank line
	for i := 0; i < p.maxLogs; i++ {
		fmt.Println() // Log line placeholders
	}

	// Render initial state
	p.render()
}

// Finish prints final status
func (p *ProgressDisplay) Finish(success bool, result string) {
	if !p.enabled {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Clear progress display
	linesToClear := p.maxLogs + 2
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
