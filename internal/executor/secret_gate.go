package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
	"github.com/qf-studio/pilot/internal/memory"
)

// AlertEventTypeSecurityEvent mirrors alerts.EventTypeSecurityEvent
// ("security_event") byte-for-byte; EngineAdapter forwards it by string cast.
const AlertEventTypeSecurityEvent AlertEventType = "security_event"

// SecurityEventSourceSecretGate is the AlertEvent.Source of the push gate's
// events. The alerts engine routes them to secret_leak rules.
const SecurityEventSourceSecretGate = "secret_gate"

var (
	secretGateMu     sync.RWMutex
	secretGateAlerts AlertEventProcessor
)

// SetSecretGateAlertProcessor sets where the secret gate sends its
// security_event alerts. Runner.SetAlertProcessor calls it, so the gate in
// internal/autopilot reaches the same alerts engine. A package-level seam
// because the push sites are spread over packages that share no object.
func SetSecretGateAlertProcessor(p AlertEventProcessor) {
	secretGateMu.Lock()
	secretGateAlerts = p
	secretGateMu.Unlock()
}

// SecretGateProject is one project's settings for the push gate.
type SecretGateProject struct {
	// DefaultBranch is the project's configured default branch (the push base).
	DefaultBranch string
	// AllowPaths are globs of files that may show secret shapes (never values).
	AllowPaths []string
}

var (
	secretGateProjMu    sync.RWMutex
	secretGateProjects  = map[string]SecretGateProject{} // by repository identity, see gateRepoKey
	secretGateAllowPath []string                         // security.secret_gate.allow_paths, every project
)

// SetSecretGateAllowPaths sets the allow_paths globs that apply to every project.
func SetSecretGateAllowPaths(globs []string) {
	secretGateProjMu.Lock()
	secretGateAllowPath = append([]string(nil), globs...)
	secretGateProjMu.Unlock()
}

// SetSecretGateProject records a project's gate settings. A worktree of the
// project resolves to the same entry (the key is the repository's common git
// dir), so the Runner's per-task worktrees and the autopilot's scratch
// worktrees see the project's default branch and allow_paths.
func SetSecretGateProject(projectPath string, p SecretGateProject) {
	key := gateRepoKey(context.Background(), projectPath)
	if key == "" {
		return
	}
	secretGateProjMu.Lock()
	secretGateProjects[key] = p
	secretGateProjMu.Unlock()
}

// ResetSecretGateConfigForTest clears every gate setting.
func ResetSecretGateConfigForTest() {
	secretGateProjMu.Lock()
	secretGateProjects = map[string]SecretGateProject{}
	secretGateAllowPath = nil
	secretGateProjMu.Unlock()
}

// gateRepoKey identifies the repository dir belongs to: its common git dir,
// the same for the main checkout and all of its worktrees. "" when dir is not
// in a git repository.
func gateRepoKey(ctx context.Context, dir string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-common-dir")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	if resolved, err := filepath.EvalSymlinks(common); err == nil {
		common = resolved
	}
	return filepath.Clean(common)
}

// secretGateOptions builds the scan options for a push from dir.
func secretGateOptions(ctx context.Context, dir string) secretpatterns.PushOptions {
	key := gateRepoKey(ctx, dir)
	secretGateProjMu.RLock()
	defer secretGateProjMu.RUnlock()
	opts := secretpatterns.PushOptions{AllowPaths: append([]string(nil), secretGateAllowPath...)}
	if p, ok := secretGateProjects[key]; ok && key != "" {
		opts.DefaultBranch = p.DefaultBranch
		opts.AllowPaths = append(opts.AllowPaths, p.AllowPaths...)
	}
	return opts
}

// IsPushRefused reports whether err is the secret gate's refusal. Nothing
// retries it: the same commits hold the same secret on every attempt.
func IsPushRefused(err error) bool {
	return errors.Is(err, secretpatterns.ErrPushRefused)
}

