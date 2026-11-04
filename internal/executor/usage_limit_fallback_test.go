package executor

import (
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// newFallbackDispatcher starts a dispatcher with the given billing config and
// usage_limit_pause.fallback value on a fake clock.
func newFallbackDispatcher(t *testing.T, billing *BillingConfig, pause *UsageLimitPauseConfig) (*Dispatcher, *billingAlertSink, *fakePauseClock, *memory.Store) {
	t.Helper()
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	clock := newFakePauseClock(usageLimitTestNow)
	d, sink := newOverrideDispatcher(t, store, billing, clock)
	d.runner.config.UsageLimitPause = pause
	return d, sink, clock, store
}

func (s *billingAlertSink) eventsOf(typ AlertEventType) []AlertEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AlertEvent
	for _, e := range s.events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

const fallbackLimitMsg = "You've hit your limit · resets 2pm (UTC)"

func TestUsageLimitFallback_SwitchesToAPIKeyWithoutPause(t *testing.T) {
	d, sink, _, store := newFallbackDispatcher(t, overrideTestConfig(t), &UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey})

	d.armUsageLimitPause("GH-6069", "/p", fallbackLimitMsg)

	if d.IsAdmissionPaused() {
		t.Fatalf("admission paused: %+v", d.AdmissionStatus())
	}
	wantUntil := time.Date(2026, 10, 26, 14, 5, 0, 0, time.UTC) // reset + RetryBuffer
	got := d.BillingOverride()
	if got.Mode != "api_key" || got.Source != BillingModeSourceFallback || !got.Until.Equal(wantUntil) {
		t.Fatalf("override = %+v, want api_key / fallback / %v", got, wantUntil)
	}
	if _, _, _, _, found, _ := store.GetRuntimeOverride(memory.RuntimeOverrideBillingMode); !found {
		t.Error("override row was not persisted")
	}
	cred, err := d.admitBilling(&Task{ID: "GH-next"})
	if err != nil || cred.Mode() != BillingModeAPIKey {
		t.Fatalf("admission = %v, %v, want api_key", cred, err)
	}
	if env := cred.Env(); len(env) != 1 || env[0] != billingKeyEnv+"="+testBillingKey {
		t.Errorf("admission env = %v, want the API key", env)
	}
	cred.Zero()

	changes := sink.modeChanges()
	if len(changes) != 1 || changes[0].Metadata["source"] != BillingModeSourceFallback || changes[0].Metadata["to"] != "api_key" {
		t.Fatalf("billing_mode_changed = %+v, want one with source fallback", changes)
	}
	if n := len(sink.eventsOf(AlertEventTypeUsageLimitPause)); n != 0 {
		t.Errorf("%d usage_limit_pause alerts, want 0", n)
	}
	if n := len(sink.eventsOf(AlertEventTypeBillingFallbackUnavailable)); n != 0 {
		t.Errorf("%d billing_fallback_unavailable alerts, want 0", n)
	}
}

func TestUsageLimitFallback_RevertsAtUntil(t *testing.T) {
	d, sink, clock, store := newFallbackDispatcher(t, overrideTestConfig(t), &UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey})

	d.armUsageLimitPause("GH-6069", "/p", fallbackLimitMsg)

	clock.Advance(4*time.Hour + 4*time.Minute + 59*time.Second)
	if got := d.BillingOverride(); got.Mode != "api_key" {
		t.Fatalf("mode one tick before until = %s, want api_key", got.Mode)
	}
	clock.Advance(time.Second)
	got := d.BillingOverride()
	if got.Mode != "subscription" || got.Source != BillingModeSourceConfig || !got.Until.IsZero() {
		t.Fatalf("status at until = %+v, want the config's subscription", got)
	}
	if _, _, _, _, found, _ := store.GetRuntimeOverride(memory.RuntimeOverrideBillingMode); found {
		t.Error("the row survived the expiry")
	}
	cred, err := d.admitBilling(&Task{ID: "GH-after"})
	if err != nil || cred.Mode() != BillingModeSubscription {
		t.Errorf("admission after the revert = %v, %v, want subscription", cred, err)
	}
	changes := sink.modeChanges()
	if len(changes) != 2 {
		t.Fatalf("%d billing_mode_changed alerts, want 2 (fallback, expiry)", len(changes))
	}
	last := changes[1].Metadata
	if last["from"] != "api_key" || last["to"] != "subscription" || last["set_by"] != "expiry" {
		t.Errorf("expiry alert payload = %v", last)
	}
	if d.IsAdmissionPaused() {
		t.Error("admission paused after the revert")
	}
}

