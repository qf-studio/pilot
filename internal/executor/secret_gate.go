package executor

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
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
	err := secretpatterns.CheckPush(ctx, dir, head)
	if err == nil {
		return nil
	}
	var pre *secretpatterns.PushRangeError
	if !errors.As(err, &pre) {
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
