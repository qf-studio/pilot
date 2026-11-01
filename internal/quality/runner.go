package quality

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

// ProgressCallback reports gate execution progress
type ProgressCallback func(gateName string, status GateStatus, message string)

// Runner executes quality gates
type Runner struct {
	config     *Config
	projectDir string
	// baseBranch is the branch the task diverged from; the changed-package
	// scope (GH-5926) is computed against it. Empty forces the full suite.
	baseBranch string
	log        *slog.Logger
	onProgress ProgressCallback

	// scopeFn computes the changed-package scope; ChangedGoPackages unless a test overrides it.
	scopeFn func(ctx context.Context, dir, baseBranch string) ([]string, bool, string)
	// runScope is the scope RunAll computed for the current run, reused by every
	// gate and retry in it; nil outside RunAll.
	scopeMu  sync.Mutex
	runScope *changeScope

	// lookPath resolves a binary on PATH; exec.LookPath unless a test overrides it.
	lookPath func(string) (string, error)
	// fallbackWarned dedups the toolchain-fallback WARN to once per gate per Runner.
	fallbackMu     sync.Mutex
	fallbackWarned map[string]bool
}

// NewRunner creates a new quality gate runner
func NewRunner(config *Config, projectDir string) *Runner {
	return &Runner{
		config:     config,
		projectDir: projectDir,
		log:        logging.WithComponent("quality"),
		lookPath:   exec.LookPath,
		scopeFn:    ChangedGoPackages,
	}
}

// changeScope is the changed-package scope exported to gate commands.
type changeScope struct {
	packages []string
	full     bool
	reason   string
}

// env returns the PILOT_CHANGED_* assignments for a gate command.
func (s *changeScope) env() []string {
	full := "0"
	pkgs := strings.Join(s.packages, " ")
	if s.full {
		full = "1"
		pkgs = ""
	}
	return []string{
		EnvChangedPackages + "=" + pkgs,
		EnvChangedFull + "=" + full,
		EnvChangedReason + "=" + s.reason,
	}
}

func (r *Runner) computeScope(ctx context.Context) *changeScope {
	pkgs, full, reason := r.scopeFn(ctx, r.projectDir, r.baseBranch)
	return &changeScope{packages: pkgs, full: full, reason: reason}
}

