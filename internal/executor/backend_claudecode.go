package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

// GracePeriod is the time to wait after context cancellation before hard killing the process.
// This allows the process to clean up gracefully if it responds to SIGTERM.
const GracePeriod = 5 * time.Second

// MaxStderrBufferBytes caps the in-memory stderr buffer for a single Claude Code
// invocation. GH-2332: long-running Navigator sessions (10+ min, 100+ tool calls)
// could accumulate unbounded stderr lines and push Pilot into OOM territory.
// When the cap is reached, the oldest bytes are dropped (tail-truncation) so the
// most recent stderr — which classifyClaudeCodeError actually inspects — is kept.
const MaxStderrBufferBytes = 1 << 20 // 1 MiB

// MaxStdoutTailBufferBytes caps the in-memory raw-stdout tail buffer kept
// purely for failure diagnostics (GH-4395). This is intentionally much
// smaller than MaxStderrBufferBytes — it exists to give triage something
// to look at when a nonzero exit produced no stderr and no parsed assistant
// text, not to reconstruct the full session transcript.
const MaxStdoutTailBufferBytes = 64 * 1024 // 64 KiB

// maxStdoutLineBytes caps how much of a single stdout stream-json line the
// reader keeps before treating it as oversized (GH-4519). A line over this
// cap (e.g. a tool result embedding a base64 blob) is truncated rather than
// aborting the read — bufio.Scanner previously errored out with
// bufio.ErrTooLong on exactly this case and silently stopped draining the
// pipe, wedging the child process and freezing the heartbeat.
const maxStdoutLineBytes = 1024 * 1024 // 1 MiB

// stdoutTruncationSnippetBytes bounds how much of an oversized line's kept
// prefix is copied into stdoutTail alongside the truncation marker. Keeping
// this small ensures the marker survives stdoutTail's own tail-truncation
// policy instead of being pushed out by megabytes of low-value snippet data.
const stdoutTruncationSnippetBytes = 256

// DefaultHeartbeatTimeout is the default time to wait for any stream-json event before considering the process hung.
const DefaultHeartbeatTimeout = 5 * time.Minute

// MinHeartbeatTimeout is the minimum allowed heartbeat timeout.
const MinHeartbeatTimeout = 1 * time.Minute

// MaxHeartbeatTimeout is the maximum allowed heartbeat timeout.
const MaxHeartbeatTimeout = 30 * time.Minute

// HeartbeatCheckInterval is how often to check for heartbeat timeout.
const HeartbeatCheckInterval = 30 * time.Second

// HeartbeatCallback is a callback invoked when heartbeat timeout is detected.
// Returns true if the callback wants to handle the timeout (process will be killed).
type HeartbeatCallback func(pid int, lastEventAge time.Duration)

// ClaudeCodeErrorType categorizes different types of Claude Code failures.
// GH-917: Better error classification enables smarter retry decisions.
type ClaudeCodeErrorType string

const (
	// ErrorTypeRateLimit indicates Claude Code hit a rate limit
	ErrorTypeRateLimit ClaudeCodeErrorType = "rate_limit"
	// ErrorTypeInvalidConfig indicates invalid configuration (e.g., --effort max)
	ErrorTypeInvalidConfig ClaudeCodeErrorType = "invalid_config"
	// ErrorTypeAPIError indicates Claude API errors (auth, server errors)
	ErrorTypeAPIError ClaudeCodeErrorType = "api_error"
	// ErrorTypeTimeout indicates the process was killed due to timeout
	ErrorTypeTimeout ClaudeCodeErrorType = "timeout"
	// ErrorTypeOOM indicates the process was OOM-killed (exit 137/139) (GH-2112)
	ErrorTypeOOM ClaudeCodeErrorType = "oom_killed"
	// ErrorTypeShutdownTerminated indicates the process exited with an
	// OOM-signature exit code (137/139) but the run context was already
	// cancelled by our own shutdown/timeout path — the process was killed
	// by us (SIGTERM/SIGKILL via context cancellation), not the kernel's
	// OOM killer. GH-4105.
	ErrorTypeShutdownTerminated ClaudeCodeErrorType = "shutdown_terminated"
	// ErrorTypeSessionNotFound indicates the session for --from-pr or --resume was not found (GH-1267)
	ErrorTypeSessionNotFound ClaudeCodeErrorType = "session_not_found"
	// ErrorTypeNoChanges indicates Claude exited 0 but produced no diff — typically
	// a refusal or "task already done" response. The final assistant message holds
	// the refusal reason. GH-2328.
	ErrorTypeNoChanges ClaudeCodeErrorType = "no_changes"
	// ErrorTypeDeclined indicates Claude explicitly declined the task as unactionable,
	// emitting a structured DECLINED:<reason> marker in its response. GH-2777.
	ErrorTypeDeclined ClaudeCodeErrorType = "declined"
	// ErrorTypeUnknown indicates an unclassified error
	ErrorTypeUnknown ClaudeCodeErrorType = "unknown"
)

// ClaudeCodeError represents a classified error from Claude Code.
type ClaudeCodeError struct {
	Type    ClaudeCodeErrorType
	Message string
	Stderr  string
}

