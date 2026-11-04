package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

	d.applyUsageLimit("GH-6069", "/p", fallbackLimitMsg)

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

	d.applyUsageLimit("GH-6069", "/p", fallbackLimitMsg)

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

			d.applyUsageLimit("GH-6069", "/p", tt.msg)

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

	// A genuine API-side limit: the mode in force is api_key by an operator
	// override, not the fallback.
	if err := d.SetBillingOverride("api_key", time.Time{}, BillingModeSourceCLI); err != nil {
		t.Fatalf("SetBillingOverride: %v", err)
	}
	before := d.BillingOverride()

	d.applyUsageLimit("GH-2", "/p", "You've hit your limit · resets 3pm (UTC)")

	st := d.AdmissionStatus()
	if !st.Paused || len(st.Owners) != 1 || st.Owners[0] != AdmissionPauseOwnerUsageLimit {
		t.Fatalf("status = %+v, want paused by exactly [%s]", st, AdmissionPauseOwnerUsageLimit)
	}
	if after := d.BillingOverride(); after != before || after.Source != BillingModeSourceCLI {
		t.Errorf("override = %+v, want untouched %+v", after, before)
	}
	if n := len(sink.modeChanges()); n != 1 {
		t.Errorf("%d billing_mode_changed alerts, want 1 (the operator's)", n)
	}
	if n := len(sink.eventsOf(AlertEventTypeUsageLimitPause)); n != 1 {
		t.Errorf("%d usage_limit_pause alerts, want 1", n)
	}
}

func TestUsageLimitFallback_SecondLimitWhileFallbackServesIsQuiet(t *testing.T) {
	d, sink, _, _ := newFallbackDispatcher(t, overrideTestConfig(t), &UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey})
	d.applyUsageLimit("GH-1", "/p", fallbackLimitMsg)
	before := d.BillingOverride()

	d.applyUsageLimit("GH-2", "/p", "You've hit your limit · resets 3pm (UTC)")

	if d.IsAdmissionPaused() {
		t.Errorf("admission paused: %+v", d.AdmissionStatus())
	}
	if after := d.BillingOverride(); after != before {
		t.Errorf("override = %+v, want untouched %+v", after, before)
	}
	if n := len(sink.eventsOf(AlertEventTypeUsageLimitPause)) + len(sink.eventsOf(AlertEventTypeBillingFallbackUnavailable)); n != 0 {
		t.Errorf("%d pause/unavailable alerts, want 0", n)
	}
}

func TestUsageLimitFallback_KeyUnavailablePausesAndAlerts(t *testing.T) {
	skipWithoutShell(t)
	billing := &BillingConfig{Mode: BillingModeSubscription, APIKeyCommand: "exit 1"}
	d, sink, _, store := newFallbackDispatcher(t, billing, &UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey})

	d.applyUsageLimit("GH-1", "/p", fallbackLimitMsg)
	d.applyUsageLimit("GH-2", "/p", "You've hit your limit · resets 3pm (UTC)")

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

		d.applyUsageLimit("GH-6069", "/p", fallbackLimitMsg)

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