// gateScope returns the scope RunAll computed for this run, or computes one
// for a gate run on its own (RunGate).
func (r *Runner) gateScope(ctx context.Context) *changeScope {
	r.scopeMu.Lock()
	s := r.runScope
	r.scopeMu.Unlock()
	if s != nil {
		return s
	}
	return r.computeScope(ctx)
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

	// Compute the changed-package scope once: every gate and every retry of it
	// sees the same diff, and go list over the module is not free.
	scope := r.computeScope(ctx)
	r.scopeMu.Lock()
	r.runScope = scope
	r.scopeMu.Unlock()
	defer func() {
		r.scopeMu.Lock()
		r.runScope = nil
		r.scopeMu.Unlock()
	}()

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
			logging.SafeGo("quality-runner", func() {
				defer wg.Done()
				results.Results[i] = r.runGate(ctx, gate)
			})
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

	command := r.resolveCommand(gate)
	result.Command = command

	r.reportProgress(gate.Name, StatusRunning, fmt.Sprintf("Running %s gate...", gate.Name))

	scope := r.gateScope(ctx)
	r.log.Info("Quality gate changed-package scope",
		slog.String("gate", gate.Name),
		slog.Int("package_count", len(scope.packages)),
		slog.Bool("full", scope.full),
		slog.String("reason", scope.reason),
	)

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

		exitCode, output, err := r.executeCommand(ctx, gate, command, scope)

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

		// Exit 127 is "command not found": the gate's runner binary is missing, which
		// no amount of retrying (here or via Claude) can fix. Fail fast, distinctly.
		if exitCode == exitCodeCommandNotFound {
			result.Err = fmt.Errorf("%w: %q exited with code %d (command not found)", ErrGateRunnerMissing, command, exitCode)
			result.Error = result.Err.Error()
			result.Status = StatusFailed
			r.reportProgress(gate.Name, StatusFailed, fmt.Sprintf("%s gate failed: %s", gate.Name, result.Error))
			break
		}

		// Exit 2 with "No rule to make target" is a gate naming a target the Makefile
		// lacks: a config problem Claude cannot fix without rewriting the Makefile.
		// A line carrying "needed by" is a missing prerequisite file, a real code problem.
		if exitCode == exitCodeMakeError {
			if target, ok := missingMakeTarget(output); ok {
				result.Err = fmt.Errorf("%w: %q exited with code %d (no rule to make target %q)", ErrGateTargetMissing, command, exitCode, target)
				result.Error = result.Err.Error()
				result.Status = StatusFailed
				r.reportProgress(gate.Name, StatusFailed, fmt.Sprintf("%s gate failed: %s", gate.Name, result.Error))
				break
			}
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

// resolveCommand returns the command to execute for gate. For a gate that runs
// through `make` it substitutes a working command in two cases:
//   - make is not installed (e.g. the hosted-tenant AMI ships go but not make):
//     the language-native build/test command (GH-5622);
//   - make is installed but the Makefile lacks the configured target (GH-6002):
//     the gate type's default target (`test`/`build`) when present, else the
//     native command.
//
// Substitutions are logged once per gate. With no usable substitute the original
// command is returned and fails (exit 127 / ErrGateTargetMissing).
func (r *Runner) resolveCommand(gate *Gate) string {
	fields := strings.Fields(gate.Command)
	if len(fields) == 0 || fields[0] != "make" {
		return gate.Command
	}
	if _, err := r.lookPath("make"); err == nil {
		return r.resolveMissingMakeTarget(gate, fields)
	}
	native := nativeFallbackCommand(r.projectDir, gate)
	if native == "" {
		return gate.Command
	}
	r.warnFallbackOnce(gate, native, "make not found on PATH; running native toolchain command for quality gate")
	return native
}

// resolveMissingMakeTarget handles a `make` gate when make is on PATH. It reads
// the Makefile rather than probing with `make -n`/`-q`, which would expand
// $(shell ...) and recursive $(MAKE) lines.
func (r *Runner) resolveMissingMakeTarget(gate *Gate, fields []string) string {
	idx := -1
	for i := 1; i < len(fields); i++ {
		w := fields[i]
		if strings.HasPrefix(w, "-") || strings.Contains(w, "=") {
			continue
		}
		idx = i
		break
	}
	if idx < 0 {
		return gate.Command // bare `make`: the default goal, nothing to check
	}
	target := fields[idx]

	makefile := filepath.Join(r.projectDir, "Makefile")
	if !fileExists(makefile) || hasMakefileTarget(makefile, target) || makefileHasInclude(makefile) {
		return gate.Command
	}

	var fallback string
	if def := defaultMakeTarget(gate.Type); def != "" && def != target && hasMakefileTarget(makefile, def) {
		repl := append([]string(nil), fields...)
		repl[idx] = def
		fallback = strings.Join(repl, " ")
	} else {
		fallback = nativeFallbackCommand(r.projectDir, gate)
	}
	if fallback == "" {
		return gate.Command
	}
	r.warnFallbackOnce(gate, fallback, "configured make target absent; running fallback for quality gate", slog.String("target", target))
	return fallback
}

// defaultMakeTarget is the conventional make target for a gate type, "" if none.
func defaultMakeTarget(t GateType) string {
	switch t {
	case GateTest:
		return "test"
	case GateBuild:
		return "build"
	default:
		return ""
	}
}

// makefileHasInclude reports whether the Makefile has an include directive, in
// which case a target hasMakefileTarget cannot see may still be defined.
func makefileHasInclude(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "include ") || strings.HasPrefix(t, "-include ") || strings.HasPrefix(t, "sinclude ") ||
			strings.HasPrefix(t, "include\t") || strings.HasPrefix(t, "-include\t") || strings.HasPrefix(t, "sinclude\t") {
			return true
		}
	}
	return false
}

// warnFallbackOnce logs the fallback substitution at most once per gate per Runner.
func (r *Runner) warnFallbackOnce(gate *Gate, fallback, msg string, extra ...any) {
	r.fallbackMu.Lock()
	first := !r.fallbackWarned[gate.Name]
	if r.fallbackWarned == nil {
		r.fallbackWarned = make(map[string]bool)
	}
	r.fallbackWarned[gate.Name] = true
	r.fallbackMu.Unlock()
	if !first {
		return
	}
	attrs := append([]any{
		slog.String("gate", gate.Name),
		slog.String("configured_command", gate.Command),
		slog.String("fallback_command", fallback),
	}, extra...)
	r.log.Warn(msg, attrs...)
}

var (
	noRuleToMakeTargetRe = regexp.MustCompile(`No rule to make target ['"\x60‘]([^'"\x60’]+)['"\x60’]`)
	dontKnowHowToMakeRe  = regexp.MustCompile(`don't know how to make ([^\s:.]+(?:\.[^\s:.]+)*)`)
)