func (e *ClaudeCodeError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s: %s (stderr: %s)", e.Type, e.Message, e.Stderr)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// ErrorType implements BackendError.
func (e *ClaudeCodeError) ErrorType() string { return string(e.Type) }

// ErrorMessage implements BackendError.
func (e *ClaudeCodeError) ErrorMessage() string { return e.Message }

// ErrorStderr implements BackendError.
func (e *ClaudeCodeError) ErrorStderr() string { return e.Stderr }

// classifyClaudeCodeError examines stderr output and exit code to classify the error.
// ctxCancelled indicates whether the run's context was already cancelled (by our
// own shutdown or timeout path) at the time the process exited — GH-4105.
// selfKillReason, when non-empty, names the Pilot-internal watchdog that issued
// the kill directly via cmd.Process.Kill() (e.g. "heartbeat timeout", "watchdog
// timeout") — these kills happen outside the context-cancellation path, so
// ctxCancelled alone doesn't catch them. GH-4412.
// kernelOOMConfirmed indicates the caller already checked dmesg (or, in future,
// a cgroup memory.events oom_kill counter) for a kernel OOM-killer entry naming
// the subprocess PID. A bare 137/139 exit is NOT sufficient evidence of OOM on
// its own — GH-4412: the orphan-running sweep and other SIGKILL sources produce
// the identical exit code, and labeling every one of them "oom_killed" without
// kernel evidence is a misdiagnosis that pollutes metrics and mis-routes retry
// strategy.
func classifyClaudeCodeError(stderr string, originalErr error, ctxCancelled bool, selfKillReason string, kernelOOMConfirmed bool) *ClaudeCodeError {
	// GH-2112: Check exit code first — OOM kills (137=SIGKILL, 139=SIGSEGV) often
	// produce no stderr, so exit code is the only reliable signal.
	if exitCode := extractExitCode(originalErr); exitCode == 137 || exitCode == 139 {
		sigName := "SIGKILL"
		if exitCode == 139 {
			sigName = "SIGSEGV"
		}
		// GH-4105/GH-4412: if we ourselves cancelled the context (shutdown/
		// timeout) OR killed the process directly via a heartbeat/watchdog
		// timeout, the resulting SIGKILL/SIGSEGV-shaped exit is expected
		// process teardown, not a genuine out-of-memory kill.
		if ctxCancelled {
			return &ClaudeCodeError{
				Type:    ErrorTypeShutdownTerminated,
				Message: fmt.Sprintf("Process terminated by %s after context cancellation (exit code %d)", sigName, exitCode),
				Stderr:  strings.TrimSpace(stderr),
			}
		}
		if selfKillReason != "" {
			return &ClaudeCodeError{
				Type:    ErrorTypeShutdownTerminated,
				Message: fmt.Sprintf("Process terminated by %s after Pilot %s (exit code %d)", sigName, selfKillReason, exitCode),
				Stderr:  strings.TrimSpace(stderr),
			}
		}
		// GH-4412: an unexplained 137/139 is NOT automatically OOM. Only
		// label it oom_killed when the caller confirmed a kernel OOM-killer
		// entry for this PID. Otherwise this is an external kill of unknown
		// origin (another process's `kill -9`, a sweep/reaper bug, etc.) —
		// classify it in the same "killed, cause unconfirmed" bucket as the
		// stderr-detected signal-kill case below so it still gets a sane
		// (non-OOM) retry strategy instead of silently defaulting to OOM.
		if kernelOOMConfirmed {
			return &ClaudeCodeError{
				Type:    ErrorTypeOOM,
				Message: fmt.Sprintf("Process killed by %s (exit code %d, confirmed via kernel OOM-killer log)", sigName, exitCode),
				Stderr:  strings.TrimSpace(stderr),
			}
		}
		return &ClaudeCodeError{
			Type:    ErrorTypeTimeout,
			Message: fmt.Sprintf("Process killed by %s (exit code %d, no kernel OOM evidence found)", sigName, exitCode),
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	stderrLower := strings.ToLower(stderr)

	// Rate limit detection
	if strings.Contains(stderrLower, "hit your limit") ||
		strings.Contains(stderrLower, "rate limit") ||
		strings.Contains(stderrLower, "resets") && strings.Contains(stderrLower, "limit") {
		return &ClaudeCodeError{
			Type:    ErrorTypeRateLimit,
			Message: "Claude Code rate limit reached",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// Invalid config detection (effort level, model, etc.)
	if strings.Contains(stderrLower, "effort level") ||
		strings.Contains(stderrLower, "is not available") ||
		strings.Contains(stderrLower, "invalid model") ||
		strings.Contains(stderrLower, "requires --verbose") {
		return &ClaudeCodeError{
			Type:    ErrorTypeInvalidConfig,
			Message: "Invalid Claude Code configuration",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// API errors
	if strings.Contains(stderrLower, "api error") ||
		strings.Contains(stderrLower, "authentication") ||
		strings.Contains(stderrLower, "unauthorized") ||
		strings.Contains(stderrLower, "403") ||
		strings.Contains(stderrLower, "401") {
		return &ClaudeCodeError{
			Type:    ErrorTypeAPIError,
			Message: "Claude API error",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// Session not found (GH-1267: --from-pr or --resume failed)
	// GH-2377: include "no conversation found" — CC emits this exact phrase
	// when --resume targets an evicted/expired session ID.
	if strings.Contains(stderrLower, "session not found") ||
		strings.Contains(stderrLower, "no session") ||
		strings.Contains(stderrLower, "no conversation found") ||
		strings.Contains(stderrLower, "session expired") ||
		strings.Contains(stderrLower, "could not find session") ||
		strings.Contains(stderrLower, "invalid session") {
		return &ClaudeCodeError{
			Type:    ErrorTypeSessionNotFound,
			Message: "Session not found for --from-pr or --resume",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// Timeout/killed
	if strings.Contains(stderrLower, "killed") ||
		strings.Contains(stderrLower, "signal") ||
		strings.Contains(stderrLower, "timeout") {
		return &ClaudeCodeError{
			Type:    ErrorTypeTimeout,
			Message: "Process killed or timed out",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// Unknown error
	msg := "Unknown error"
	if originalErr != nil {
		msg = originalErr.Error()
	}
	return &ClaudeCodeError{
		Type:    ErrorTypeUnknown,
		Message: msg,
		Stderr:  strings.TrimSpace(stderr),
	}
}

// extractExitCode returns the process exit code from an exec.ExitError, or -1 if unavailable.
func extractExitCode(err error) int {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return -1
	}
	// On Unix, check for signal-based termination (128+signal)
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			return 128 + int(ws.Signal())
		}
	}
	return exitErr.ExitCode()
}

// parseClaudeCodeError examines stderr output and exit code to classify the error.
// This function matches the specification in GH-917 and returns error interface.
func parseClaudeCodeError(stderr string, originalErr error, ctxCancelled bool, selfKillReason string, kernelOOMConfirmed bool) error {
	return classifyClaudeCodeError(stderr, originalErr, ctxCancelled, selfKillReason, kernelOOMConfirmed)
}

// kernelOOMEvidenceTimeout bounds how long the dmesg subprocess may run before
// classification gives up and treats the kill as unconfirmed — dmesg must
// never block classification of an already-failed execution. GH-4412.
const kernelOOMEvidenceTimeout = 2 * time.Second

// dmesgTimestampRe matches the `[Mon Jan  2 15:04:05 2006]` prefix `dmesg -T`
// emits on each ring-buffer line.
var dmesgTimestampRe = regexp.MustCompile(`^\[([A-Za-z]{3} [A-Za-z]{3}\s+\d{1,2} \d{2}:\d{2}:\d{2} \d{4})\]`)

// hasKernelOOMEvidence best-effort checks dmesg for a kernel OOM-killer log
// line naming pid, logged at or after since. It returns false — "no evidence"
// — on any failure: dmesg missing, permission denied (kernel.dmesg_restrict,
// common on hardened hosts), or no matching entry. Absence of evidence must
// never be treated as evidence of OOM; the caller falls back to a
// cause-unconfirmed classification instead. GH-4412 (relevant to the #4401
// cgroup work: once subprocesses run inside a delegated cgroup, a
// memory.events oom_kill counter check can be added here alongside dmesg).
func hasKernelOOMEvidence(pid int, since time.Time) bool {
	if pid <= 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), kernelOOMEvidenceTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "dmesg", "-T").Output()
	if err != nil {
		return false
	}
	return dmesgHasRecentOOMKill(string(out), pid, since)
}

// dmesgHasRecentOOMKill scans dmesg -T output for a "Killed process <pid>"
// line (the kernel OOM-killer's signature message) timestamped at or after
// since. Lines whose timestamp can't be parsed are skipped rather than
// counted as evidence — a false negative here is far safer than a false
// positive that mislabels an unrelated, recycled-PID kill as OOM.
func dmesgHasRecentOOMKill(dmesgOutput string, pid int, since time.Time) bool {
	needle := fmt.Sprintf("Killed process %d", pid)
	for _, line := range strings.Split(dmesgOutput, "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		m := dmesgTimestampRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ts, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", m[1], time.Local)
		if err != nil {
			continue
		}
		if !ts.Before(since) {
			return true
		}
	}
	return false
}

// ClaudeCodeBackend implements Backend for Claude Code CLI.
type ClaudeCodeBackend struct {
	config           *ClaudeCodeConfig
	heartbeatTimeout time.Duration
	log              *slog.Logger

	// GH-2371: provider routing env vars injected into the subprocess.
	// Sourced from BackendConfig.APIBaseURL / APIAuthToken / DefaultModel
	// via the factory. Empty = preserve today's CC defaults (OAuth /
	// ~/.claude/settings.json / ANTHROPIC_API_KEY).
	apiBaseURL   string
	apiAuthToken string
	defaultModel string

	// subprocessLimits configures RSS telemetry and optional RLIMIT_AS cap. GH-3028.
	subprocessLimits *SubprocessLimitsConfig
}

// NewClaudeCodeBackend creates a new Claude Code backend.
func NewClaudeCodeBackend(config *ClaudeCodeConfig) *ClaudeCodeBackend {
	if config == nil {
		config = &ClaudeCodeConfig{Command: "claude"}
	}
	if config.Command == "" {
		config.Command = "claude"
	}
	return &ClaudeCodeBackend{
		config:           config,
		heartbeatTimeout: DefaultHeartbeatTimeout,
		log:              logging.WithComponent("executor.claudecode"),
	}
}

// SetHeartbeatTimeout sets a custom heartbeat timeout for this backend.
func (b *ClaudeCodeBackend) SetHeartbeatTimeout(d time.Duration) {
	b.heartbeatTimeout = d
}

// effectiveHeartbeatTimeout applies opts.HeartbeatFloor (GH-4691) on top of
// the backend's own configured/default heartbeat timeout: the larger of the
// two wins, so a per-call floor (set by the runner for high-effort/heavy-
// complexity lanes via effortAwareHeartbeatFloor) can only raise the
// timeout, never lower it below the backend's own configured value. The
// returned source string ("config" or "effort_floor") is logged alongside
// the kill so the effective timeout is diagnosable from one line.
func effectiveHeartbeatTimeout(base, floor time.Duration) (timeout time.Duration, source string) {
	if floor > base {
		return floor, "effort_floor"
	}
	return base, "config"
}

// SetSubprocessLimits configures RSS telemetry and optional memory cap for the
// Claude Code subprocess. GH-3028.
func (b *ClaudeCodeBackend) SetSubprocessLimits(cfg *SubprocessLimitsConfig) {
	b.subprocessLimits = cfg
}

// nodeOptionsEnv returns the NODE_OPTIONS value to inject into the Claude
// Code subprocess env, merging in a --max-old-space-size flag sized from
// cfg.MaxRSSMB when the cap is enabled. Returns "" when the cap is disabled
// (existing is ignored in that case — nothing to inject, the subprocess
// inherits its own NODE_OPTIONS via os.Environ() unchanged). When existing
// is non-empty, the heap flag is appended rather than replacing it, so a
// caller's own NODE_OPTIONS (e.g. --experimental-* flags) survives.
//
// GH-4401: this is a cooperative, heap-only bound — independent of the
// cgroup v2 memory.max cap applied via applyResourceLimits. It costs nothing
// and degrades nothing even when the cgroup cap can't be created (no
// permission/delegation), unlike the removed RLIMIT_AS approach which broke
// fetch outright.
func nodeOptionsEnv(existing string, cfg *SubprocessLimitsConfig) string {
	if cfg == nil || !cfg.Enabled || cfg.MaxRSSMB <= 0 {
		return ""
	}
	heapFlag := fmt.Sprintf("--max-old-space-size=%d", cfg.MaxRSSMB)
	if existing != "" {
		return existing + " " + heapFlag
	}
	return heapFlag
}

// SetProviderEnv configures provider-routing env vars injected into the
// Claude Code subprocess (GH-2371). When any value is non-empty, the
// corresponding ANTHROPIC_* env var is appended to the subprocess env,
// letting a single Pilot config route both Pilot-internal HTTP calls and
// the CC subprocess to a non-Anthropic provider (Z.AI, OpenRouter, etc.).
// All empty = today's behavior (CC uses its own auth).
func (b *ClaudeCodeBackend) SetProviderEnv(baseURL, authToken, model string) {
	b.apiBaseURL = baseURL
	b.apiAuthToken = authToken
	b.defaultModel = model
}

// Name returns the backend identifier.
func (b *ClaudeCodeBackend) Name() string {
	return BackendTypeClaudeCode
}

// IsAvailable checks if Claude Code CLI is installed.
func (b *ClaudeCodeBackend) IsAvailable() bool {
	_, err := exec.LookPath(b.config.Command)
	return err == nil
}

// Execute runs a prompt through Claude Code CLI.
// If --from-pr is used and fails with session not found, it falls back to executing without it.
func (b *ClaudeCodeBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	result, err := b.executeWithFromPR(ctx, opts, true)

	// GH-1267: Fallback if --from-pr fails with session not found
	if err != nil && opts.FromPR > 0 && b.config.UseFromPR {
		if ccErr, ok := err.(*ClaudeCodeError); ok && ccErr.Type == ErrorTypeSessionNotFound {
			b.log.Warn("Session not found for --from-pr, retrying without it",
				slog.Int("pr", opts.FromPR),
				slog.String("error", ccErr.Message),
			)
			// Retry without --from-pr
			return b.executeWithFromPR(ctx, opts, false)
		}
	}

	// GH-2377: Fallback if --resume fails with session not found.
	// Self-review reuses the main-execution session ID to save tokens
	// (GH-1265); when CC has evicted that session, drop --resume and
	// run a fresh session rather than silently skipping self-review.
	if err != nil && opts.ResumeSessionID != "" {
		if ccErr, ok := err.(*ClaudeCodeError); ok && ccErr.Type == ErrorTypeSessionNotFound {
			b.log.Warn("Session not found for --resume, retrying without it",
				slog.String("session_id", opts.ResumeSessionID),
				slog.String("error", ccErr.Message),
			)
			opts.ResumeSessionID = ""
			return b.Execute(ctx, opts)
		}
	}

	return result, err
}

// executeWithFromPR is the internal implementation that allows controlling --from-pr usage.
// When allowFromPR is false, it skips --from-pr even if opts.FromPR is set.
// This enables fallback retry without --from-pr if the session is not found.
func (b *ClaudeCodeBackend) executeWithFromPR(ctx context.Context, opts ExecuteOptions, allowFromPR bool) (*BackendResult, error) {
	// Build command arguments
	var args []string

	// GH-1267: Use --from-pr for session resumption from PR context
	// This takes precedence over --resume since it's more specific.
	if opts.FromPR > 0 && allowFromPR && b.config.UseFromPR {
		args = []string{
			"--from-pr", strconv.Itoa(opts.FromPR),
			"-p", opts.Prompt,
			"--verbose",
			"--output-format", "stream-json",
			// GH-4501: stream delta chunks during a turn (not just one complete
			// message at the end) so a long silent "thinking" turn still emits
			// stdout lines that reset the stall watchdog's idle clock.
			"--include-partial-messages",
			"--dangerously-skip-permissions",
		}
		b.log.Info("Resuming session from PR context",
			slog.Int("pr", opts.FromPR),
		)
	} else if opts.ResumeSessionID != "" {
		// GH-1265: Use --resume for session continuation (e.g., self-review)
		args = []string{
			"--resume", opts.ResumeSessionID,
			"-p", opts.Prompt,
			"--verbose",
			"--output-format", "stream-json",
			"--include-partial-messages", // GH-4501
			"--dangerously-skip-permissions",
		}
		b.log.Info("Resuming session for context continuation",
			slog.String("session_id", opts.ResumeSessionID),
		)
	} else {
		args = []string{
			"-p", opts.Prompt,
			"--verbose",
			"--output-format", "stream-json",
			"--include-partial-messages", // GH-4501
			"--dangerously-skip-permissions",
		}
	}

	// Add model flag if specified (model routing GH-215)
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
		b.log.Info("Using routed model", slog.String("model", opts.Model))
	} else {
		b.log.Info("Omitting --model flag; worker will use its own default",
			slog.String("backend", b.Name()),
			slog.String("task_id", opts.TaskID),
		)
	}

	// Add max-turns flag if set by .pilot/workflow.yaml agent.max_turns (TASK-304)
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(opts.MaxTurns))
		b.log.Info("Using workflow max_turns override", slog.Int("max_turns", opts.MaxTurns))
	}

	// Add effort flag if specified (effort routing)
	// Note: Claude Code CLI may not support --effort yet; this is future-proofed.
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
		b.log.Info("Using routed effort", slog.String("effort", opts.Effort))
	}

	// GH-2432: --allowedTools restricts the subprocess toolbox; --mcp-config
	// scopes which MCP servers (if any) the subprocess loads. Both cut the
	// per-turn token cost considerably when MCPs are not needed.
	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(opts.AllowedTools, ","))
	}
	if opts.MCPConfigPath != "" {
		args = append(args, "--mcp-config", opts.MCPConfigPath)
	}

	args = append(args, b.config.ExtraArgs...)

	cmd := exec.CommandContext(ctx, b.config.Command, args...)
	cmd.Dir = opts.ProjectPath

	// GH-4503: give the subprocess its own process group so every kill path
	// below can reach children Claude Code's Bash tool backgrounds (GH-4357's
	// task_started/task_notification events prove it forks them) instead of
	// orphaning them — see pilot-console GH-24 (gen-0's claude process
	// survived its session kill for 1h14m, 335M RSS, reparented to the
	// daemon). WaitDelay bounds cmd.Wait() so a surviving grandchild holding
	// the stdout/stderr pipes open can't hang it forever.
	configureProcessGroup(cmd)
	// exec.CommandContext's default Cancel signals only cmd.Process
	// (single PID) the instant ctx is done — which is exactly the path the
	// stall/heartbeat watchdogs drive via context cancellation below.
	// Override it to signal the whole process group instead.
	cmd.Cancel = func() error {
		return killProcessGroup(cmd, syscall.SIGKILL)
	}

	// GH-2328: Signal executor mode to the child process. Project `CLAUDE.md`
	// and auto-memory can detect this and skip Navigator-only "DO NOT write
	// code" rules without relying on prompt-prefix heuristics.
	env := append(os.Environ(), "PILOT_EXECUTOR=1")
	// GH-2371: route the CC subprocess to the configured provider when set.
	// Values are appended after os.Environ() so they win on Node's last-write
	// lookup if the user's shell also exports ANTHROPIC_*.
	if b.apiBaseURL != "" {
		env = append(env, "ANTHROPIC_BASE_URL="+b.apiBaseURL)
	}
	if b.apiAuthToken != "" {
		env = append(env, "ANTHROPIC_AUTH_TOKEN="+b.apiAuthToken)
	}
	if b.defaultModel != "" {
		env = append(env, "ANTHROPIC_MODEL="+b.defaultModel)
	}
	// Pass context window and output token env vars if configured (GH-2163).
	if b.config.Disable1MContext {
		env = append(env, "CLAUDE_CODE_DISABLE_1M_CONTEXT=1")
	}
	if b.config.MaxOutputTokens > 0 {
		env = append(env, fmt.Sprintf("CLAUDE_CODE_MAX_OUTPUT_TOKENS=%d", b.config.MaxOutputTokens))
	}
	// GH-4401: cooperative V8 heap cap. NEVER use RLIMIT_AS here (it broke
	// 100% of executor children on Linux — see applyResourceLimits below).
	// NODE_OPTIONS=--max-old-space-size bounds the JS heap without touching
	// virtual address space, so undici/fetch's own mmap-heavy connection
	// pool is unaffected. This is additive to (and independent of) the
	// cgroup v2 memory.max cap applied after cmd.Start().
	if nodeOpts := nodeOptionsEnv(os.Getenv("NODE_OPTIONS"), b.subprocessLimits); nodeOpts != "" {
		env = append(env, "NODE_OPTIONS="+nodeOpts)
	}
	cmd.Env = env

	b.log.Debug("Starting Claude Code",
		slog.String("command", b.config.Command),
		slog.String("project", opts.ProjectPath),
	)

	// Create pipes for output
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start the command
	// GH-4412: recorded so an unexplained 137/139 exit's dmesg evidence check
	// only considers OOM-killer log lines from this run, not a stale entry
	// left over from an earlier process that reused the same PID.
	startTime := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Claude Code: %w", err)
	}
	b.log.Debug("Claude Code started", slog.Int("pid", cmd.Process.Pid))

	// GH-3028/GH-4401: apply RSS cap via a cgroup v2 memory.max leaf on
	// Linux (darwin/other: telemetry-only no-op). The returned cleanup
	// removes the leaf; it's deferred here rather than called eagerly
	// because it must run after cmd.Wait() reaps the process below —
	// cgroup.procs must be empty before rmdir succeeds.
	cleanupResourceLimits := applyResourceLimits(cmd.Process.Pid, b.subprocessLimits)
	defer cleanupResourceLimits()

	// GH-3028: start RSS sampler — collects peak/final RSS for telemetry.
	sampleInterval := 10 * time.Second
	if b.subprocessLimits != nil && b.subprocessLimits.SampleIntervalSec > 0 {
		sampleInterval = time.Duration(b.subprocessLimits.SampleIntervalSec) * time.Second
	}
	rssSamplerCtx, cancelRSSSampler := context.WithCancel(context.Background())
	rssCh := StartRSSSampler(rssSamplerCtx, cmd.Process.Pid, sampleInterval)

	// Track results
	result := &BackendResult{}
	// GH-2332: bounded stderr buffer to prevent OOM on long sessions.
	stderrOutput := newBoundedBuffer(MaxStderrBufferBytes)
	// GH-4395: bounded raw-stdout tail, independent of stream-json parsing —
	// kept as a diagnostic fallback for nonzero exits where stderr is empty.
	stdoutTail := newBoundedBuffer(MaxStdoutTailBufferBytes)
	var wg sync.WaitGroup

	// Channel to signal command completion
	cmdDone := make(chan struct{})

	// GH-4412: track whether Pilot itself killed the process directly via
	// cmd.Process.Kill() (heartbeat/watchdog timeout) rather than through
	// context cancellation. These kills produce the identical 137/139 exit
	// code as a genuine kernel OOM kill but ctx.Err() stays nil for them, so
	// without this flag they were silently mislabeled oom_killed.
	var heartbeatKilled, watchdogKilled atomic.Bool

	// Heartbeat tracking: store last event time as Unix nano (atomic int64)
	var lastEventAt atomic.Int64
	lastEventAt.Store(time.Now().UnixNano())

	// Heartbeat monitor goroutine
	// GH-4668: a silent stdout stream alone is not evidence of a hang — the
	// stream-json protocol emits nothing while a local tool (e.g. `make
	// test`) runs, which routinely exceeds the 5m heartbeat window on this
	// repo. hbMonitor checks process-group liveness (descendant PIDs and/or
	// advancing CPU time) before killing; a genuinely idle, silent group is
	// still killed exactly as before.
	//
	// GH-4691: opts.HeartbeatFloor (set by the runner via
	// effortAwareHeartbeatFloor for high-effort/heavy-complexity lanes) can
	// raise the effective timeout above the backend's flat
	// default/configured value — the hard heartbeat must never fire before
	// the stall watchdog's own effort-aware floor would. timeoutSource is
	// logged on kill so the effective timeout is diagnosable from one line.
	heartbeatTimeout, timeoutSource := effectiveHeartbeatTimeout(b.heartbeatTimeout, opts.HeartbeatFloor)
	hbMonitor := newHeartbeatMonitor(heartbeatTimeout, opts.WatchdogTimeout, probeProcessLiveness)
	heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
	defer cancelHeartbeat()
	logging.SafeGo("executor-backend-claudecode", func() {
		ticker := time.NewTicker(HeartbeatCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-cmdDone:
				return
			case <-ticker.C:
				lastNano := lastEventAt.Load()
				lastTime := time.Unix(0, lastNano)
				now := time.Now()
				decision, descendants, cpuDelta, logGrace, reason, probeErr := hbMonitor.evaluate(now, startTime, lastTime, cmd.Process.Pid)

				switch decision {
				case heartbeatNoAction:
					continue
				case heartbeatGrace:
					if logGrace {
						b.log.Info("heartbeat grace: local tool execution in flight",
							slog.Int("pid", cmd.Process.Pid),
							slog.Duration("last_event_age", now.Sub(lastTime)),
							slog.Int("descendants", descendants),
							slog.Uint64("cpu_delta_ticks", cpuDelta),
						)
					}
					continue
				case heartbeatKill:
					// fall through below
				}

				age := now.Sub(lastTime)
				if probeErr != nil {
					b.log.Warn("Heartbeat: process-liveness probe failed, killing toward safe default",
						slog.Int("pid", cmd.Process.Pid),
						slog.Any("error", probeErr),
					)
				}
				b.log.Warn("Heartbeat timeout detected, killing hung process",
					slog.Int("pid", cmd.Process.Pid),
					slog.Duration("last_event_age", age),
					slog.Duration("timeout", heartbeatTimeout),
					slog.String("timeout_source", timeoutSource),
					slog.String("reason", string(reason)),
					slog.Int("descendants", descendants),
					slog.Uint64("cpu_delta_ticks", cpuDelta),
				)

				// Invoke callback if provided
				if opts.HeartbeatCallback != nil {
					opts.HeartbeatCallback(cmd.Process.Pid, age)
				}

				// Kill the hung process
				// GH-4503: signal the whole process group, not just the
				// tracked PID, so backgrounded grandchildren die too.
				if cmd.Process != nil {
					if err := killProcessGroup(cmd, syscall.SIGKILL); err != nil {
						b.log.Error("Failed to kill hung process",
							slog.Int("pid", cmd.Process.Pid),
							slog.Any("error", err),
						)
					} else {
						// GH-4412: mark self-inflicted so classification
						// doesn't mislabel the resulting 137 as OOM.
						heartbeatKilled.Store(true)
						b.log.Info("Hung process killed successfully",
							slog.Int("pid", cmd.Process.Pid),
						)
					}
				}
				return
			}
		}
	})

	// Watchdog goroutine: hard kill after absolute timeout (GH-882)
	// This is a safety net for processes that ignore context cancellation.
	if opts.WatchdogTimeout > 0 {
		logging.SafeGo("executor-backend-claudecode", func() {
			select {
			case <-cmdDone:
				// Command completed normally, watchdog not needed
				return
			case <-time.After(opts.WatchdogTimeout):
				// Watchdog timeout expired, forcibly kill the process
				if cmd.Process == nil {
					return
				}

				b.log.Warn("Watchdog timeout expired, forcibly killing subprocess",
					slog.Int("pid", cmd.Process.Pid),
					slog.Duration("watchdog_timeout", opts.WatchdogTimeout),
				)

				// Invoke callback before killing (allows alert emission)
				if opts.WatchdogCallback != nil {
					opts.WatchdogCallback(cmd.Process.Pid, opts.WatchdogTimeout)
				}

				// Kill the process
				// GH-4503: signal the whole process group, not just the
				// tracked PID, so backgrounded grandchildren die too.
				if err := killProcessGroup(cmd, syscall.SIGKILL); err != nil {
					b.log.Error("Watchdog failed to kill process",
						slog.Int("pid", cmd.Process.Pid),
						slog.Any("error", err),
					)
				} else {
					// GH-4412: mark self-inflicted so classification doesn't
					// mislabel the resulting 137 as OOM.
					watchdogKilled.Store(true)
					b.log.Info("Watchdog killed process successfully",
						slog.Int("pid", cmd.Process.Pid),
					)
				}
			}
		})
	}

	// Read stdout (stream-json events)
	wg.Add(1)
	logging.SafeGo("executor-backend-claudecode", func() {
		defer wg.Done()
		reader := bufio.NewReaderSize(stdout, 64*1024)

		for {
			lineBytes, truncated, totalBytes, rerr := readBoundedLine(reader, maxStdoutLineBytes, func(n int) {
				// GH-4519: heartbeat must track raw byte flow, not just
				// completed lines — an oversized line spanning many reads
				// must not let the heartbeat monitor think the process is
				// hung while it's still actively streaming data.
				lastEventAt.Store(time.Now().UnixNano())
			})

			if len(lineBytes) > 0 || truncated {
				line := string(lineBytes)

				if truncated {
					// GH-4519: a stream-json line over the 1MB cap (e.g. a
					// tool result carrying a base64 blob) previously made
					// bufio.Scanner abort with ErrTooLong and silently stop
					// draining the pipe — the child then blocked writing to
					// a full pipe, the heartbeat froze, and the heartbeat
					// monitor SIGKILLed a process that was still alive and
					// producing output. Record a bounded marker instead and
					// keep reading subsequent lines.
					snippet := line
					if len(snippet) > stdoutTruncationSnippetBytes {
						snippet = snippet[:stdoutTruncationSnippetBytes]
					}
					stdoutTail.WriteLine(fmt.Sprintf("[line truncated: %d bytes] %s", totalBytes, snippet))
				} else {
					// GH-4395: capture the raw line regardless of whether it parses as
					// a stream-json event — a crash mid-line or non-JSON diagnostic
					// output would otherwise vanish entirely.
					stdoutTail.WriteLine(line)
				}

				if opts.Verbose {
					fmt.Printf("   %s\n", line)
				}

				// GH-4519: an oversized line's kept prefix dropped its
				// closing braces/brackets along with the rest of the line,
				// so it is essentially never complete JSON — only attempt
				// to parse it if it actually validates; otherwise skip
				// parsing this line rather than feeding a corrupt event
				// through.
				if !truncated || json.Valid(lineBytes) {
					// Parse and convert to BackendEvent
					event := b.parseStreamEvent(line)
					if opts.EventHandler != nil {
						opts.EventHandler(event)
					}

					// GH-2328: track the last assistant text block so refusals (Claude
					// exits 0 after politely declining) can be surfaced to the user.
					if event.Type == EventTypeText && event.Message != "" {
						result.LastAssistantText = event.Message
					}

					// Track final result
					if event.Type == EventTypeResult {
						// GH-2103: Cancel heartbeat on result event.
						// On slow I/O flush, the heartbeat timer could fire and kill
						// the process after it had already produced output.
						cancelHeartbeat()

						if event.IsError {
							result.Error = event.Message
						} else {
							result.Output = event.Message
							result.SawSuccessResult = true // GH-2107: track successful result for timeout recovery
						}
						// Cancel heartbeat — process is finishing, don't kill it
						cancelHeartbeat()
					}

					// Capture session ID from init event (GH-1265)
					if event.Type == EventTypeInit && event.SessionID != "" {
						result.SessionID = event.SessionID
					}

					// Accumulate token usage
					result.TokensInput += event.TokensInput
					result.TokensOutput += event.TokensOutput
					result.CacheCreationInputTokens += event.CacheCreationInputTokens
					result.CacheReadInputTokens += event.CacheReadInputTokens
					if event.Model != "" {
						result.Model = event.Model
					}
				}
			}

			if rerr != nil {
				// GH-4519: a reader exiting must never be silent — the
				// previous scanner-based loop had no Err() check at all, so
				// an ErrTooLong (or any other read error) stopped draining
				// stdout with zero diagnostic trace.
				if !errors.Is(rerr, io.EOF) {
					b.log.Warn("stdout reader exited with error",
						slog.String("task_id", opts.TaskID),
						slog.Any("error", rerr),
					)
				}
				return
			}
		}
	})

	// Read stderr
	wg.Add(1)
	logging.SafeGo("executor-backend-claudecode", func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			stderrOutput.WriteLine(line)
			if opts.Verbose {
				fmt.Printf("   [err] %s\n", line)
			}
		}
		if err := scanner.Err(); err != nil {
			// GH-4519: a reader exiting must never be silent.
			b.log.Warn("stderr reader exited with error",
				slog.String("task_id", opts.TaskID),
				slog.Any("error", err),
			)
		}
	})

	// Monitor context for timeout and handle hard kill
	logging.SafeGo("executor-backend-claudecode", func() {
		select {
		case <-cmdDone:
			// Command completed normally, nothing to do
			return
		case <-ctx.Done():
			// Context cancelled (timeout or explicit cancellation)
			// exec.CommandContext will send SIGTERM/interrupt, wait grace period then SIGKILL
			if cmd.Process == nil {
				return
			}

			b.log.Warn("Context cancelled, waiting grace period before hard kill",
				slog.Int("pid", cmd.Process.Pid),
				slog.Duration("grace_period", GracePeriod),
			)

			// Wait for grace period or command to exit
			select {
			case <-cmdDone:
				// Process exited gracefully after signal
				b.log.Debug("Process exited gracefully after context cancellation",
					slog.Int("pid", cmd.Process.Pid),
				)
				return
			case <-time.After(GracePeriod):
				// Grace period expired, hard kill
				if cmd.Process != nil {
					b.log.Warn("Grace period expired, sending SIGKILL",
						slog.Int("pid", cmd.Process.Pid),
					)
					// GH-4503: signal the whole process group, not just the
					// tracked PID. Note cmd.Cancel (set at spawn) already
					// group-kills on ctx.Done(), so this is a defense-in-depth
					// re-strike rather than the primary kill.
					if err := killProcessGroup(cmd, syscall.SIGKILL); err != nil {
						b.log.Error("Failed to kill process",
							slog.Int("pid", cmd.Process.Pid),
							slog.Any("error", err),
						)
					} else {
						b.log.Info("Process killed successfully",
							slog.Int("pid", cmd.Process.Pid),
						)
					}
				}
			}
		}
	})

	// Wait for output readers
	wg.Wait()

	// Wait for command to complete
	err = cmd.Wait()
	close(cmdDone) // Signal that command is done

	// GH-3028: collect RSS sample (cancelling the sampler goroutine triggers final read).
	cancelRSSSampler()
	if rssSample, ok := <-rssCh; ok {
		result.PeakRSSMB = rssSample.PeakMB
		result.FinalRSSMB = rssSample.FinalMB
		if rssSample.PeakMB > 0 {
			b.log.Debug("Subprocess RSS telemetry",
				slog.Int("peak_rss_mb", rssSample.PeakMB),
				slog.Int("final_rss_mb", rssSample.FinalMB),
			)
		}
	}

	if err != nil {
		// GH-2107: If a successful result event was seen before the process exited with
		// an error, the work was completed but Claude Code timed out on a subsequent turn
		// (e.g., writing final summary). Recover as success.
		if result.SawSuccessResult {
			b.log.Info("Recovering success: process exited with error after successful result event (GH-2107)",
				slog.String("exit_error", err.Error()),
				slog.String("output_preview", truncate(result.Output, 200)),
			)
			result.Success = true
			return result, nil
		}

		result.Success = false

		// GH-917: Classify the error for better handling.
		// GH-4105: pass whether the run context was already cancelled (our own
		// shutdown/timeout path) so 137/139 exits during teardown aren't
		// misclassified as OOM kills.
		stderr := stderrOutput.String()
		ctxCancelled := ctx.Err() != nil

		// GH-4412: heartbeat/watchdog kills call cmd.Process.Kill() directly
		// (not through context cancellation), so ctxCancelled alone misses
		// them — without this they were mislabeled oom_killed too.
		selfKillReason := ""
		switch {
		case heartbeatKilled.Load():
			selfKillReason = "heartbeat timeout"
		case watchdogKilled.Load():
			selfKillReason = "watchdog timeout"
		}

		// GH-4412: an unexplained 137/139 (not our own shutdown/heartbeat/
		// watchdog kill) requires kernel dmesg evidence before it's labeled
		// OOM — the exit code alone is also produced by external SIGKILL
		// sources (e.g. the orphan-running sweep's prior kill-and-mislabel
		// bug this task is part of).
		kernelOOMConfirmed := false
		if !ctxCancelled && selfKillReason == "" {
			if exitCode := extractExitCode(err); exitCode == 137 || exitCode == 139 {
				kernelOOMConfirmed = hasKernelOOMEvidence(cmd.Process.Pid, startTime)
			}
		}

		ccErr := parseClaudeCodeError(stderr, err, ctxCancelled, selfKillReason, kernelOOMConfirmed).(*ClaudeCodeError)

		// GH-2328: surface the raw stderr + classification so the runner can
		// write them to execution_logs. Without this, "unknown: exit status 1"
		// is all the user ever sees and diagnosis requires re-running with a
		// patched binary.
		result.Stderr = stderr
		result.ErrorType = string(ccErr.Type)
		// GH-4395: always attach the raw stdout tail on failure — the primary
		// value is when stderr and LastAssistantText are both empty (the
		// exact "unknown: exit status 1" signature with no diagnostics), but
		// it's cheap to keep even when other diagnostics are present.
		result.StdoutTail = stdoutTail.String()

		// GH-2112: Log OOM kills at error level for monitoring
		if ccErr.Type == ErrorTypeOOM {
			b.log.Error("Claude Code process OOM-killed",
				slog.String("error_type", string(ccErr.Type)),
				slog.String("message", ccErr.Message),
				slog.String("stderr", ccErr.Stderr),
			)
		} else {
			b.log.Warn("Claude Code execution failed",
				slog.String("error_type", string(ccErr.Type)),
				slog.String("message", ccErr.Message),
				slog.String("stderr", ccErr.Stderr),
			)
		}

		// Store classified error info in result
		if result.Error == "" {
			result.Error = ccErr.Error()
		}

		// Return classified error for upstream handling
		return result, ccErr
	}

	result.Success = true
	// GH-2328: expose stderr on success too so warnings (e.g. rate-limit
	// overage rejected, context window warnings) can be logged.
	result.Stderr = stderrOutput.String()
	return result, nil
}

