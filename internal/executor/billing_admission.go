package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/qf-studio/pilot/internal/memory"
)

// AdmissionPauseOwnerBillingKey is the Dispatcher.PauseAdmissionFor /
// ResumeAdmissionFor owner key for the api_key billing pause (GH-6016).
// Distinct from the usage-limit, self-upgrade and platform-breaker owners so
// none of them can undo another.
const AdmissionPauseOwnerBillingKey = "billing-key"

// ErrBillingKeyHold marks a task held at admission because billing.mode is
// api_key and the key is unavailable. It wraps ErrDispatchGated: the task was
// declined, not failed, and callers leave it for the next poll.
var ErrBillingKeyHold = fmt.Errorf("billing key unavailable, admission paused: %w", ErrDispatchGated)

// billingResolverOrConfig returns the resolver for the current config.
func (d *Dispatcher) billingResolverOrConfig() *BillingResolver {
	if d.billingResolver != nil {
		return d.billingResolver
	}
	return NewBillingResolver(d.billingConfig())
}

func (d *Dispatcher) billingConfig() *BillingConfig {
	if d.runner != nil && d.runner.config != nil && d.runner.config.ClaudeCode != nil {
		return d.runner.config.ClaudeCode.Billing
	}
	return nil
}

// billingApplies reports whether billing_mode describes this daemon's runs:
// only the claude CLI picks its credential from the subprocess env.
func (d *Dispatcher) billingApplies() bool {
	if d.runner == nil || d.runner.config == nil {
		return true
	}
	t := d.runner.config.Type
	return t == "" || t == BackendTypeClaudeCode
}

// admitBilling resolves the credential of the execution about to begin and
// stamps task.billingMode. A nil credential with a nil error means billing
// does not apply. While the billing-key pause is held it returns the last
// resolver error without running the resolver; the retry timer owns that.
func (d *Dispatcher) admitBilling(task *Task) (*BillingCredential, error) {
	if !d.billingApplies() {
		task.billingMode = ""
		return nil, nil
	}
	resolver := d.billingResolverOrConfig()
	if resolver.Mode() == BillingModeAPIKey {
		d.billingMu.Lock()
		held, lastErr := d.billingHeld, d.billingLastErr
		d.billingMu.Unlock()
		if held {
			return nil, lastErr
		}
	}
	cred, err := resolver.Resolve(context.Background())
	if err != nil {
		task.billingMode = string(BillingModeAPIKey)
		return nil, err
	}
	task.billingMode = cred.MemoryMode()
	return cred, nil
}

// beginWithGenerationRetry resolves the billing credential once for this
// execution (GH-6016), then claims and creates the row stamped with its mode.
// An unresolved api_key fails closed: see failBillingKeyUnavailable.
func (d *Dispatcher) beginWithGenerationRetry(task *Task, initial Status) (string, error) {
	cred, err := d.admitBilling(task)
	if err != nil {
		return d.failBillingKeyUnavailable(task, err)
	}
	execID, err := d.beginClaimWithGenerationRetry(task, initial)
	if err != nil || execID == "" || initial != ExecStatusQueued {
		cred.Zero()
		return execID, err
	}
	d.stashBillingCredential(execID, cred)
	return execID, nil
}

// failBillingKeyUnavailable is the fail-closed path (GH-6016). The first
// failure of an outage creates the execution row, finishes it infra-class with
// the billing_key_unavailable error, pauses admission for
// AdmissionPauseOwnerBillingKey, arms the retry timer and emits one alert.
// Every call returns an ErrBillingKeyHold so the task is left for the next
// poll; none of them starts a backend or falls back to the subscription.
//
// Decision: the row is finished ExecStatusInfra, not failed (GH-6021). A
// failed row makes the next pickup count a repick drop and grow the backoff
// window, and two outages with identical text trip the identical-failure
// streak and stall the task, though it never ran. The infra class has its own
// capped counter and no streak check. The error text carries no env var names,
// so the GH-5211 env-class signatures never match it either.
func (d *Dispatcher) failBillingKeyUnavailable(task *Task, keyErr error) (string, error) {
	if d.armBillingKeyPause(task.ID, task.ProjectPath, keyErr) {
		execID, err := d.beginClaimWithGenerationRetry(task, ExecStatusRunning)
		switch {
		case err != nil:
			d.log.Warn("billing key unavailable: could not record the failed execution",
				slog.String("task_id", task.ID), slog.Any("error", err))
		case execID != "":
			d.recordExecutionEvent(execID, memory.StageFailed, keyErr.Error())
			lifecycle := NewExecutionLifecycle(d.store)
			if _, finErr := lifecycle.Finish(execID, nil, keyErr, 0, ExecStatusInfra); finErr != nil {
				d.log.Error("failed to persist billing-key failure",
					slog.String("execution_id", execID), slog.Any("error", finErr))
			}
		}
	}
	d.billingMu.Lock()
	last := d.billingLastErr
	d.billingMu.Unlock()
	if last == nil {
		last = keyErr
	}
	return "", fmt.Errorf("%w: %w", ErrBillingKeyHold, last)
}

