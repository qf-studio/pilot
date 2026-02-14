// Package quality provides quality gate enforcement for task execution.
// Gates run after implementation to ensure code quality before PR creation.
package quality

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Errors for quality gate enforcement
var (
	ErrGateFailed     = errors.New("quality gate failed")
	ErrGateTimeout    = errors.New("quality gate timed out")
	ErrRetryExhausted = errors.New("quality gate retries exhausted")
	ErrGateNotFound   = errors.New("quality gate not found")
)

// GateType identifies built-in gate types
type GateType string

const (
	GateBuild     GateType = "build"
	GateTest      GateType = "test"
	GateLint      GateType = "lint"
	GateCoverage  GateType = "coverage"
	GateSecurity  GateType = "security"
	GateTypeCheck GateType = "typecheck"
	GateCustom    GateType = "custom"
)

// GateStatus represents the current state of a gate check
type GateStatus string

const (
	StatusPending  GateStatus = "pending"
	StatusRunning  GateStatus = "running"
	StatusPassed   GateStatus = "passed"
	StatusFailed   GateStatus = "failed"
	StatusSkipped  GateStatus = "skipped"
	StatusRetrying GateStatus = "retrying"
)

// Gate defines a single quality gate check
type Gate struct {
	Name        string        `yaml:"name" json:"name"`
	Type        GateType      `yaml:"type" json:"type"`
	Command     string        `yaml:"command" json:"command"`
	Required    bool          `yaml:"required" json:"required"`         // Fail pipeline if gate fails
	Timeout     time.Duration `yaml:"timeout" json:"timeout"`           // Max execution time
	Threshold   float64       `yaml:"threshold" json:"threshold"`       // For coverage gates (percentage)
	MaxRetries  int           `yaml:"max_retries" json:"max_retries"`   // Retry count on failure
	RetryDelay  time.Duration `yaml:"retry_delay" json:"retry_delay"`   // Delay between retries
	FailureHint string        `yaml:"failure_hint" json:"failure_hint"` // Hint for Claude on failure
}

// DefaultTimeout returns default timeout for a gate type
func (g *Gate) DefaultTimeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	switch g.Type {
	case GateBuild:
		return 5 * time.Minute
	case GateTest:
		return 10 * time.Minute
	case GateLint:
		return 2 * time.Minute
	case GateCoverage:
		return 10 * time.Minute
	case GateSecurity:
		return 5 * time.Minute
	case GateTypeCheck:
		return 3 * time.Minute
	default:
		return 5 * time.Minute
	}
}

// Result represents the outcome of running a gate
type Result struct {
	GateName    string        `json:"gate_name"`
	Status      GateStatus    `json:"status"`
	ExitCode    int           `json:"exit_code"`
	Output      string        `json:"output"` // stdout + stderr
	Error       string        `json:"error"`  // Error message if failed
	Duration    time.Duration `json:"duration"`
	RetryCount  int           `json:"retry_count"` // How many retries were attempted
	Coverage    float64       `json:"coverage"`    // Parsed coverage percentage (for coverage gates)
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at"`
}

// Passed returns true if the gate passed
func (r *Result) Passed() bool {
	return r.Status == StatusPassed
}

// CheckResults holds all gate check results for a task
type CheckResults struct {
	TaskID      string        `json:"task_id"`
	AllPassed   bool          `json:"all_passed"`
	Results     []*Result     `json:"results"`
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at"`
	TotalTime   time.Duration `json:"total_time"`
}

// GetFailedGates returns all failed required gates
func (cr *CheckResults) GetFailedGates() []*Result {
	var failed []*Result
	for _, r := range cr.Results {
		if r.Status == StatusFailed {
			failed = append(failed, r)
		}
	}
	return failed
}

// Config holds quality gates configuration
type Config struct {
	Enabled   bool          `yaml:"enabled" json:"enabled"`
	Parallel  *bool         `yaml:"parallel" json:"parallel"` // Run gates in parallel (default: true)
	Gates     []*Gate       `yaml:"gates" json:"gates"`
	OnFailure FailureConfig `yaml:"on_failure" json:"on_failure"`
}

// IsParallel returns whether gates should run in parallel.
// Defaults to true if not explicitly set.
func (c *Config) IsParallel() bool {
	if c.Parallel == nil {
		return true // Default to parallel execution
	}
	return *c.Parallel
}

