package config

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/gateway"
	"github.com/qf-studio/pilot/internal/memory"
)

// TestGatewayAuthConfig unit-tests the GH-4784 gate: only an explicitly
// configured api-token with a non-empty token should ever enable bearer
// auth. DefaultConfig()'s claude-code default and an empty token must both
// resolve to nil, so production construction sites fall back to the
// fully-open behavior that predates this change.
func TestGatewayAuthConfig(t *testing.T) {
	tests := []struct {
		name    string
		auth    *gateway.AuthConfig
		wantNil bool
	}{
		{"nil auth", nil, true},
		{"default claude-code type", &gateway.AuthConfig{Type: gateway.AuthTypeClaudeCode}, true},
		{"api-token with empty token", &gateway.AuthConfig{Type: gateway.AuthTypeAPIToken, Token: ""}, true},
		{"api-token with token", &gateway.AuthConfig{Type: gateway.AuthTypeAPIToken, Token: "secret"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{Auth: tt.auth}
			got := c.GatewayAuthConfig()
			if tt.wantNil && got != nil {
				t.Errorf("GatewayAuthConfig() = %+v, want nil", got)
			}
			if !tt.wantNil && got == nil {
				t.Errorf("GatewayAuthConfig() = nil, want %+v", tt.auth)
			}
		})
	}
}

// TestGatewayAuthConfig_ComposedProductionWiring reproduces the exact
// construction sequence production now uses in internal/pilot/pilot.go
// (gateway mode) and cmd/pilot/main.go (polling mode):
// *config.Config -> GatewayAuthConfig() -> gateway.NewServerWithAuth.
//
// PR#4752 added bearer-auth *support* (Authenticator.Middleware) and a
// gateway-package test proving it, but neither production call site ever
// invoked NewServerWithAuth with a real token — both called gateway.NewServer,
// which always passes a nil authConfig — so the deployed daemon served
// /api/v1 (including the pre-merge approval decision route) with no auth at
// all (GH-4784). This test starts from *config.Config, the same object
// production loads from YAML, and proves the flow now gates a read route
// AND the decision route, and that a correct bearer still lets requests
// through end to end (real memory.Store + real approval.Manager).
func TestGatewayAuthConfig_ComposedProductionWiring(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	mgr := approval.NewManager(approval.DefaultConfig()).WithStateWriter(store)

	const reqID = "req-gh4784-composed"
	if err := store.InsertPendingApproval(&memory.PendingApproval{
		ID:        reqID,
		TaskID:    "GH-4784",
		Stage:     "pre_merge",
		Title:     "GH-4784 composed wiring test approval",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("InsertPendingApproval: %v", err)
	}

	cfg := &Config{
		Gateway: &gateway.Config{Host: "127.0.0.1", Port: 19097},
		Auth:    &gateway.AuthConfig{Type: gateway.AuthTypeAPIToken, Token: "gh4784-composed-secret"},
	}

	// The exact call production makes.
	server := gateway.NewServerWithAuth(cfg.Gateway, cfg.GatewayAuthConfig())
	server.SetDashboardStore(store)
	server.SetDecisionRecorder(mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://127.0.0.1:19097"

	// 1. No bearer -> 401 on a read route and the decision route.
	statusReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/status", nil)
	statusResp, err := client.Do(statusReq)
	if err != nil {
		t.Fatalf("GET /status (no auth): %v", err)
	}
	_ = statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /status no-auth status = %d, want 401", statusResp.StatusCode)
	}

	decisionReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/approvals/"+reqID+"/decision",
		bytes.NewBufferString(`{"decision":"approve","by":"alice"}`))
	decisionResp, err := client.Do(decisionReq)
	if err != nil {
		t.Fatalf("POST decision (no auth): %v", err)
	}
	_ = decisionResp.Body.Close()
	if decisionResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST decision no-auth status = %d, want 401", decisionResp.StatusCode)
	}

	// 2. Correct bearer -> 200 on both routes.
	statusReq2, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/status", nil)
	statusReq2.Header.Set("Authorization", "Bearer gh4784-composed-secret")
	statusResp2, err := client.Do(statusReq2)
	if err != nil {
		t.Fatalf("GET /status (auth): %v", err)
	}
	_ = statusResp2.Body.Close()
	if statusResp2.StatusCode != http.StatusOK {
		t.Errorf("GET /status auth status = %d, want 200", statusResp2.StatusCode)
	}

	decisionReq2, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/approvals/"+reqID+"/decision",
		bytes.NewBufferString(`{"decision":"approve","by":"alice"}`))
	decisionReq2.Header.Set("Authorization", "Bearer gh4784-composed-secret")
	decisionReq2.Header.Set("Content-Type", "application/json")
	decisionResp2, err := client.Do(decisionReq2)
	if err != nil {
		t.Fatalf("POST decision (auth): %v", err)
	}
	_ = decisionResp2.Body.Close()
	if decisionResp2.StatusCode != http.StatusOK {
		t.Errorf("POST decision auth status = %d, want 200", decisionResp2.StatusCode)
	}
}

// TestGatewayAuthConfig_EmptyTokenStaysOpen proves acceptance criterion #2
// from GH-4784: an empty/default token reproduces today's fully
// backwards-compatible behavior — no Authenticator wired at all, so
// /api/v1 stays reachable without a bearer token (loopback bind remains
// the only mitigant, unchanged by this fix).
func TestGatewayAuthConfig_EmptyTokenStaysOpen(t *testing.T) {
	cfg := &Config{
		Gateway: &gateway.Config{Host: "127.0.0.1", Port: 19098},
		Auth:    DefaultConfig().Auth, // AuthTypeClaudeCode, no token — the real default
	}

	server := gateway.NewServerWithAuth(cfg.Gateway, cfg.GatewayAuthConfig())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:19098/api/v1/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (default/empty-token config must stay open)", resp.StatusCode)
	}
}