// Mutation pin (GH-6073): pause when the fallback is already serving -> the
// losing worker pauses admission and alerts. Mutation pin (GH-6071): drop the
// usageLimitFallbackMu section -> two alerts and a replaced until.
func TestUsageLimitFallback_LosingWorkerDoesNotPause(t *testing.T) {
	skipWithoutShell(t)
	// Both commands wait for the other, so both workers have passed the
	// subscription check before either installs.
	dir := t.TempDir()
	cmd := `touch "` + dir + `/$$"; while [ "$(ls "` + dir + `" | wc -l)" -lt 2 ]; do sleep 0.05; done; echo ` + testBillingKey
	billing := &BillingConfig{Mode: BillingModeSubscription, APIKeyCommand: cmd}
	d, sink, _, _ := newFallbackDispatcher(t, billing, &UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey})

	var wg sync.WaitGroup
	for i, msg := range []string{fallbackLimitMsg, "You've hit your limit · resets 3pm (UTC)"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.armUsageLimitPause(fmt.Sprintf("GH-%d", i), "/p", msg)
		}()
	}
	wg.Wait()

	changes := sink.modeChanges()
	if len(changes) != 1 {
		t.Fatalf("%d billing_mode_changed alerts, want 1", len(changes))
	}
	got := d.BillingOverride()
	if got.Mode != "api_key" || got.Source != BillingModeSourceFallback || got.Until.Format(time.RFC3339) != changes[0].Metadata["until"] {
		t.Errorf("override = %+v, want the first install's until %s", got, changes[0].Metadata["until"])
	}
	if d.IsAdmissionPaused() {
		t.Errorf("admission paused: %+v", d.AdmissionStatus())
	}
	if n := len(sink.eventsOf(AlertEventTypeUsageLimitPause)); n != 0 {
		t.Errorf("%d usage_limit_pause alerts, want 0", n)
	}
}

// Mutation pin (GH-6073): skip the mode re-check on the key-failed path -> A
// pauses admission after B installed the override.
func TestUsageLimitFallback_KeyFailAfterPeerInstallDoesNotPause(t *testing.T) {
	skipWithoutShell(t)
	// The first invocation (worker A) is slow and fails; later ones (worker B)
	// succeed at once, so B installs before A's failure is known.
	dir := t.TempDir()
	cmd := `if mkdir "` + dir + `/first" 2>/dev/null; then sleep 1; exit 1; fi; echo ` + testBillingKey
	billing := &BillingConfig{Mode: BillingModeSubscription, APIKeyCommand: cmd}
	d, sink, _, _ := newFallbackDispatcher(t, billing, &UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.armUsageLimitPause("GH-A", "/p", fallbackLimitMsg)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "first")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker A never started its key command")
		}
		time.Sleep(10 * time.Millisecond)
	}
	d.armUsageLimitPause("GH-B", "/p", "You've hit your limit · resets 3pm (UTC)")
	if got := d.BillingOverride(); got.Mode != "api_key" || got.Source != BillingModeSourceFallback {
		t.Fatalf("setup: override after B = %+v, want the fallback", got)
	}
	wg.Wait()

	if d.IsAdmissionPaused() {
		t.Errorf("admission paused: %+v", d.AdmissionStatus())
	}
	if got := d.BillingOverride(); got.Mode != "api_key" || got.Source != BillingModeSourceFallback {
		t.Errorf("override = %+v, want the fallback still in force", got)
	}
	if n := len(sink.eventsOf(AlertEventTypeBillingFallbackUnavailable)); n != 1 {
		t.Errorf("%d billing_fallback_unavailable alerts, want 1", n)
	}
	if n := len(sink.eventsOf(AlertEventTypeUsageLimitPause)); n != 0 {
		t.Errorf("%d usage_limit_pause alerts, want 0", n)
	}
	if n := len(sink.modeChanges()); n != 1 {
		t.Errorf("%d billing_mode_changed alerts, want 1", n)
	}
}

// Mutation pin for item 2 (GH-6071): revert releaseUsageLimitPauseForFallback
// -> admission stays held by the older pause.
func TestUsageLimitFallback_ReleasesOlderPauseWhenFallbackSucceeds(t *testing.T) {
	d, _, _, _ := newFallbackDispatcher(t, overrideTestConfig(t), &UsageLimitPauseConfig{Fallback: UsageLimitFallbackPause})
	d.applyUsageLimit("GH-1", "/p", fallbackLimitMsg)
	if !d.IsAdmissionPaused() {
		t.Fatal("setup: the first episode did not pause admission")
	}

	d.runner.config.UsageLimitPause = &UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey}
	d.applyUsageLimit("GH-2", "/p", fallbackLimitMsg)

	if got := d.BillingOverride(); got.Mode != "api_key" || got.Source != BillingModeSourceFallback {
		t.Fatalf("override = %+v, want the api_key fallback", got)
	}
	if d.IsAdmissionPaused() {
		t.Errorf("admission still held by the older pause: %+v", d.AdmissionStatus())
	}
}