// FailureConfig defines behavior when gates fail
type FailureConfig struct {
	Action     FailureAction `yaml:"action" json:"action"`
	MaxRetries int           `yaml:"max_retries" json:"max_retries"`
	NotifyOn   []GateStatus  `yaml:"notify_on" json:"notify_on"` // Statuses to notify on
}

// FailureAction defines what to do when a required gate fails
type FailureAction string

const (
	ActionRetry FailureAction = "retry" // Retry with error feedback
	ActionFail  FailureAction = "fail"  // Stop immediately
	ActionWarn  FailureAction = "warn"  // Warn but continue
)

// DefaultConfig returns sensible default quality gates configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled: false, // Disabled by default
		Gates: []*Gate{
			{
				Name:        "build",
				Type:        GateBuild,
				Command:     "make build",
				Required:    true,
				Timeout:     5 * time.Minute,
				MaxRetries:  2,
				RetryDelay:  5 * time.Second,
				FailureHint: "Fix compilation errors in the changed files",
			},
			{
				Name:        "test",
				Type:        GateTest,
				Command:     "make test",
				Required:    true,
				Timeout:     10 * time.Minute,
				MaxRetries:  2,
				RetryDelay:  5 * time.Second,
				FailureHint: "Fix failing tests or update test expectations",
			},
			{
				Name:        "lint",
				Type:        GateLint,
				Command:     "make lint",
				Required:    false, // Warn only by default
				Timeout:     2 * time.Minute,
				MaxRetries:  1,
				RetryDelay:  2 * time.Second,
				FailureHint: "Fix linting errors: formatting, unused imports, etc.",
			},
		},
		OnFailure: FailureConfig{
			Action:     ActionRetry,
			MaxRetries: 2,
			NotifyOn:   []GateStatus{StatusFailed},
		},
	}
}

// GetGate returns a gate by name
func (c *Config) GetGate(name string) *Gate {
	for _, g := range c.Gates {
		if g.Name == name {
			return g
		}
	}
	return nil
}

// GetRequiredGates returns all required gates
func (c *Config) GetRequiredGates() []*Gate {
	var required []*Gate
	for _, g := range c.Gates {
		if g.Required {
			required = append(required, g)
		}
	}
	return required
}

// Validate validates the configuration
func (c *Config) Validate() error {
	for _, g := range c.Gates {
		if g.Name == "" {
			return errors.New("quality gate name is required")
		}
		if g.Command == "" {
			return errors.New("quality gate command is required for gate: " + g.Name)
		}
	}
	return nil
}

// MinimalBuildGate returns a minimal quality gate config with just build verification.
// Used when quality gates are not explicitly configured but we still want basic safety.
// The build command should be set via DetectBuildCommand() based on project type.
func MinimalBuildGate() *Config {
	return &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:        "build",
				Type:        GateBuild,
				Command:     "go build ./...", // Default for Go projects, override via DetectBuildCommand
				Required:    true,
				Timeout:     3 * time.Minute,
				MaxRetries:  1, // Single retry for build fixes
				RetryDelay:  3 * time.Second,
				FailureHint: "Fix compilation errors in the changed files",
			},
		},
		OnFailure: FailureConfig{
			Action:     ActionRetry,
			MaxRetries: 1, // Single retry for build fixes
		},
	}
}

// DetectBuildCommand returns appropriate build command for the project.
// Checks for common project indicators and returns the build command.
// Returns empty string if project type cannot be detected.
func DetectBuildCommand(projectPath string) string {
	// Check for Go project
	if fileExists(filepath.Join(projectPath, "go.mod")) {
		return "go build ./..."
	}
	// Check for Node.js project with TypeScript
	if fileExists(filepath.Join(projectPath, "package.json")) {
		if fileExists(filepath.Join(projectPath, "tsconfig.json")) {
			return "npm run build || npx tsc --noEmit"
		}
		return "npm run build --if-present"
	}
	// Check for Rust project
	if fileExists(filepath.Join(projectPath, "Cargo.toml")) {
		return "cargo check"
	}
	// Check for Python project
	if fileExists(filepath.Join(projectPath, "pyproject.toml")) || fileExists(filepath.Join(projectPath, "setup.py")) {
		// Basic syntax check for Python files
		return "python -m py_compile $(find . -name '*.py' -not -path './venv/*' -not -path './.venv/*' 2>/dev/null | head -100)"
	}
	// No build command detected
	return ""
}

// fileExists checks if a file or directory exists at the given path.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
