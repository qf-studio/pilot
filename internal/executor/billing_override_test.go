package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

const billingOverrideKeyEnv = "PILOT_TEST_OVERRIDE_KEY"

// overrideTestConfig is a subscription config with an env key source, so
// api_key can be switched to at runtime.
func overrideTestConfig(t *testing.T) *BillingConfig {
	t.Helper()
	t.Setenv(billingOverrideKeyEnv, testBillingKey)
	return &BillingConfig{Mode: BillingModeSubscription, APIKeyEnv: billingOverrideKeyEnv}
}

// newOverrideDispatcher starts a dispatcher on store with the given config and
// clock. The process billing mode is restored when the test ends.
func newOverrideDispatcher(t *testing.T, store *memory.Store, billing *BillingConfig, clock pauseClock) (*Dispatcher, *billingAlertSink) {
	t.Helper()
	setProcessModeForTest(t, billing.EffectiveMode())
	runner := NewRunnerWithBackend(&countingBackend{})
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{Type: BackendTypeClaudeCode, SkipSelfReview: true, ClaudeCode: &ClaudeCodeConfig{Billing: billing}}
	sink := &billingAlertSink{}
	runner.SetAlertProcessor(sink)

	d := NewDispatcher(store, runner, nil)
	d.clock = clock
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(d.Stop)
	return d, sink
}

func (s *billingAlertSink) modeChanges() []AlertEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AlertEvent
	for _, e := range s.events {
		if e.Type == AlertEventTypeBillingModeChanged {
			out = append(out, e)
		}
	}
	return out
}

func TestBillingOverride_SetAppliesToNextAdmissionOnly(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	cfg := &BillingConfig{Mode: BillingModeAPIKey, APIKeyEnv: billingOverrideKeyEnv}
	t.Setenv(billingOverrideKeyEnv, testBillingKey)
	d, _ := newOverrideDispatcher(t, store, cfg, newFakePauseClock(usageLimitTestNow))

	// A task admitted before the switch: credential resolved and stashed for its worker.
	before := &Task{ID: "GH-6052-a", ProjectPath: "/tmp/pilot-gh6052-a"}
	frozen, err := d.admitBilling(before)
	if err != nil {
		t.Fatalf("admitBilling before the switch: %v", err)
	}
	if frozen.Mode() != BillingModeAPIKey || before.billingMode != "api_key" {
		t.Fatalf("pre-switch credential = %s / row mode %q, want api_key", frozen.Mode(), before.billingMode)
	}
	d.stashBillingCredential("exec-before", frozen)

	if err := d.SetBillingOverride("subscription", time.Time{}, "cli"); err != nil {
		t.Fatalf("SetBillingOverride: %v", err)
	}

	// The frozen credential is untouched, in hand and in the stash.
	if got := frozen.Env(); len(got) != 1 || got[0] != "ANTHROPIC_API_KEY="+testBillingKey {
		t.Errorf("frozen credential env after the switch = %v, want the key it was admitted with", got)
	}
	if frozen.Mode() != BillingModeAPIKey {
		t.Errorf("frozen credential mode after the switch = %s, want api_key", frozen.Mode())
	}
	stashed, err := d.billingCredentialForRun("exec-before", before.ID, before.ProjectPath)
	if err != nil {
		t.Fatalf("billingCredentialForRun: %v", err)
	}
	if got := stashed.Env(); len(got) != 1 || got[0] != "ANTHROPIC_API_KEY="+testBillingKey {
		t.Errorf("stashed credential env after the switch = %v, want the original key", got)
	}

	// The next admission resolves the override.
	after := &Task{ID: "GH-6052-b", ProjectPath: "/tmp/pilot-gh6052-a"}
	next, err := d.admitBilling(after)
	if err != nil {
		t.Fatalf("admitBilling after the switch: %v", err)
	}
	if next.Mode() != BillingModeSubscription || len(next.Env()) != 0 || after.billingMode != "subscription" {
		t.Errorf("post-switch credential = %s env %v row mode %q, want subscription with no key",
			next.Mode(), next.Env(), after.billingMode)
	}

	// Clearing hands the decision back to the config.
	if err := d.ClearBillingOverride("cli"); err != nil {
		t.Fatalf("ClearBillingOverride: %v", err)
	}
	again, err := d.admitBilling(&Task{ID: "GH-6052-c", ProjectPath: "/tmp/pilot-gh6052-a"})
	if err != nil || again.Mode() != BillingModeAPIKey {
		t.Errorf("admission after clear = %v, %v, want api_key from the config", again, err)
	}
}

