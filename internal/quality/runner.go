package quality

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// ProgressCallback reports gate execution progress
type ProgressCallback func(gateName string, status GateStatus, message string)

// Runner executes quality gates
type Runner struct {
	config     *Config
	projectDir string
	log        *slog.Logger
	onProgress ProgressCallback
}

// NewRunner creates a new quality gate runner
func NewRunner(config *Config, projectDir string) *Runner {
	return &Runner{
		config:     config,
		projectDir: projectDir,
		log:        logging.WithComponent("quality"),
	}
}

// OnProgress sets the progress callback
func (r *Runner) OnProgress(callback ProgressCallback) {
	r.onProgress = callback
}

// RunAll executes all configured quality gates.
// By default gates run in parallel. Set config.Parallel=false for sequential execution.
func (r *Runner) RunAll(ctx context.Context, taskID string) (*CheckResults, error) {
	if !r.config.Enabled {
		r.log.Debug("Quality gates disabled, skipping")
		return &CheckResults{
			TaskID:    taskID,
			AllPassed: true,
			Results:   []*Result{},
		}, nil
	}

	results := &CheckResults{
		TaskID:    taskID,
		StartedAt: time.Now(),
		Results:   make([]*Result, len(r.config.Gates)),
	}

	parallel := r.config.IsParallel()
	mode := "parallel"
	if !parallel {
		mode = "sequential"
	}

	r.log.Info("Starting quality gate checks",
		slog.String("task_id", taskID),
		slog.Int("gate_count", len(r.config.Gates)),
		slog.String("mode", mode),
	)

	if parallel {
		// Execute all gates in parallel
		var wg sync.WaitGroup
		for i, gate := range r.config.Gates {
			wg.Add(1)
			go func(idx int, g *Gate) {
				defer wg.Done()
				results.Results[idx] = r.runGate(ctx, g)
			}(i, gate)
		}
		wg.Wait()
	} else {
		// Execute gates sequentially
		for i, gate := range r.config.Gates {
			results.Results[i] = r.runGate(ctx, gate)
		}
	}

	// Evaluate results
	allPassed := true
	for i, result := range results.Results {
		gate := r.config.Gates[i]
		if result.Status == StatusFailed && gate.Required {
			allPassed = false
			r.log.Warn("Required quality gate failed",
				slog.String("gate", gate.Name),
				slog.String("error", result.Error),
			)
		}
	}

	results.AllPassed = allPassed
	results.CompletedAt = time.Now()
	results.TotalTime = results.CompletedAt.Sub(results.StartedAt)

	r.log.Info("Quality gate checks completed",
		slog.String("task_id", taskID),
		slog.Bool("all_passed", allPassed),
		slog.Duration("total_time", results.TotalTime),
	)

	return results, nil
}

// RunGate executes a single quality gate
func (r *Runner) RunGate(ctx context.Context, gateName string) (*Result, error) {
	gate := r.config.GetGate(gateName)
	if gate == nil {
		return nil, ErrGateNotFound
	}
	return r.runGate(ctx, gate), nil
}

// runGate executes a gate with retry logic
func (r *Runner) runGate(ctx context.Context, gate *Gate) *Result {
	result := &Result{
		GateName:  gate.Name,
		Status:    StatusRunning,
		StartedAt: time.Now(),
	}

	r.reportProgress(gate.Name, StatusRunning, fmt.Sprintf("Running %s gate...", gate.Name))

	maxAttempts := gate.MaxRetries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			result.RetryCount = attempt
			result.Status = StatusRetrying
			r.reportProgress(gate.Name, StatusRetrying, fmt.Sprintf("Retrying %s (attempt %d/%d)...", gate.Name, attempt+1, maxAttempts))

			// Wait before retry
			if gate.RetryDelay > 0 {
				select {
				case <-ctx.Done():
					result.Status = StatusFailed
					result.Error = "context cancelled during retry delay"
					result.CompletedAt = time.Now()
					result.Duration = result.CompletedAt.Sub(result.StartedAt)
					return result
				case <-time.After(gate.RetryDelay):
				}
			}
		}

		r.log.Debug("Executing gate command",
			slog.String("gate", gate.Name),
			slog.String("command", gate.Command),
			slog.Int("attempt", attempt+1),
		)

		exitCode, output, err := r.executeCommand(ctx, gate)

		result.ExitCode = exitCode
		result.Output = output

		if err != nil {
			if ctx.Err() != nil {
				result.Status = StatusFailed
				result.Error = "context cancelled"
				break
			}
			// Command execution error (not exit code error)
			result.Error = err.Error()
			continue
		}

		if exitCode == 0 {
			result.Status = StatusPassed
			r.reportProgress(gate.Name, StatusPassed, fmt.Sprintf("%s gate passed", gate.Name))

			// Parse coverage if this is a coverage gate
			if gate.Type == GateCoverage {
				result.Coverage = parseCoverageOutput(output)
				if gate.Threshold > 0 && result.Coverage < gate.Threshold {
					result.Status = StatusFailed
					result.Error = fmt.Sprintf("coverage %.1f%% below threshold %.1f%%", result.Coverage, gate.Threshold)
					r.reportProgress(gate.Name, StatusFailed, result.Error)
					continue
				}
			}

			break
		}

		// Exit code != 0
		result.Error = fmt.Sprintf("command exited with code %d", exitCode)

		// Don't retry on last attempt
		if attempt == maxAttempts-1 {
			result.Status = StatusFailed
			r.reportProgress(gate.Name, StatusFailed, fmt.Sprintf("%s gate failed: %s", gate.Name, result.Error))
		}
	}

	result.CompletedAt = time.Now()
	result.Duration = result.CompletedAt.Sub(result.StartedAt)

	return result
}