// truncate returns the first n characters of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// readBoundedLine reads a single newline-terminated line from r, keeping at
// most maxBytes of it. Unlike bufio.Scanner (which aborts the entire read
// with bufio.ErrTooLong the moment a line exceeds its fixed buffer), this
// keeps draining an oversized line to its end so the caller can continue
// reading subsequent lines — GH-4519: a silently-abandoned oversized line
// left the child process blocked writing to a full pipe, which froze the
// heartbeat and got the whole (otherwise-healthy) process SIGKILLed.
//
// onBytes, if non-nil, is invoked with the length of each underlying chunk
// read — including chunks belonging to a line that ends up truncated — so
// callers can drive a heartbeat off raw byte flow rather than only
// completed lines.
//
// It returns the (possibly truncated) line content without the trailing
// newline, whether truncation occurred, the original untruncated line
// length in bytes, and any terminal read error (e.g. io.EOF).
func readBoundedLine(r *bufio.Reader, maxBytes int, onBytes func(n int)) (line []byte, truncated bool, totalBytes int, err error) {
	var buf []byte
	for {
		chunk, isPrefix, rerr := r.ReadLine()
		if len(chunk) > 0 {
			if onBytes != nil {
				onBytes(len(chunk))
			}
			totalBytes += len(chunk)
			if len(buf) < maxBytes {
				remaining := maxBytes - len(buf)
				take := chunk
				if len(take) > remaining {
					take = take[:remaining]
				}
				buf = append(buf, take...)
				if len(take) < len(chunk) {
					truncated = true
				}
			} else {
				truncated = true
			}
		}
		if rerr != nil {
			return buf, truncated, totalBytes, rerr
		}
		if !isPrefix {
			return buf, truncated, totalBytes, nil
		}
	}
}