func TestBillingOverride_SurvivesRestart(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	cfg := overrideTestConfig(t)
	clock := newFakePauseClock(usageLimitTestNow)
	until := usageLimitTestNow.Add(2 * time.Hour)

	d1, _ := newOverrideDispatcher(t, store, cfg, clock)
	if err := d1.SetBillingOverride("api_key", until, "gateway"); err != nil {
		t.Fatalf("SetBillingOverride: %v", err)
	}
	d1.Stop()

	d2, _ := newOverrideDispatcher(t, store, cfg, clock)
	got := d2.BillingOverride()
	if got.Mode != "api_key" || got.Source != BillingModeSourceGateway || !got.Until.Equal(until) || got.ConfigMode != "subscription" {
		t.Fatalf("restored status = %+v, want api_key / gateway / %s / config subscription", got, until)
	}
	if m := processBillingMode(); m != BillingModeAPIKey {
		t.Errorf("process billing mode after boot = %s, want api_key", m)
	}
	cred, err := d2.admitBilling(&Task{ID: "GH-6052-r"})
	if err != nil || cred.Mode() != BillingModeAPIKey {
		t.Errorf("admission after restart = %v, %v, want api_key", cred, err)
	}

	// The restored override still expires on the remaining time.
	clock.Advance(2 * time.Hour)
	if got := d2.BillingOverride(); got.Source != BillingModeSourceConfig || got.Mode != "subscription" {
		t.Errorf("status after the restored expiry = %+v, want config / subscription", got)
	}
}

func TestBillingOverride_BootDropsExpiredRow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	cfg := overrideTestConfig(t)
	if err := store.SetRuntimeOverride(memory.RuntimeOverrideBillingMode, "api_key", usageLimitTestNow.Add(-time.Minute), "cli"); err != nil {
		t.Fatal(err)
	}

	d, sink := newOverrideDispatcher(t, store, cfg, newFakePauseClock(usageLimitTestNow))
	if got := d.BillingOverride(); got.Source != BillingModeSourceConfig || got.Mode != "subscription" {
		t.Errorf("status = %+v, want the config's subscription", got)
	}
	if _, _, _, _, found, _ := store.GetRuntimeOverride(memory.RuntimeOverrideBillingMode); found {
		t.Error("the expired row was not deleted at boot")
	}
	if n := len(sink.modeChanges()); n != 0 {
		t.Errorf("%d billing_mode_changed alerts at boot, want 0", n)
	}
}

func TestBillingOverride_ExpiryRevertsToConfig(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	cfg := overrideTestConfig(t)
	clock := newFakePauseClock(usageLimitTestNow)
	d, sink := newOverrideDispatcher(t, store, cfg, clock)

	if err := d.SetBillingOverride("api_key", usageLimitTestNow.Add(time.Hour), "cli"); err != nil {
		t.Fatalf("SetBillingOverride: %v", err)
	}
	clock.Advance(59 * time.Minute)
	if got := d.BillingOverride(); got.Mode != "api_key" {
		t.Fatalf("mode before the until = %s, want api_key", got.Mode)
	}

	clock.Advance(time.Minute)
	got := d.BillingOverride()
	if got.Mode != "subscription" || got.Source != BillingModeSourceConfig || !got.Until.IsZero() {
		t.Errorf("status after the until = %+v, want the config's subscription", got)
	}
	if _, _, _, _, found, _ := store.GetRuntimeOverride(memory.RuntimeOverrideBillingMode); found {
		t.Error("the row survived the expiry")
	}
	if m := processBillingMode(); m != BillingModeSubscription {
		t.Errorf("process billing mode after the expiry = %s, want subscription", m)
	}
	cred, err := d.admitBilling(&Task{ID: "GH-6052-e"})
	if err != nil || cred.Mode() != BillingModeSubscription {
		t.Errorf("admission after the expiry = %v, %v, want subscription", cred, err)
	}

	changes := sink.modeChanges()
	if len(changes) != 2 {
		t.Fatalf("%d billing_mode_changed alerts, want 2 (set, expiry)", len(changes))
	}
	last := changes[1].Metadata
	if last["from"] != "api_key" || last["to"] != "subscription" || last["set_by"] != "expiry" || last["source"] != BillingModeSourceConfig {
		t.Errorf("expiry alert payload = %v", last)
	}
}

