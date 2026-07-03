package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/gitlab"
	"github.com/qf-studio/pilot/internal/adapters/linear"
	"github.com/qf-studio/pilot/internal/adapters/slack"
	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/gateway"
	"github.com/qf-studio/pilot/internal/health/verify"
)

// fakeVerifiable is a minimal verify.Verifiable double used to exercise
// runAdapterPreflight/registerAdapterReadiness without hitting real adapter
// APIs (GH-3769).
type fakeVerifiable struct {
	name string
	err  error
}

func (f *fakeVerifiable) Name() string                     { return f.name }
func (f *fakeVerifiable) Verify(ctx context.Context) error { return f.err }

var _ verify.Verifiable = (*fakeVerifiable)(nil)

// TestBuildAdapterVerifiers confirms only enabled adapters with a resolvable
// credential produce a verify.Verifiable, and disabled/empty-token adapters
// are skipped (their presence is already flagged by doctor's checks).
func TestBuildAdapterVerifiers(t *testing.T) {
	resetGitHubTokenTestState(t)

	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			Telegram: &telegram.Config{Enabled: true, BotToken: "tg-token"},
			Slack:    &slack.Config{Enabled: false, BotToken: "slack-token"}, // disabled — skipped
			GitHub:   &github.Config{Enabled: true, Token: "gh-token"},
			Linear:   &linear.Config{Enabled: true, APIKey: ""}, // no key — skipped
			GitLab:   &gitlab.Config{Enabled: true, Token: "gl-token", Project: "group/proj"},
		},
	}

	verifiers := buildAdapterVerifiers(cfg)

	names := make(map[string]bool, len(verifiers))
	for _, v := range verifiers {
		names[v.Name()] = true
	}

	for _, want := range []string{"telegram", "github", "gitlab"} {
		if !names[want] {
			t.Errorf("expected verifier %q to be built, got %v", want, names)
		}
	}
	for _, notWant := range []string{"slack", "linear"} {
		if names[notWant] {
			t.Errorf("did not expect verifier %q (disabled or no credential), got %v", notWant, names)
		}
	}
}

// TestBuildAdapterVerifiers_NoAdapters confirms a nil/empty adapters config
// produces no verifiers instead of panicking.
func TestBuildAdapterVerifiers_NoAdapters(t *testing.T) {
	if got := buildAdapterVerifiers(&config.Config{}); len(got) != 0 {
		t.Errorf("expected no verifiers, got %v", got)
	}
	if got := buildAdapterVerifiers(nil); len(got) != 0 {
		t.Errorf("expected no verifiers for nil config, got %v", got)
	}
}

// TestRunAdapterPreflight_FakeRegistry exercises runAdapterPreflight against
// a fake registry of Verifiables covering the healthy, failing, and
// not-yet-implemented cases, confirming: the returned per-adapter result map
// is accurate, a genuine failure fires a config_error alert, and a healthy
// or not-implemented probe does not.
func TestRunAdapterPreflight_FakeRegistry(t *testing.T) {
	engine, ch := newTestAlertsEngine(t)

	verifiers := []verify.Verifiable{
		&fakeVerifiable{name: "healthy", err: nil},
		&fakeVerifiable{name: "dead-credential", err: errors.New("token invalid: 401")},
		&fakeVerifiable{name: "unimplemented", err: verify.ErrProbeNotImplemented},
	}

	results := runAdapterPreflight(context.Background(), verifiers, engine)

	if err := results["healthy"]; err != nil {
		t.Errorf("healthy result = %v, want nil", err)
	}
	if err := results["dead-credential"]; err == nil {
		t.Error("dead-credential result = nil, want an error")
	}
	if err := results["unimplemented"]; !errors.Is(err, verify.ErrProbeNotImplemented) {
		t.Errorf("unimplemented result = %v, want ErrProbeNotImplemented", err)
	}

	// Event dispatch is async (queued via a channel); poll with a deadline
	// rather than engine.WaitForDispatch(), which only blocks on dispatches
	// already in flight and can race ahead of the event being picked up.
	deadline := time.After(2 * time.Second)
	for ch.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("expected an alert to be fired for the dead-credential adapter")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if got := ch.count(); got != 1 {
		t.Errorf("expected exactly 1 alert (dead-credential only), got %d", got)
	}
}

// TestRunAdapterPreflight_EmptyRegistry confirms an empty/nil verifier list
// is a safe no-op.
func TestRunAdapterPreflight_EmptyRegistry(t *testing.T) {
	results := runAdapterPreflight(context.Background(), nil, nil)
	if len(results) != 0 {
		t.Errorf("expected empty result map, got %v", results)
	}
}

// TestRegisterAdapterReadiness_Nil confirms registering against a nil
// gateway server (gateway disabled) does not panic.
func TestRegisterAdapterReadiness_Nil(t *testing.T) {
	registerAdapterReadiness(nil, []verify.Verifiable{&fakeVerifiable{name: "x"}}, time.Second)
}

// TestRegisterAdapterReadiness_ReadyEndpoint wires a fake registry into a
// real gateway.Server via registerAdapterReadiness and confirms /ready
// reflects each adapter's live Verify(ctx) outcome end-to-end (GH-3769: the
// same path daemon startup uses to make readiness real, not presence-only).
func TestRegisterAdapterReadiness_ReadyEndpoint(t *testing.T) {
	gwServer := gateway.NewServer(&gateway.Config{Host: "127.0.0.1", Port: 19199})

	verifiers := []verify.Verifiable{
		&fakeVerifiable{name: "healthy-adapter", err: nil},
		&fakeVerifiable{name: "broken-adapter", err: errors.New("credential invalid")},
	}
	registerAdapterReadiness(gwServer, verifiers, time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = gwServer.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:19199/ready")
	if err != nil {
		t.Fatalf("GET /ready: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d (broken-adapter should trip readiness)", resp.StatusCode, http.StatusServiceUnavailable)
	}

	var body struct {
		Ready  bool            `json:"ready"`
		Checks map[string]bool `json:"checks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Checks["healthy-adapter"] {
		t.Error("healthy-adapter check = false, want true")
	}
	if body.Checks["broken-adapter"] {
		t.Error("broken-adapter check = true, want false")
	}
}
