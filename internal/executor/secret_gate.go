package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
	secretGatePending   = map[string]SecretGateProject{} // by project path: not a git repository when registered
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
//
// A path that is not a git repository yet (the config loads before a clone or
// init finishes) is kept by path and resolved on the first push from the
// project's repository, so its default branch and allow_paths still apply.
func SetSecretGateProject(projectPath string, p SecretGateProject) {
	key := gateRepoKey(context.Background(), projectPath)
	secretGateProjMu.Lock()
	defer secretGateProjMu.Unlock()
	if key == "" {
		slog.Warn("Secret gate: project path is not a git repository yet; its settings apply once it is",
			slog.String("path", projectPath))
		secretGatePending[filepath.Clean(projectPath)] = p
		return
	}
	secretGateProjects[key] = p
}

// ResetSecretGateConfigForTest clears every gate setting.
func ResetSecretGateConfigForTest() {
	secretGateProjMu.Lock()
	secretGateProjects = map[string]SecretGateProject{}
	secretGatePending = map[string]SecretGateProject{}
	secretGateAllowPath = nil
	secretGateProjMu.Unlock()
	resetAlertedCommits()
}

func resetAlertedCommits() {
	alertedMu.Lock()
	alertedCommits = map[string]struct{}{}
	alertedMu.Unlock()
}

// SecretGateOptionsFor returns the scan options the gate would use for a push
// from dir: the registered default branch and allow_paths of the project that
// dir belongs to, plus the global allow_paths.
func SecretGateOptionsFor(ctx context.Context, dir string) secretpatterns.PushOptions {
	return secretGateOptions(ctx, dir)
}

// gateRepoKey identifies the repository dir belongs to: its common git dir,
// the same for the main checkout and all of its worktrees. "" when dir is not
// in a git repository.
func gateRepoKey(ctx context.Context, dir string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-common-dir")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_NO_REPLACE_OBJECTS=1") // like every git call of the gate
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
	promoteSecretGatePending(ctx, key)
	secretGateProjMu.RLock()
	defer secretGateProjMu.RUnlock()
	opts := secretpatterns.PushOptions{AllowPaths: append([]string(nil), secretGateAllowPath...)}
	if p, ok := secretGateProjects[key]; ok && key != "" {
		opts.DefaultBranch = p.DefaultBranch
		opts.AllowPaths = append(opts.AllowPaths, p.AllowPaths...)
	}
	return opts
}

// promoteSecretGatePending moves the pending projects that are the repository
// key into the keyed registry (see SetSecretGateProject).
func promoteSecretGatePending(ctx context.Context, key string) {
	if key == "" {
		return
	}
	secretGateProjMu.RLock()
	paths := make([]string, 0, len(secretGatePending))
	for path := range secretGatePending {
		paths = append(paths, path)
	}
	secretGateProjMu.RUnlock()
	for _, path := range paths {
		if gateRepoKey(ctx, path) != key {
			continue
		}
		secretGateProjMu.Lock()
		if p, ok := secretGatePending[path]; ok {
			delete(secretGatePending, path)
			if _, set := secretGateProjects[key]; !set {
				secretGateProjects[key] = p
			}
		}
		secretGateProjMu.Unlock()
	}
}

// IsPushRefused reports whether err is the secret gate's refusal. Nothing
// retries it: the same commits hold the same secret on every attempt.
func IsPushRefused(err error) bool {
	return errors.Is(err, secretpatterns.ErrPushRefused)
}

// secretGateUnavailableError is the gate failing to run (git failed inside the
// scan). The text is the cause's; the type lets pushNeverForgiven tell it from
// an ordinary push error.
type secretGateUnavailableError struct{ err error }

func (e *secretGateUnavailableError) Error() string { return e.err.Error() }
func (e *secretGateUnavailableError) Unwrap() error { return e.err }

// IsSecretGateUnavailable reports whether err is the gate failing to run: the
// push was neither cleared nor refused, and nothing was pushed.
func IsSecretGateUnavailable(err error) bool {
	var u *secretGateUnavailableError
	return errors.As(err, &u)
}

// alertedCommits remembers the (task, commit) pairs already alerted, so the
// auto-preserve push and the final push of one task raise one alert for one
// refused commit (GH-6284). Reset by ResetSecretGateConfigForTest; bounded by
// maxAlertedCommits.
var (
	alertedMu      sync.Mutex
	alertedCommits = map[string]struct{}{}
)

const maxAlertedCommits = 4096

// firstAlertFor reports whether the (task, commit) pair has not alerted yet and
// records it. A call without a task ID is never deduplicated: the autopilot
// sites have no task to key on, and each of their runs is its own event.
//
// Decision: a commit that is not a SHA is not a key either (GH-6318). The gate
// names the ref instead of a commit when its context ended before it could
// resolve the head, and a ref name would stand for different commits over time.
func firstAlertFor(taskID, commit string) bool {
	if taskID == "" || !looksLikeCommitID(commit) {
		return true
	}
	alertedMu.Lock()
	defer alertedMu.Unlock()
	key := taskID + "\x00" + commit
	if _, seen := alertedCommits[key]; seen {
		return false
	}
	if len(alertedCommits) >= maxAlertedCommits {
		alertedCommits = map[string]struct{}{}
	}
	alertedCommits[key] = struct{}{}
	return true
}