func TestBillingOverride_LaterSetReplacesTimer(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	clock := newFakePauseClock(usageLimitTestNow)
	d, _ := newOverrideDispatcher(t, store, overrideTestConfig(t), clock)

	if err := d.SetBillingOverride("api_key", usageLimitTestNow.Add(time.Hour), "cli"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetBillingOverride("api_key", usageLimitTestNow.Add(3*time.Hour), "cli"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	if got := d.BillingOverride(); got.Mode != "api_key" {
		t.Fatalf("the first timer reverted the replaced override: %+v", got)
	}
	clock.Advance(2 * time.Hour)
	if got := d.BillingOverride(); got.Source != BillingModeSourceConfig {
		t.Errorf("status after the second until = %+v, want config", got)
	}

	// No-until stays until cleared.
	if err := d.SetBillingOverride("api_key", time.Time{}, "cli"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(1000 * time.Hour)
	if got := d.BillingOverride(); got.Mode != "api_key" || !got.Until.IsZero() {
		t.Errorf("an override without until changed by itself: %+v", got)
	}
}

func TestBillingOverride_SwitchPublishesProcessMode(t *testing.T) {
	envFile := fakeEnvRecordingCLI(t)
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	d, _ := newOverrideDispatcher(t, store, nil, newFakePauseClock(usageLimitTestNow))

	driveResolveComplexity(t, "GH-6052-unset")
	if got, ok := billingEnvValue(readRecordedEnv(t, envFile), "ANTHROPIC_API_KEY"); !ok || got != testAmbientAPIKey {
		t.Fatalf("ANTHROPIC_API_KEY = %q (present=%v) in the unset config mode, want the inherited key", got, ok)
	}

	if err := d.SetBillingOverride("subscription", time.Time{}, "cli"); err != nil {
		t.Fatalf("SetBillingOverride: %v", err)
	}
	// A resolver built later (chat, CLI, reviewer) must not undo the override.
	NewBillingResolver(nil)
	if m := processBillingMode(); m != BillingModeSubscription {
		t.Fatalf("process billing mode after Set = %s, want subscription", m)
	}
	driveResolveComplexity(t, "GH-6052-sub")
	env := readRecordedEnv(t, envFile)
	for _, banned := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if v, ok := billingEnvValue(env, banned); ok {
			t.Errorf("%s=%q reached the bare-ctx classifier after the switch to subscription", banned, v)
		}
	}

	if err := d.ClearBillingOverride("cli"); err != nil {
		t.Fatalf("ClearBillingOverride: %v", err)
	}
	driveResolveComplexity(t, "GH-6052-cleared")
	if got, ok := billingEnvValue(readRecordedEnv(t, envFile), "ANTHROPIC_API_KEY"); !ok || got != testAmbientAPIKey {
		t.Errorf("ANTHROPIC_API_KEY = %q (present=%v) after clear, want the inherited key again", got, ok)
	}
}

func TestBillingOverride_ClearResumesBillingKeyPause(t *testing.T) {
	skipWithoutShell(t)
	for _, tc := range []struct {
		name  string
		leave func(d *Dispatcher) error
	}{
		{"clear", func(d *Dispatcher) error { return d.ClearBillingOverride("cli") }},
		{"set subscription", func(d *Dispatcher) error { return d.SetBillingOverride("subscription", time.Time{}, "cli") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			t.Cleanup(cleanup)
			clock := newFakePauseClock(usageLimitTestNow)
			cfg := &BillingConfig{Mode: BillingModeSubscription, APIKeyCommand: "exit 1"}
			d, _ := newOverrideDispatcher(t, store, cfg, clock)
			const proj = "/tmp/pilot-gh6052-pause"

			if err := d.SetBillingOverride("api_key", time.Time{}, "cli"); err != nil {
				t.Fatalf("SetBillingOverride: %v", err)
			}
			if _, err := d.QueueTask(context.Background(), &Task{ID: "GH-6052-p1", Title: "needs key", ProjectPath: proj}); !errors.Is(err, ErrBillingKeyHold) {
				t.Fatalf("QueueTask err = %v, want ErrBillingKeyHold", err)
			}
			if st := d.AdmissionStatus(); !st.Paused || len(st.Owners) != 1 || st.Owners[0] != AdmissionPauseOwnerBillingKey {
				t.Fatalf("admission = %+v, want paused by the billing-key owner", st)
			}

			if err := tc.leave(d); err != nil {
				t.Fatalf("leaving api_key: %v", err)
			}
			if d.IsAdmissionPaused() {
				t.Fatalf("admission still paused after leaving api_key: %+v", d.AdmissionStatus())
			}
			// The stale retry tick does nothing, and the next task is admitted.
			clock.Advance(BillingKeyRetryInterval)
			if d.IsAdmissionPaused() {
				t.Error("the stale retry timer re-paused admission")
			}
			execID, err := d.QueueTask(context.Background(), &Task{ID: "GH-6052-p2", Title: "ok now", ProjectPath: proj})
			if err != nil || execID == "" {
				t.Errorf("QueueTask after leaving api_key = %q, %v, want admitted", execID, err)
			}
		})
	}
}

func TestBillingOverride_RejectsInvalidRequests(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	clock := newFakePauseClock(usageLimitTestNow)
	noKey := &BillingConfig{Mode: BillingModeSubscription}
	d, sink := newOverrideDispatcher(t, store, noKey, clock)

	for name, call := range map[string]func() error{
		"unknown mode":      func() error { return d.SetBillingOverride("unset", time.Time{}, "cli") },
		"config as a mode":  func() error { return d.SetBillingOverride("config", time.Time{}, "cli") },
		"until in the past": func() error { return d.SetBillingOverride("subscription", usageLimitTestNow, "cli") },
		"api_key, no key":   func() error { return d.SetBillingOverride("api_key", time.Time{}, "cli") },
	} {
		if err := call(); !errors.Is(err, ErrBillingOverrideInvalid) {
			t.Errorf("%s: err = %v, want ErrBillingOverrideInvalid", name, err)
		}
	}
	if got := d.BillingOverride(); got.Source != BillingModeSourceConfig {
		t.Errorf("a rejected request changed the status: %+v", got)
	}
	if _, _, _, _, found, _ := store.GetRuntimeOverride(memory.RuntimeOverrideBillingMode); found {
		t.Error("a rejected request wrote a row")
	}
	if n := len(sink.modeChanges()); n != 0 {
		t.Errorf("%d alerts for rejected requests, want 0", n)
	}
	// Clearing with nothing set is a quiet no-op.
	if err := d.ClearBillingOverride("cli"); err != nil || len(sink.modeChanges()) != 0 {
		t.Errorf("clear with no override: err %v, %d alerts", err, len(sink.modeChanges()))
	}
}

// GH-6053: the observer hears every switch (set, clear, expiry) with the mode
// then in force, and nothing for a clear that had nothing to clear.
func TestBillingOverride_ObserverHearsEverySwitch(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	clock := newFakePauseClock(usageLimitTestNow)
	d, _ := newOverrideDispatcher(t, store, overrideTestConfig(t), clock)

	var got []BillingOverrideStatus
	d.SetBillingObserver(func(st BillingOverrideStatus) { got = append(got, st) })

	if err := d.ClearBillingOverride("cli"); err != nil {
		t.Fatalf("ClearBillingOverride with nothing to clear: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("observer heard %d switches for a no-op clear, want 0", len(got))
	}

	if err := d.SetBillingOverride("api_key", usageLimitTestNow.Add(time.Hour), "cli"); err != nil {
		t.Fatalf("SetBillingOverride: %v", err)
	}
	if err := d.ClearBillingOverride("cli"); err != nil {
		t.Fatalf("ClearBillingOverride: %v", err)
	}
	if err := d.SetBillingOverride("api_key", usageLimitTestNow.Add(time.Hour), "gateway"); err != nil {
		t.Fatalf("SetBillingOverride: %v", err)
	}
	clock.Advance(time.Hour)

	want := []struct{ mode, source string }{
		{"api_key", "cli"},
		{"subscription", BillingModeSourceConfig},
		{"api_key", "gateway"},
		{"subscription", BillingModeSourceConfig},
	}
	if len(got) != len(want) {
		t.Fatalf("observer heard %d switches, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Mode != w.mode || got[i].Source != w.source {
			t.Errorf("switch %d = %s/%s, want %s/%s", i, got[i].Mode, got[i].Source, w.mode, w.source)
		}
	}
}
