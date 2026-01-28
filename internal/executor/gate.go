package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GateResult represents the result of running the pre-push gate
type GateResult struct {
	Passed   bool
	Duration time.Duration
	Checks   []GateCheck
	Output   string
}

// GateCheck represents a single check in the gate
type GateCheck struct {
	Name     string
	Passed   bool
	Duration time.Duration
	Output   string
}

// Gate runs pre-push validation before pushing code
type Gate struct {
	projectPath string
	maxRetries  int
	autoFix     bool
}

// NewGate creates a new Gate instance
func NewGate(projectPath string) *Gate {
	return &Gate{
		projectPath: projectPath,
		maxRetries:  3,
		autoFix:     true,
	}
}

// SetMaxRetries sets the maximum number of fix attempts
func (g *Gate) SetMaxRetries(n int) {
	g.maxRetries = n
}

// SetAutoFix enables/disables auto-fix attempts
func (g *Gate) SetAutoFix(enabled bool) {
	g.autoFix = enabled
}

// Run executes the pre-push gate validation
// Returns GateResult with detailed information about each check
func (g *Gate) Run(ctx context.Context) (*GateResult, error) {
	start := time.Now()

	result := &GateResult{
		Passed: true,
		Checks: make([]GateCheck, 0),
	}

	// Check if gate script exists
	gateScript := filepath.Join(g.projectPath, "scripts", "pre-push-gate.sh")

	// Run the gate script
	cmd := exec.CommandContext(ctx, "bash", gateScript)
	cmd.Dir = g.projectPath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result.Duration = time.Since(start)
	result.Output = stdout.String() + stderr.String()

	if err != nil {
		result.Passed = false

		// Parse output to identify which checks failed
		result.Checks = parseGateOutput(result.Output)

		// If auto-fix is enabled, try to fix and re-run
		if g.autoFix {
			for attempt := 1; attempt <= g.maxRetries; attempt++ {
				// Run auto-fix
				if fixErr := g.runAutoFix(ctx); fixErr != nil {
					// Auto-fix failed, continue to next attempt
					continue
				}

				// Re-run gate
				cmd = exec.CommandContext(ctx, "bash", gateScript)
				cmd.Dir = g.projectPath
				stdout.Reset()
				stderr.Reset()
				cmd.Stdout = &stdout
				cmd.Stderr = &stderr

				if cmd.Run() == nil {
					result.Passed = true
					result.Output = stdout.String() + stderr.String()
					result.Checks = parseGateOutput(result.Output)
					break
				}

				result.Output = stdout.String() + stderr.String()
				result.Checks = parseGateOutput(result.Output)
			}
		}
	} else {
		result.Checks = parseGateOutput(result.Output)
	}

	result.Duration = time.Since(start)
	return result, nil
}

// RunQuick runs a quick validation (build only)
func (g *Gate) RunQuick(ctx context.Context) (*GateResult, error) {
	start := time.Now()

	result := &GateResult{
		Passed: true,
		Checks: make([]GateCheck, 0),
	}

	// Just run build
	cmd := exec.CommandContext(ctx, "go", "build", "-o", "/dev/null", "./...")
	cmd.Dir = g.projectPath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	duration := time.Since(start)

	check := GateCheck{
		Name:     "build",
		Passed:   err == nil,
		Duration: duration,
		Output:   stdout.String() + stderr.String(),
	}
	result.Checks = append(result.Checks, check)
	result.Passed = err == nil
	result.Duration = duration
	result.Output = check.Output

	return result, nil
}

// runAutoFix runs the auto-fix script
func (g *Gate) runAutoFix(ctx context.Context) error {
	autoFixScript := filepath.Join(g.projectPath, "scripts", "auto-fix.sh")

	cmd := exec.CommandContext(ctx, "bash", autoFixScript)
	cmd.Dir = g.projectPath

	return cmd.Run()
}

// parseGateOutput parses gate output to extract individual check results
func parseGateOutput(output string) []GateCheck {
	checks := make([]GateCheck, 0)

	lines := strings.Split(output, "\n")
	var currentCheck *GateCheck

	for _, line := range lines {
		// Detect check headers like "[1/5] Build"
		if strings.Contains(line, "/5]") && strings.Contains(line, "[") {
			// Extract check name
			parts := strings.SplitN(line, "]", 2)
			if len(parts) == 2 {
				name := strings.TrimSpace(parts[1])
				if currentCheck != nil {
					checks = append(checks, *currentCheck)
				}
				currentCheck = &GateCheck{
					Name: name,
				}
			}
		}

		// Detect pass/fail
		if currentCheck != nil {
			if strings.Contains(line, "✓") {
				currentCheck.Passed = true
			} else if strings.Contains(line, "✗") {
				currentCheck.Passed = false
			}
		}

		// Capture output
		if currentCheck != nil && !strings.HasPrefix(strings.TrimSpace(line), "[") {
			currentCheck.Output += line + "\n"
		}
	}

	// Add last check
	if currentCheck != nil {
		checks = append(checks, *currentCheck)
	}

	return checks
}

// FormatGateResult formats the gate result for display
func FormatGateResult(result *GateResult) string {
	var sb strings.Builder

	if result.Passed {
		sb.WriteString("✅ Gate PASSED")
	} else {
		sb.WriteString("❌ Gate FAILED")
	}
	sb.WriteString(fmt.Sprintf(" (%s)\n", result.Duration.Round(time.Millisecond)))

	for _, check := range result.Checks {
		icon := "✓"
		if !check.Passed {
			icon = "✗"
		}
		sb.WriteString(fmt.Sprintf("  %s %s\n", icon, check.Name))
	}

	return sb.String()
}
