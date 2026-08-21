package alerts

import (
	"errors"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// failingActiveAlertStore is a deliberately-broken ActiveAlertStore double:
// every method returns an error, used to verify persistence failures are
// best-effort and never block the alerting path (GH-4890 acceptance).
type failingActiveAlertStore struct {
	upsertErr error
	deleteErr error
	loadErr   error
	upserted  int
	deleted   int
}

func (f *failingActiveAlertStore) UpsertActiveAlert(a *memory.ActiveAlert) error {
	f.upserted++
	return f.upsertErr
}

func (f *failingActiveAlertStore) DeleteActiveAlert(ruleName, source string) error {
	f.deleted++
	return f.deleteErr
}

func (f *failingActiveAlertStore) LoadActiveAlerts() ([]*memory.ActiveAlert, error) {
	return nil, f.loadErr
}

// TestActiveAlertStore_NilStoreIsNoop is the GH-4890 regression pin: an
// engine constructed without WithActiveAlertStore (the default, matching
// every pre-GH-4890 caller) must behave exactly as it did before this store
// existed — fire/resolve dispatch identically, and the persistence helpers
// are silent no-ops rather than nil-pointer panics.
func TestActiveAlertStore_NilStoreIsNoop(t *testing.T) {
	engine, mockCh := newResolutionTestEngine(t, nil, 0)

	if engine.activeAlertStore != nil {
		t.Fatal("expected activeAlertStore to be nil when WithActiveAlertStore is not passed")
	}

	fireConfigError(engine, "adapter:github")
	fireConfigHealthy(engine, "adapter:github")

	got := mockCh.getAlerts()
	if len(got) != 2 {
		t.Fatalf("dispatched %d alerts, want 2 (fire + resolution) — unchanged from pre-GH-4890 behavior", len(got))
	}
	if !got[1].IsResolution() {
		t.Error("second dispatched alert is not a resolution")
	}

	// Calling the persistence helpers directly against a nil store must not
	// panic — every call site in markActive/handleConfigHealthy relies on
	// this guard.
	engine.persistActiveAlert(&activeAlert{rule: AlertRule{Name: "x"}, alert: &Alert{Source: "y"}})
	engine.deletePersistedActiveAlert("x", "y")
}

// TestActiveAlertStore_FailureIsBestEffort is the GH-4890 acceptance test for
// a deliberately-failing store: persistence errors on fire, resolve, and
// rehydrate must all be logged and swallowed — never block or alter alert
// delivery.
func TestActiveAlertStore_FailureIsBestEffort(t *testing.T) {
	failing := &failingActiveAlertStore{
		upsertErr: errors.New("disk full"),
		deleteErr: errors.New("disk full"),
		loadErr:   errors.New("disk full"),
	}

	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []AlertRule{
			{Name: "config-error", Type: AlertTypeServiceUnhealthy, Enabled: true, Severity: SeverityWarning},
		},
	}
	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	// A broken LoadActiveAlerts must not prevent engine construction.
	engine := NewEngine(config, WithDispatcher(dispatcher), WithActiveAlertStore(failing))

	fireConfigError(engine, "adapter:github")
	if got := len(mockCh.getAlerts()); got != 1 {
		t.Fatalf("dispatched %d alerts on fire despite failing UpsertActiveAlert, want 1 (alert still fires)", got)
	}
	if failing.upserted == 0 {
		t.Error("expected UpsertActiveAlert to have been attempted")
	}

	fireConfigHealthy(engine, "adapter:github")
	got := mockCh.getAlerts()
	if len(got) != 2 {
		t.Fatalf("dispatched %d alerts on resolve despite failing DeleteActiveAlert, want 2 (resolution still dispatches)", len(got))
	}
	if !got[1].IsResolution() {
		t.Error("second dispatched alert is not a resolution")
	}
	if failing.deleted == 0 {
		t.Error("expected DeleteActiveAlert to have been attempted")
	}
}

// TestActiveAlertPersistence_RehydrateAcrossRestart is the GH-4890 end-to-end
// acceptance test against real SQLite: fire an alert (persisting it) ->
// construct a fresh Engine instance against the same DB (simulating a daemon
// restart) -> rehydrate -> trigger the recovery event -> assert the
// resolution reached the ORIGINALLY-delivered channel set (not re-filtered
// by the resolution's own info severity) and the persisted row was deleted.
func TestActiveAlertPersistence_RehydrateAcrossRestart(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			// Severities deliberately excludes info: dispatchResolution
			// forces SeverityInfo on every resolution. If the rehydrated
			// resolution were re-routed through resolveChannels instead of
			// dispatching straight to the persisted channel set, this
			// channel would be wrongly filtered out and the test would fail.
			{Name: "ops-channel", Type: "webhook", Enabled: true, Severities: []Severity{SeverityWarning, SeverityCritical}},
		},
		Rules: []AlertRule{
			{Name: "config-error", Type: AlertTypeServiceUnhealthy, Enabled: true, Severity: SeverityWarning},
		},
	}

	// --- pre-restart process: fire, which persists the active alert ---
	mockCh1 := newMockChannel("ops-channel", "webhook")
	dispatcher1 := NewDispatcher(config)
	dispatcher1.RegisterChannel(mockCh1)
	engine1 := NewEngine(config, WithDispatcher(dispatcher1), WithActiveAlertStore(store))

	fireConfigError(engine1, "adapter:github")

	if got := len(mockCh1.getAlerts()); got != 1 {
		t.Fatalf("engine1 dispatched %d alerts on fire, want 1", got)
	}

	persisted, err := store.LoadActiveAlerts()
	if err != nil {
		t.Fatalf("LoadActiveAlerts after fire: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected 1 persisted active alert after fire, got %d", len(persisted))
	}
	if len(persisted[0].Channels) != 1 || persisted[0].Channels[0] != "ops-channel" {
		t.Fatalf("persisted channels = %v, want [ops-channel]", persisted[0].Channels)
	}

	// --- restart: fresh Engine, fresh dispatcher/channel instance, same
	// store and same channel *name* (as a real restart would re-register) ---
	mockCh2 := newMockChannel("ops-channel", "webhook")
	dispatcher2 := NewDispatcher(config)
	dispatcher2.RegisterChannel(mockCh2)
	engine2 := NewEngine(config, WithDispatcher(dispatcher2), WithActiveAlertStore(store))

	engine2.mu.RLock()
	_, rehydrated := engine2.activeAlerts[activeAlertKey("config-error", "adapter:github")]
	engine2.mu.RUnlock()
	if !rehydrated {
		t.Fatal("expected active alert to be rehydrated into engine2's in-memory map")
	}

	fireConfigHealthy(engine2, "adapter:github")

	got := mockCh2.getAlerts()
	if len(got) != 1 {
		t.Fatalf("engine2 dispatched %d alerts on rehydrated resolve, want 1", len(got))
	}
	resolution := got[0]
	if !resolution.IsResolution() {
		t.Error("rehydrated dispatch is not a resolution (ResolvedAt nil)")
	}
	if resolution.Source != "adapter:github" {
		t.Errorf("resolution.Source = %q, want adapter:github", resolution.Source)
	}

	remaining, err := store.LoadActiveAlerts()
	if err != nil {
		t.Fatalf("LoadActiveAlerts after resolve: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected persisted row deleted after resolve, got %d remaining", len(remaining))
	}
}