// CheckSecretGate refuses a push of head from dir when the commits it would
// publish add a registered secret value or a known secret shape (GH-6263). On a
// hit it raises a security_event alert and returns an error that satisfies
// IsPushRefused; the branch stays local. Any other error means the gate could
// not run, and the caller must not push either. taskID only names the task in
// the alert and may be empty.
func CheckSecretGate(ctx context.Context, dir, head, taskID string) error {
	err := secretpatterns.CheckPush(ctx, dir, head, secretGateOptions(ctx, dir))
	if err == nil {
		return nil
	}
	var pre *secretpatterns.PushRangeError
	if !errors.As(err, &pre) {
		// Not a secret hit: the gate could not clear the push (the base could not
		// be computed, a commit is too large to scan, or git failed). No alert.
		slog.Warn("Secret gate could not scan the push range", slog.String("task_id", taskID), slog.Any("error", err))
		return err
	}
	slog.Error("Push refused by the secret gate",
		slog.String("task_id", taskID), slog.String("head", head),
		slog.String("commit", pre.Commit), slog.String("file", pre.File), slog.String("kind", pre.Kind))

	secretGateMu.RLock()
	sink := secretGateAlerts
	secretGateMu.RUnlock()
	if sink != nil {
		sink.ProcessEvent(AlertEvent{
			Type:   AlertEventTypeSecurityEvent,
			Source: SecurityEventSourceSecretGate,
			TaskID: taskID,
			Error:  pre.Error(),
			Metadata: map[string]string{
				"commit": pre.Commit,
				"file":   pre.File,
				"kind":   pre.Kind,
				"branch": head,
			},
			Timestamp: time.Now(),
		})
	}
	return err
}

// pushRefusedMarker starts the result.Error of a push the secret gate refused.
// TerminalStatus and IsPermanentFailure key off it: it is a terminal, non-infra
// outcome, and the error text carries no "push failed" infra signature.
const pushRefusedMarker = "push refused"

// pushFailureForgiven reports whether a failed Push of branch may be ignored
// because the data is already on origin (the GH-1389 chdir false positive).
// A refusal by the secret gate is never forgiven (GH-6272): the commits did not
// leave the machine, and an earlier push of the same branch only means the
// remote holds a stale head, not this one. Every Push call site that tolerates
// an existing remote branch goes through here.
func pushFailureForgiven(ctx context.Context, git *GitOperations, branch string, err error) bool {
	if IsPushRefused(err) {
		return false
	}
	return git.RemoteBranchExists(ctx, branch)
}

// failPushRefused ends the run on a secret-gate refusal: needs_human, never
// infra and never re-picked. It pins the commits under a recovery ref (the
// worktree is wiped on restart), applies pilot-needs-human and posts one issue
// comment naming the commit and the file, never the value (GH-6272). Nothing
// was pushed, so no PR is opened.
func (r *Runner) failPushRefused(ctx context.Context, task *Task, git *GitOperations, result *ExecutionResult, log *slog.Logger, pushErr error) {
	holdCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	msg := pushErr.Error()
	if !strings.HasPrefix(msg, pushRefusedMarker) {
		msg = pushRefusedMarker + ": " + msg
	}
	recoveryRef := ""
	if git != nil {
		if ref, refErr := git.CreateRecoveryRef(holdCtx, task.ID, "HEAD"); refErr == nil {
			recoveryRef = ref
		} else {
			log.Warn("push refused: recovery ref failed", slog.String("task_id", task.ID), slog.Any("error", refErr))
		}
	}
	if recoveryRef != "" {
		msg += fmt.Sprintf(" — recovery: branch=%s recovery_ref=%s", task.Branch, recoveryRef)
	}

	if task.SourceAdapter == "" || task.SourceAdapter == "github" {
		if issueNum := task.GHIssueRef(); issueNum != "" {
			body := fmt.Sprintf("Pilot parked this task under `pilot-needs-human`: %s.\n\nNothing was pushed and no PR was opened. Remove the secret from the commit (and rotate it if it is real), then clear the label to let Pilot retry.", pushErr.Error())
			if recoveryRef != "" {
				body += fmt.Sprintf("\n\nThe commits are kept on the Pilot host under `%s`.", recoveryRef)
			}
			if err := ghIssueComment(holdCtx, task.ProjectPath, issueNum, body); err != nil {
				log.Warn("push refused: failed to post the issue comment", slog.String("task_id", task.ID), slog.Any("error", err))
			}
			if err := ghEditLabels(holdCtx, task.ProjectPath, issueNum, []string{labelPilotNeedsHuman}, []string{"pilot-in-progress", labelPilotRetryReady}); err != nil {
				log.Warn("push refused: failed to apply pilot-needs-human", slog.String("task_id", task.ID), slog.Any("error", err))
			}
		}
	}

	pushed := false
	result.BranchPushed = &pushed
	result.Success = false
	result.Outcome = "needs_human"
	result.Error = msg
	r.reportProgress(task.ID, "NeedsHuman", 100, msg)
	r.recordExecutionEvent(task.LogExecutionID(), memory.StageFailed, msg)
}
