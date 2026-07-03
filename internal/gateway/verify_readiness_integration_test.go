package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/health/verify"
)

// fakeVerifiable is a minimal verify.Verifiable double standing in for a
// real adapter client (GitHub/Telegram/Slack/...) so /ready can be
// exercised end-to-end without live network calls (GH-3769).
type fakeVerifiable struct {
	name string
	err  error
}

func (f *fakeVerifiable) Name() string                     { return f.name }
func (f *fakeVerifiable) Verify(ctx context.Context) error { return f.err }

var _ verify.Verifiable = (*fakeVerifiable)(nil)

// TestReadyEndpoint_FakeVerifiableRegistry registers a small fake registry
// of verify.Verifiable adapters — one healthy, one failing, one with an
// unimplemented probe — via the same NewReadinessAdapter +
// RegisterReadinessChecker path GH-3769 wires real adapters through, and
// asserts /ready reflects each adapter's real Verify(ctx) outcome.
func TestReadyEndpoint_FakeVerifiableRegistry(t *testing.T) {
	server := NewServer(&Config{Host: "127.0.0.1", Port: 9090})

	registry := []verify.Verifiable{
		&fakeVerifiable{name: "github", err: nil},
		&fakeVerifiable{name: "telegram", err: errors.New("telegram token invalid: 401")},
		&fakeVerifiable{name: "linear", err: verify.ErrProbeNotImplemented},
	}
	for _, v := range registry {
		server.RegisterReadinessChecker(verify.NewReadinessAdapter(v, time.Second))
	}

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	server.handleReady(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d (a failing adapter should trip /ready)", w.Code, http.StatusServiceUnavailable)
	}

	var response struct {
		Ready  bool            `json:"ready"`
		Checks map[string]bool `json:"checks"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response.Ready {
		t.Error("ready = true, want false")
	}
	if !response.Checks["github"] {
		t.Error("github check = false, want true (Verify returned nil)")
	}
	if response.Checks["telegram"] {
		t.Error("telegram check = true, want false (Verify returned an error)")
	}
	// ErrProbeNotImplemented has no live probe yet, so it maps to
	// not-ready in the boolean ReadinessChecker contract — see
	// verify.ReadinessAdapter's doc comment.
	if response.Checks["linear"] {
		t.Error("linear check = true, want false (probe not implemented yet)")
	}
}

// TestReadyEndpoint_FakeVerifiableRegistry_AllHealthy confirms a registry
// where every adapter verifies cleanly reports 200 and ready=true.
func TestReadyEndpoint_FakeVerifiableRegistry_AllHealthy(t *testing.T) {
	server := NewServer(&Config{Host: "127.0.0.1", Port: 9090})

	registry := []verify.Verifiable{
		&fakeVerifiable{name: "github", err: nil},
		&fakeVerifiable{name: "slack", err: nil},
	}
	for _, v := range registry {
		server.RegisterReadinessChecker(verify.NewReadinessAdapter(v, time.Second))
	}

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	server.handleReady(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var response struct {
		Ready bool `json:"ready"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !response.Ready {
		t.Error("ready = false, want true")
	}
}