// armBillingKeyPause holds admission for the billing-key owner and schedules
// the resolver retry. It returns true only for the call that started the
// outage; that call also logs and alerts once. Later calls just record keyErr.
func (d *Dispatcher) armBillingKeyPause(taskID, projectPath string, keyErr error) bool {
	d.billingMu.Lock()
	d.billingLastErr = keyErr
	if d.billingHeld {
		d.billingMu.Unlock()
		return false
	}
	d.billingHeld = true
	d.billingGen++
	gen := d.billingGen
	d.billingTimer = d.pauseClockOrReal().AfterFunc(BillingKeyRetryInterval, func() { d.retryBillingKey(gen) })
	d.billingMu.Unlock()

	d.PauseAdmissionFor(AdmissionPauseOwnerBillingKey)
	d.log.Warn("billing api_key unavailable, task admission paused",
		slog.String("owner", AdmissionPauseOwnerBillingKey),
		slog.String("task_id", taskID),
		slog.Duration("retry_in", BillingKeyRetryInterval),
		slog.String("error", keyErr.Error()),
	)
	if d.runner != nil && d.runner.AlertProcessor() != nil {
		d.runner.AlertProcessor().ProcessEvent(AlertEvent{
			Type:      AlertEventTypeBillingKeyUnavailable,
			TaskID:    taskID,
			Project:   projectPath,
			Error:     keyErr.Error(),
			Metadata:  map[string]string{"owner": AdmissionPauseOwnerBillingKey},
			Timestamp: d.pauseClockOrReal().Now(),
		})
	}
	return true
}

// retryBillingKey is the timer callback: it re-runs the resolver, and the
// first success resumes admission for the billing-key owner. A stale
// generation is ignored.
func (d *Dispatcher) retryBillingKey(gen uint64) {
	d.billingMu.Lock()
	if gen != d.billingGen || !d.billingHeld {
		d.billingMu.Unlock()
		return
	}
	d.billingMu.Unlock()

	cred, err := d.billingResolverOrConfig().Resolve(context.Background())

	d.billingMu.Lock()
	if gen != d.billingGen || !d.billingHeld {
		d.billingMu.Unlock()
		cred.Zero()
		return
	}
	if err != nil {
		d.billingLastErr = err
		d.billingTimer = d.pauseClockOrReal().AfterFunc(BillingKeyRetryInterval, func() { d.retryBillingKey(gen) })
		d.billingMu.Unlock()
		d.log.Debug("billing api_key still unavailable", slog.String("error", err.Error()))
		return
	}
	d.billingHeld = false
	d.billingLastErr = nil
	d.billingTimer = nil
	d.billingMu.Unlock()
	cred.Zero()

	d.ResumeAdmissionFor(AdmissionPauseOwnerBillingKey)
	d.log.Info("billing api_key resolved, task admission resumed",
		slog.String("owner", AdmissionPauseOwnerBillingKey))
}

// stashBillingCredential keeps cred for the worker that will run execID.
func (d *Dispatcher) stashBillingCredential(execID string, cred *BillingCredential) {
	if cred == nil || len(cred.Env()) == 0 {
		return
	}
	d.billingMu.Lock()
	defer d.billingMu.Unlock()
	if d.billingCreds == nil {
		d.billingCreds = make(map[string]*BillingCredential)
	}
	d.billingCreds[execID] = cred
}

// billingCredentialForRun hands the worker the credential of execID: the one
// resolved at admission, or a fresh resolution when there is none (the daemon
// restarted while the row was queued). A failed fresh resolution fails closed
// and pauses admission like the admission path does.
func (d *Dispatcher) billingCredentialForRun(execID, taskID, projectPath string) (*BillingCredential, error) {
	if !d.billingApplies() {
		return nil, nil
	}
	d.billingMu.Lock()
	cred := d.billingCreds[execID]
	delete(d.billingCreds, execID)
	d.billingMu.Unlock()
	if cred != nil {
		return cred, nil
	}
	cred, err := d.billingResolverOrConfig().Resolve(context.Background())
	if err != nil {
		d.armBillingKeyPause(taskID, projectPath, err)
		return nil, err
	}
	return cred, nil
}

// discardBillingCredential zeroes and removes the stashed credential of
// execID, for a queued row that will never reach the worker's run (GH-6021).
// A missing entry is a no-op.
func (d *Dispatcher) discardBillingCredential(execID string) {
	d.billingMu.Lock()
	cred := d.billingCreds[execID]
	delete(d.billingCreds, execID)
	d.billingMu.Unlock()
	cred.Zero()
}

// billingStashRowLive reports whether a row in this status can still reach the
// worker's key take. A running row is live: the worker moves a row to running
// before it takes the key, and a sweep tick in that gap must not zero a key
// admission already resolved (GH-6026).
func billingStashRowLive(status string) bool {
	return status == string(ExecStatusQueued) || status == string(ExecStatusRunning)
}

// sweepBillingCredentials zeroes every stashed credential whose execution row
// is missing or neither queued nor running, e.g. canceled by `pilot task
// cancel` from another process (GH-6021). A lookup error keeps the entry.
func (d *Dispatcher) sweepBillingCredentials() {
	d.billingMu.Lock()
	ids := make([]string, 0, len(d.billingCreds))
	for id := range d.billingCreds {
		ids = append(ids, id)
	}
	d.billingMu.Unlock()
	if len(ids) == 0 || d.store == nil {
		return
	}
	for _, id := range ids {
		exec, err := d.store.GetExecution(id)
		switch {
		case err == nil && exec != nil && billingStashRowLive(exec.Status):
			continue
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			continue
		}
		d.discardBillingCredential(id)
	}
}

// stopBillingState cancels the retry timer and wipes stashed keys on Stop.
func (d *Dispatcher) stopBillingState() {
	d.billingMu.Lock()
	defer d.billingMu.Unlock()
	if d.billingTimer != nil {
		d.billingTimer.Stop()
		d.billingTimer = nil
	}
	for id, c := range d.billingCreds {
		c.Zero()
		delete(d.billingCreds, id)
	}
}