// missingMakeTarget reports the target named by a GNU "No rule to make target" or
// BSD "don't know how to make" line in output. Lines with "needed by" (a missing
// prerequisite file) do not count.
func missingMakeTarget(output string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "needed by") {
			continue
		}
		if m := noRuleToMakeTargetRe.FindStringSubmatch(line); m != nil {
			return m[1], true
		}
		if m := dontKnowHowToMakeRe.FindStringSubmatch(line); m != nil {
			return m[1], true
		}
	}
	return "", false
}

// nativeFallbackCommand returns the language-native command equivalent to a
// `make`-based gate of the given type, or "" when none can be determined.
func nativeFallbackCommand(projectDir string, gate *Gate) string {
	switch gate.Type {
	case GateBuild:
		return DetectBuildCommand(projectDir)
	case GateTest:
		return detectNativeTestCommand(projectDir)
	default:
		return ""
	}
}

// executeCommand runs the gate command with the daemon environment plus the
// PILOT_CHANGED_* scope variables.
func (r *Runner) executeCommand(ctx context.Context, gate *Gate, command string, scope *changeScope) (int, string, error) {
	timeout := gate.DefaultTimeout()
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Use shell to execute command (supports pipes, redirects, etc.)
	cmd := exec.CommandContext(cmdCtx, "sh", "-c", command)
	cmd.Dir = r.projectDir
	// Later entries win over inherited duplicates (os/exec dedups keeping the last).
	cmd.Env = append(os.Environ(), scope.env()...)

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

		fmt.Fprintf(&sb, "### %s Gate (FAILED)\n\n", result.GateName)
		sb.WriteString("**Error Output:**\n```\n")
		sb.WriteString(extractFailureOutput(result.Output, 2000))
		sb.WriteString("\n```\n\n")
	}

	sb.WriteString("Please fix these issues and ensure all quality gates pass.\n")

	return sb.String()
}

// failureMarkerRE matches lines that carry the actual failure in gate output:
// go test verdicts, race reports, panics, build/lint diagnostics.
var failureMarkerRE = regexp.MustCompile(`^\s*(--- FAIL|FAIL\b|panic:|WARNING: DATA RACE|Error:|# )|\.go:\d+(:\d+)?: |race detected`)

// extractFailureOutput reduces gate output to its failure-relevant portion.
// go test prints failures and summaries AFTER potentially thousands of PASS
// lines, so a head truncation hands the retry worker nothing but noise
// (GH-3578). Lines matching failure markers are kept with trailing context;
// when nothing matches, the tail is kept — that's where tools print summaries.
func extractFailureOutput(output string, maxLen int) string {
	if len(output) <= maxLen {
		return output
	}

	lines := strings.Split(output, "\n")
	const contextAfter = 6
	keep := make([]bool, len(lines))
	matched := false
	for i, line := range lines {
		if failureMarkerRE.MatchString(line) {
			matched = true
			for j := i; j < len(lines) && j <= i+contextAfter; j++ {
				keep[j] = true
			}
		}
	}

	if !matched {
		return "... (head truncated)\n" + output[len(output)-maxLen:]
	}

	var sb strings.Builder
	skipping := false
	for i, line := range lines {
		if !keep[i] {
			skipping = true
			continue
		}
		if skipping {
			sb.WriteString("...\n")
			skipping = false
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
		if sb.Len() >= maxLen {
			sb.WriteString("... (truncated)")
			break
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ShouldRetry determines if gates should be retried with Claude
func ShouldRetry(config *Config, results *CheckResults, attempt int) bool {
	if results.AllPassed {
		return false
	}

	if config.OnFailure.Action != ActionRetry {
		return false
	}

	// A missing runner binary is not something Claude can fix by editing code.
	// Only skip the retry when no failed required gate is fixable.
	if RunnerMissingOnly(config, results) {
		return false
	}

	return attempt < config.OnFailure.MaxRetries
}

// RunnerMissingOnly reports whether a required gate failed because its runner
// binary is missing and no other required gate failed for a fixable reason.
func RunnerMissingOnly(config *Config, results *CheckResults) bool {
	runnerMissing, fixable := false, false
	for i, res := range results.Results {
		if res == nil || res.Status != StatusFailed {
			continue
		}
		if i < len(config.Gates) && !config.Gates[i].Required {
			continue
		}
		if res.RunnerMissing() {
			runnerMissing = true
		} else {
			fixable = true
		}
	}
	return runnerMissing && !fixable
}
