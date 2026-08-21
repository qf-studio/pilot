package alerts

import "github.com/qf-studio/pilot/internal/memory"

// ActiveAlertStore persists currently-firing alerts across restarts so a
// condition that recovers while the daemon is down still emits its
// resolution once the daemon restarts (GH-4890, follow-up to #4886's
// resolution-notifications work). Optional: an Engine constructed without
// one (WithActiveAlertStore never called) behaves exactly as before —
// active-alert state lives only in the in-memory activeAlerts map.
//
// *memory.Store satisfies this interface directly — same optional-store
// shape as approval.PendingApprovalStore's pending-approval precedent.
type ActiveAlertStore interface {
	UpsertActiveAlert(a *memory.ActiveAlert) error
	DeleteActiveAlert(ruleName, source string) error
	LoadActiveAlerts() ([]*memory.ActiveAlert, error)
}

// Compile-time assertion that *memory.Store satisfies ActiveAlertStore.
var _ ActiveAlertStore = (*memory.Store)(nil)
