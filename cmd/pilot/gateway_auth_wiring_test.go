package main

import (
	"strings"
	"testing"
)

// TestRunPollingMode_GatewayUsesNewServerWithAuth is a source-level wiring
// guard for GH-4756: the gateway server constructed in polling mode must be
// built via gateway.NewServerWithAuth(cfg.Gateway, cfg.GatewayAuthConfig())
// rather than the auth-less gateway.NewServer(cfg.Gateway) — the latter
// silently ignores any configured gateway.auth_token and leaves /api/v1/
// (including the pre-merge approval decision route) unauthenticated in
// production regardless of config. See the GH-4738 lesson: wire both
// production construction sites and prove each with a composed test
// (internal/config.TestGatewayAuthConfig_Composed covers the runtime
// behavior; this test guards against this call site silently reverting to
// the unauthenticated constructor).
func TestRunPollingMode_GatewayUsesNewServerWithAuth(t *testing.T) {
	body := githubFuncBody(t, "main.go", "func runPollingMode(")

	if !strings.Contains(body, "gateway.NewServerWithAuth(cfg.Gateway, cfg.GatewayAuthConfig())") {
		t.Error("runPollingMode must construct the gateway via gateway.NewServerWithAuth(cfg.Gateway, cfg.GatewayAuthConfig()), not gateway.NewServer")
	}
}
