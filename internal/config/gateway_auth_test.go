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
	"github.com/qf-studio/pilot/internal/testutil"
)

// GH-4756: gateway.Authenticator and gateway.NewServerWithAuth already existed
// (PR#4752), but neither production construction site (gateway mode —
// internal/pilot.New — or polling mode — cmd/pilot runPollingMode) called
// NewServerWithAuth; both used gateway.NewServer, which unconditionally
// leaves the server unauthenticated. These tests cover the helper that both
// sites now use to bridge cfg.Auth into that call.

func TestConfig_GatewayAuthConfig_NilAuth(t *testing.T) {
	cfg := &Config{}
	if got := cfg.GatewayAuthConfig(); got != nil {
		t.Errorf("GatewayAuthConfig() = %+v, want nil when Auth is nil", got)
	}
}

func TestConfig_GatewayAuthConfig_EmptyToken(t *testing.T) {
	cfg := &Config{Auth: &gateway.AuthConfig{Type: gateway.AuthTypeAPIToken, Token: ""}}
	if got := cfg.GatewayAuthConfig(); got != nil {
		t.Errorf("GatewayAuthConfig() = %+v, want nil when Token is empty (backwards compatible default)", got)
	}
}

func TestConfig_GatewayAuthConfig_TokenSet(t *testing.T) {
	cfg := &Config{Auth: &gateway.AuthConfig{Type: gateway.AuthTypeClaudeCode, Token: testutil.FakeBearerToken}}

	got := cfg.GatewayAuthConfig()
	if got == nil {
		t.Fatal("GatewayAuthConfig() = nil, want non-nil when Token is set")
	}
	if got.Token != testutil.FakeBearerToken {
		t.Errorf("Token = %q, want %q", got.Token, testutil.FakeBearerToken)
	}
	// Bearer-compare must be forced regardless of the configured Type — the
	// AuthTypeClaudeCode dispatch ignores Token entirely (isLocalRequest
	// only), which is exactly the gap this closes.
	if got.Type != gateway.AuthTypeAPIToken {
		t.Errorf("Type = %q, want %q (bearer-compare must be forced when a token is set)", got.Type, gateway.AuthTypeAPIToken)
	}
}

// TestGatewayAuthConfig_Composed reproduces the exact composition both
// production sites now perform — gateway.NewServerWithAuth(cfg.Gateway,
// cfg.GatewayAuthConfig()) — against a real HTTP server, and asserts a read
// route (/api/v1/status) and the state-mutating decision route
// (/api/v1/approvals/{requestId}/decision) both reject unauthenticated
// requests and accept a correct bearer token.
func TestGatewayAuthConfig_Composed(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	mgr := approval.NewManager(approval.DefaultConfig()).WithStateWriter(store)

	const execID = "exec-gh4756-1"
	const reqID = "req-gh4756-1"
	if err := store.SaveExecution(&memory.Execution{
		ID:                execID,
		TaskID:            "GH-4756",
		ProjectPath:       "/tmp/gh4756-proj",
		Status:            "running",
		ApprovalRequestID: reqID,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.InsertPendingApproval(&memory.PendingApproval{
		ID:        reqID,
		TaskID:    "GH-4756",
		Stage:     "pre_merge",
		Title:     "GH-4756 composed wiring test",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("InsertPendingApproval: %v", err)
	}

	cfg := &Config{
		Gateway: &gateway.Config{Host: "127.0.0.1", Port: 19297},
		Auth:    &gateway.AuthConfig{Type: gateway.AuthTypeClaudeCode, Token: "gh4756-composed-secret"},
	}

	// This is the exact call production code makes at both construction
	// sites (internal/pilot/pilot.go, cmd/pilot/main.go) — proving the
	// wiring, not just the primitive.
	server := gateway.NewServerWithAuth(cfg.Gateway, cfg.GatewayAuthConfig())
	server.SetDashboardStore(store)
	server.SetDecisionRecorder(mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://127.0.0.1:19297"

	// 1. No bearer token: 401 on both the read route and the decision route.
	statusReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/status", nil)
	statusResp, err := client.Do(statusReq)
	if err != nil {
		t.Fatalf("GET /api/v1/status (no auth): %v", err)
	}
	_ = statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/status no-auth status = %d, want 401", statusResp.StatusCode)
	}

	decideReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/approvals/"+reqID+"/decision",
		bytes.NewBufferString(`{"decision":"approve","by":"alice"}`))
	decideResp, err := client.Do(decideReq)
	if err != nil {
		t.Fatalf("POST decision (no auth): %v", err)
	}
	_ = decideResp.Body.Close()
	if decideResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST decision no-auth status = %d, want 401", decideResp.StatusCode)
	}

	// 2. Correct bearer token: 200 on the read route.
	statusReq2, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/status", nil)
	statusReq2.Header.Set("Authorization", "Bearer gh4756-composed-secret")
	statusResp2, err := client.Do(statusReq2)
	if err != nil {
		t.Fatalf("GET /api/v1/status (bearer): %v", err)
	}
	_ = statusResp2.Body.Close()
	if statusResp2.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/status with bearer status = %d, want 200", statusResp2.StatusCode)
	}

	// 3. Correct bearer token: 200 on the decision route.
	decideReq2, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/approvals/"+reqID+"/decision",
		bytes.NewBufferString(`{"decision":"approve","by":"alice"}`))
	decideReq2.Header.Set("Authorization", "Bearer gh4756-composed-secret")
	decideResp2, err := client.Do(decideReq2)
	if err != nil {
		t.Fatalf("POST decision (bearer): %v", err)
	}
	_ = decideResp2.Body.Close()
	if decideResp2.StatusCode != http.StatusOK {
		t.Errorf("POST decision with bearer status = %d, want 200", decideResp2.StatusCode)
	}
}