// looksLikeCommitID reports whether s is a commit SHA as the gate prints it:
// 12 to 64 hex digits (the short form of 12 up to a full SHA-256 id).
func looksLikeCommitID(s string) bool {
	if len(s) < 12 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// checkPushForGate is the scan CheckSecretGate runs; a var so a test can make
// the gate fail the way a broken git does, or refuse on the second push only.
var checkPushForGate = secretpatterns.CheckPush

// CheckSecretGate refuses a push of head from dir when the commits it would
// publish add a registered secret value or a known secret shape (GH-6263). On a
// hit it raises a security_event alert and returns an error that satisfies
// IsPushRefused; the branch stays local. A scan refusal (base not computable,
// size cap) satisfies IsPushRefused too but raises no alert: nothing was found.
// Any other error means the gate could not run, and the caller must not push
// either. taskID names the task in the alert and may be empty; one task alerts
// once per commit.
func CheckSecretGate(ctx context.Context, dir, head, taskID string) error {
	err := checkPushForGate(ctx, dir, head, secretGateOptions(ctx, dir))
	if err == nil {
		return nil
	}
	var pre *secretpatterns.PushRangeError
	if !errors.As(err, &pre) {
		// Not a secret hit: the gate could not clear the push (the base could not
		// be computed, a commit is too large to scan, or git failed). No alert.
		slog.Warn("Secret gate could not scan the push range", slog.String("task_id", taskID), slog.Any("error", err))
		if IsPushRefused(err) {
			return err
		}
		return &secretGateUnavailableError{err: err}
	}
	slog.Error("Push refused by the secret gate",
		slog.String("task_id", taskID), slog.String("head", head),
		slog.String("commit", pre.Commit), slog.String("file", pre.File), slog.String("kind", pre.Kind))

	secretGateMu.RLock()
	sink := secretGateAlerts
	secretGateMu.RUnlock()
	if sink != nil && firstAlertFor(taskID, pre.Commit) {
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

// pushNeverForgiven reports whether a failed Push stopped at the secret gate:
// the gate refused the commits (GH-6272) or could not run (the caller must not
// push either). The commits did not leave the machine, so no remote state
// excuses the failure, and retrying a refusal changes nothing. The one place
// that classifies a push error as a gate stop; every push site goes through it.
func pushNeverForgiven(err error) bool {
	return IsPushRefused(err) || IsSecretGateUnavailable(err)
}

// pushFailureForgiven reports whether a failed Push of branch may be ignored
// because the data is already on origin (the GH-1389 chdir false positive).
// A gate stop is never forgiven (GH-6272): an earlier push of the same branch
// only means the remote holds a stale head, not this one. Every Push call site
// that tolerates an existing remote branch goes through here.
func pushFailureForgiven(ctx context.Context, git *GitOperations, branch string, err error) bool {
	if pushNeverForgiven(err) {
		return false
	}
	return git.RemoteBranchExists(ctx, branch)
}

// refusalComment is the issue comment of a refused push. The text follows the
// cause: a secret hit asks to remove and rotate it, a scan refusal (the push base
// could not be computed, the size cap) names the cause and says no secret was
// found (GH-6284). It says "no PR was opened" only for a task that opens one: a
// continuation of an existing PR (FromPR) has its PR already.
func refusalComment(task *Task, pushErr error, recoveryRef string) string {
	body := fmt.Sprintf("Pilot parked this task under `pilot-needs-human`: %s.\n\n", pushErr.Error())
	nothingPushed := "Nothing was pushed"
	if task.FromPR == 0 {
		nothingPushed += " and no PR was opened"
	}
	var scan *secretpatterns.PushScanError
	if errors.As(pushErr, &scan) {
		body += "No secret was found: the secret gate could not check the commits, so it held them back. " + nothingPushed +
			". Fix the cause above (for a missing push base, fetch the default branch or set the project's `default_branch`; for the size cap, split the change), then clear the label to let Pilot retry."
	} else {
		body += nothingPushed + ". Remove the secret from the commit (and rotate it if it is real), then clear the label to let Pilot retry."
	}
	if recoveryRef != "" {
		body += fmt.Sprintf("\n\nThe commits are kept on the Pilot host under `%s`.", recoveryRef)
	}
	return body
}

// failPushRefused ends the run on a secret-gate refusal: needs_human, never
// infra and never re-picked. It pins the commits under a recovery ref (the
// worktree is wiped on restart), applies pilot-needs-human and posts one issue
// comment naming the commit and the file, never the value (GH-6272). Nothing
// was pushed.
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
			body := refusalComment(task, pushErr, recoveryRef)
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