// parseStreamEvent converts Claude Code stream-json to BackendEvent.
func (b *ClaudeCodeBackend) parseStreamEvent(line string) BackendEvent {
	event := BackendEvent{
		Raw: line,
	}

	var streamEvent StreamEvent
	if err := json.Unmarshal([]byte(line), &streamEvent); err != nil {
		// Not valid JSON, return as-is
		event.Type = EventTypeText
		event.Message = line
		return event
	}

	// Map stream event type to backend event type
	switch streamEvent.Type {
	case "stream_event":
		// GH-4501: partial-message chunk from --include-partial-messages
		// (message_start, content_block_start/delta/stop, message_delta,
		// message_stop). The complete "assistant" event for this turn still
		// arrives separately once the turn finishes, so classify these as a
		// distinct no-op event type rather than re-deriving text/tool_use from
		// them — that would double-process the same content. Every stdout
		// line already resets the stall watchdog's idle clock unconditionally
		// (see the EventHandler call site in Execute), so simply existing is
		// enough for these to do their job.
		event.Type = EventTypeStreamDelta
		if streamEvent.Event != nil {
			event.Message = streamEvent.Event.Type
		}

	case "system":
		switch streamEvent.Subtype {
		case "init":
			event.Type = EventTypeInit
			event.SessionID = streamEvent.SessionID // Capture session ID for resume (GH-1265)
			event.Message = "Claude Code initialized"
		case "task_started":
			// GH-4357: a backgrounded task (e.g. long-running Bash command or
			// sub-agent) started. Plain shell backgrounds (task_type=local_bash)
			// emit no further events until completion, so the runner must track
			// this as in-flight activity to keep the stall watchdog from
			// killing an otherwise healthy session.
			event.Type = EventTypeTaskStarted
			event.BackgroundTaskID = streamEvent.TaskID
			event.Message = "Background task started"
		case "task_notification":
			event.Type = EventTypeTaskNotification
			event.BackgroundTaskID = streamEvent.TaskID
			event.BackgroundTaskStatus = streamEvent.Status
			event.Message = "Background task finished"
		}

	case "assistant":
		if streamEvent.Message != nil {
			for _, block := range streamEvent.Message.Content {
				switch block.Type {
				case "tool_use":
					event.Type = EventTypeToolUse
					event.ToolName = block.Name
					event.ToolInput = block.Input
					event.Message = fmt.Sprintf("Using %s", block.Name)
				case "text":
					event.Type = EventTypeText
					event.Message = block.Text
				}
			}
		}

	case "user":
		// Tool results
		if streamEvent.ToolUseResult != nil {
			event.Type = EventTypeToolResult
			var toolResult ToolResultContent
			if err := json.Unmarshal(streamEvent.ToolUseResult, &toolResult); err == nil {
				event.ToolResult = toolResult.Content
				event.IsError = toolResult.IsError
			}
		}

	case "result":
		event.Type = EventTypeResult
		event.Message = streamEvent.Result
		event.IsError = streamEvent.IsError
	}

	// Capture usage info
	if streamEvent.Usage != nil {
		event.TokensInput = streamEvent.Usage.InputTokens
		event.TokensOutput = streamEvent.Usage.OutputTokens
		event.CacheCreationInputTokens = streamEvent.Usage.CacheCreationInputTokens
		event.CacheReadInputTokens = streamEvent.Usage.CacheReadInputTokens
	}
	if streamEvent.Model != "" {
		event.Model = streamEvent.Model
	}

	return event
}