func TestUsageLimitFallback_CapAndAlreadyReset(t *testing.T) {
	tests := []struct {
		name  string
		pause UsageLimitPauseConfig
		msg   string
		want  time.Time
	}{
		{
			name:  "reset beyond max_pause is capped",
			pause: UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey, MaxPause: 2 * time.Hour},
			msg:   "You've hit your limit · resets 7am (UTC)", // tomorrow 07:00, 21h away
			want:  usageLimitTestNow.Add(2 * time.Hour),
		},
		{
			name:  "reset in the past resumes after the retry buffer",
			pause: UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey},
			msg:   "You've hit your limit · resets 9am (UTC)",
			want:  usageLimitTestNow.Add(DefaultSchedulerConfig().RetryBuffer),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pause := tt.pause
			d, _, _, _ := newFallbackDispatcher(t, overrideTestConfig(t), &pause)

			d.armUsageLimitPause("GH-6069", "/p", tt.msg)

			got := d.BillingOverride()
			if got.Mode != "api_key" || got.Source != BillingModeSourceFallback || !got.Until.Equal(tt.want) {
				t.Fatalf("override = %+v, want api_key / fallback / %v", got, tt.want)
			}
			if d.IsAdmissionPaused() {
				t.Error("admission paused")
			}
		})
	}
}

func TestUsageLimitFallback_SecondLimitWhileOnFallbackPausesAsToday(t *testing.T) {
	d, sink, _, _ := newFallbackDispatcher(t, overrideTestConfig(t), &UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey})

	d.armUsageLimitPause("GH-1", "/p", fallbackLimitMsg)
	before := d.BillingOverride()

	d.armUsageLimitPause("GH-2", "/p", "You've hit your limit · resets 3pm (UTC)")

	st := d.AdmissionStatus()
	if !st.Paused || len(st.Owners) != 1 || st.Owners[0] != AdmissionPauseOwnerUsageLimit {
		t.Fatalf("status = %+v, want paused by exactly [%s]", st, AdmissionPauseOwnerUsageLimit)
	}
	if after := d.BillingOverride(); after != before {
		t.Errorf("override = %+v, want untouched %+v", after, before)
	}
	if n := len(sink.modeChanges()); n != 1 {
		t.Errorf("%d billing_mode_changed alerts, want 1 (the first fallback)", n)
	}
	if n := len(sink.eventsOf(AlertEventTypeUsageLimitPause)); n != 1 {
		t.Errorf("%d usage_limit_pause alerts, want 1", n)
	}
}

func TestUsageLimitFallback_KeyUnavailablePausesAndAlerts(t *testing.T) {
	skipWithoutShell(t)
	billing := &BillingConfig{Mode: BillingModeSubscription, APIKeyCommand: "exit 1"}
	d, sink, _, store := newFallbackDispatcher(t, billing, &UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey})

	d.armUsageLimitPause("GH-1", "/p", fallbackLimitMsg)
	d.armUsageLimitPause("GH-2", "/p", "You've hit your limit · resets 3pm (UTC)")

	st := d.AdmissionStatus()
	wantUntil := time.Date(2026, 10, 26, 14, 5, 0, 0, time.UTC)
	if !st.Paused || len(st.Owners) != 1 || st.Owners[0] != AdmissionPauseOwnerUsageLimit {
		t.Fatalf("status = %+v, want paused by exactly [%s]", st, AdmissionPauseOwnerUsageLimit)
	}
	if got := d.BillingOverride(); got.Source != BillingModeSourceConfig || got.Mode != "subscription" {
		t.Errorf("override = %+v, want none", got)
	}
	if _, _, _, _, found, _ := store.GetRuntimeOverride(memory.RuntimeOverrideBillingMode); found {
		t.Error("an override row was written")
	}
	if n := len(sink.eventsOf(AlertEventTypeUsageLimitPause)); n != 1 {
		t.Errorf("%d usage_limit_pause alerts, want 1", n)
	}
	unavail := sink.eventsOf(AlertEventTypeBillingFallbackUnavailable)
	if len(unavail) != 1 {
		t.Fatalf("%d billing_fallback_unavailable alerts, want 1 per episode", len(unavail))
	}
	md := unavail[0].Metadata
	if md["reset_at"] != time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC).Format(time.RFC3339) ||
		md["until"] != wantUntil.Format(time.RFC3339) || md["error"] == "" {
		t.Errorf("payload = %v, want reset_at 14:00Z, until %s and an error", md, wantUntil.Format(time.RFC3339))
	}
	if n := len(sink.modeChanges()); n != 0 {
		t.Errorf("%d billing_mode_changed alerts, want 0", n)
	}
}

func TestUsageLimitFallback_OffByDefault(t *testing.T) {
	for _, pause := range []*UsageLimitPauseConfig{nil, {}, {Fallback: UsageLimitFallbackPause}} {
		d, sink, _, _ := newFallbackDispatcher(t, overrideTestConfig(t), pause)

		d.armUsageLimitPause("GH-6069", "/p", fallbackLimitMsg)

		if !d.IsAdmissionPaused() {
			t.Errorf("pause %+v: admission not paused", pause)
		}
		if got := d.BillingOverride(); got.Source != BillingModeSourceConfig {
			t.Errorf("pause %+v: override = %+v, want none", pause, got)
		}
		if n := len(sink.modeChanges()) + len(sink.eventsOf(AlertEventTypeBillingFallbackUnavailable)); n != 0 {
			t.Errorf("pause %+v: %d billing alerts, want 0", pause, n)
		}
	}
}