// executeCommand runs the gate command
func (r *Runner) executeCommand(ctx context.Context, gate *Gate) (int, string, error) {
	timeout := gate.DefaultTimeout()
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Use shell to execute command (supports pipes, redirects, etc.)
	cmd := exec.CommandContext(cmdCtx, "sh", "-c", gate.Command)
	cmd.Dir = r.projectDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Combine stdout and stderr
	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\n" + stderr.String()
	}

	// Check for timeout
	if cmdCtx.Err() == context.DeadlineExceeded {
		return -1, output, ErrGateTimeout
	}

	// Get exit code
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return -1, output, err
		}
	}

	return exitCode, output, nil
}

// parseCoverageOutput extracts coverage percentage from command output
func parseCoverageOutput(output string) float64 {
	// Go coverage patterns
	// "coverage: 85.3% of statements"
	// "ok  	pkg	0.123s	coverage: 85.3% of statements"
	goPattern := regexp.MustCompile(`coverage:\s*([\d.]+)%`)

	// Jest/NYC patterns
	// "All files |   85.3 |   80.0 |   90.0 |   85.3 |"
	// "Statements   : 85.3% ( 100/117 )"
	jestPattern := regexp.MustCompile(`(?:Statements|Lines)\s*:\s*([\d.]+)%`)

	// Python coverage patterns
	// "TOTAL                                              85%"
	// "TOTAL         100      15      85%"
	pyPattern := regexp.MustCompile(`TOTAL\s+.*?(\d+)%`)

	scanner := bufio.NewScanner(strings.NewReader(output))
	var coverage float64

	for scanner.Scan() {
		line := scanner.Text()

		if matches := goPattern.FindStringSubmatch(line); len(matches) > 1 {
			if val, err := strconv.ParseFloat(matches[1], 64); err == nil {
				coverage = val
			}
		}

		if matches := jestPattern.FindStringSubmatch(line); len(matches) > 1 {
			if val, err := strconv.ParseFloat(matches[1], 64); err == nil {
				coverage = val
			}
		}

		if matches := pyPattern.FindStringSubmatch(line); len(matches) > 1 {
			if val, err := strconv.ParseFloat(matches[1], 64); err == nil {
				coverage = val
			}
		}
	}

	return coverage
}

// reportProgress sends progress update via callback
func (r *Runner) reportProgress(gateName string, status GateStatus, message string) {
	r.log.Debug("Gate progress",
		slog.String("gate", gateName),
		slog.String("status", string(status)),
		slog.String("message", message),
	)

	if r.onProgress != nil {
		r.onProgress(gateName, status, message)
	}
}

// FormatErrorFeedback formats gate failure output for Claude retry
func FormatErrorFeedback(results *CheckResults) string {
	var sb strings.Builder

	sb.WriteString("## Quality Gate Failures\n\n")
	sb.WriteString("The following quality gates failed. Please fix the issues and try again.\n\n")

	for _, result := range results.Results {
		if result.Status != StatusFailed {
			continue
		}

		sb.WriteString(fmt.Sprintf("### %s Gate (FAILED)\n\n", result.GateName))
		sb.WriteString("**Error Output:**\n```\n")

		// Truncate output if too long
		output := result.Output
		maxLen := 2000
		if len(output) > maxLen {
			output = output[:maxLen] + "\n... (truncated)"
		}
		sb.WriteString(output)
		sb.WriteString("\n```\n\n")
	}

	sb.WriteString("Please fix these issues and ensure all quality gates pass.\n")

	return sb.String()
}

// ShouldRetry determines if gates should be retried with Claude
func ShouldRetry(config *Config, results *CheckResults, attempt int) bool {
	if results.AllPassed {
		return false
	}

	if config.OnFailure.Action != ActionRetry {
		return false
	}

	return attempt < config.OnFailure.MaxRetries
}