// Mutation pin (GH-6073): run the episode on a goroutine in
// armUsageLimitPause -> admission is neither paused nor on the override when it
// returns, so the worker's next pick is admitted on the subscription.
func TestUsageLimitFallback_GatesAdmissionBeforeNextPick(t *testing.T) {
	skipWithoutShell(t)

	t.Run("key resolves", func(t *testing.T) {
		billing := &BillingConfig{Mode: BillingModeSubscription, APIKeyCommand: "sleep 1; echo " + testBillingKey}
		d, _, _, _ := newFallbackDispatcher(t, billing, &UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey})

		d.armUsageLimitPause("GH-6073", "/p", fallbackLimitMsg)

		if got := d.BillingOverride(); got.Mode != "api_key" || got.Source != BillingModeSourceFallback {
			t.Errorf("override at return = %+v, want api_key / fallback", got)
		}
		if d.IsAdmissionPaused() {
			t.Errorf("admission paused at return: %+v", d.AdmissionStatus())
		}
	})

	t.Run("key fails", func(t *testing.T) {
		billing := &BillingConfig{Mode: BillingModeSubscription, APIKeyCommand: "sleep 1; exit 1"}
		d, _, _, _ := newFallbackDispatcher(t, billing, &UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey})

		d.armUsageLimitPause("GH-6073", "/p", fallbackLimitMsg)

		st := d.AdmissionStatus()
		if !st.Paused || len(st.Owners) != 1 || st.Owners[0] != AdmissionPauseOwnerUsageLimit {
			t.Errorf("status at return = %+v, want paused by exactly [%s]", st, AdmissionPauseOwnerUsageLimit)
		}
	})
}

// Mutation pin (GH-6073): make usageLimitFallbackMu a package variable ->
// dispatcher B waits for dispatcher A's lock.
func TestUsageLimitFallback_MutexIsPerDispatcher(t *testing.T) {
	a, _, _, _ := newFallbackDispatcher(t, overrideTestConfig(t), &UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey})
	b, _, _, _ := newFallbackDispatcher(t, overrideTestConfig(t), &UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey})

	a.usageLimitFallbackMu.Lock()
	released := false
	defer func() {
		if !released {
			a.usageLimitFallbackMu.Unlock()
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		b.armUsageLimitPause("GH-6073", "/p", fallbackLimitMsg)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		a.usageLimitFallbackMu.Unlock()
		released = true
		<-done
		t.Fatal("dispatcher B's episode waited for dispatcher A's mutex")
	}
	if got := b.BillingOverride(); got.Mode != "api_key" || got.Source != BillingModeSourceFallback {
		t.Errorf("override on B = %+v, want the fallback", got)
	}
}

// Mutation pin for item 4 (GH-6071): add reset_at only when a reset parsed ->
// the key is missing from the payload.
func TestUsageLimitFallback_UnavailablePayloadCarriesEmptyResetAt(t *testing.T) {
	billing := &BillingConfig{Mode: BillingModeSubscription, APIKeyEnv: "PILOT_TEST_UNSET_BILLING_KEY"}
	d, sink, _, _ := newFallbackDispatcher(t, billing, &UsageLimitPauseConfig{Fallback: UsageLimitFallbackAPIKey})

	d.applyUsageLimit("GH-6069", "/p", "usage limit reached")

	unavail := sink.eventsOf(AlertEventTypeBillingFallbackUnavailable)
	if len(unavail) != 1 {
		t.Fatalf("%d billing_fallback_unavailable alerts, want 1", len(unavail))
	}
	if v, ok := unavail[0].Metadata["reset_at"]; !ok || v != "" {
		t.Errorf("reset_at = %q, present=%v, want present and empty", v, ok)
	}
}
