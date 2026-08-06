package pilot

import (
	"os"
	"strings"
	"testing"
)

// funcBody returns the source text of the named top-level function in file,
// from the given signature prefix up to (but not including) the next
// top-level "\nfunc " declaration. Used for source-level wiring guards where
// asserting the actual runtime behavior would require standing up the full
// daemon (adapters, memory store, etc.).
func funcBody(t *testing.T, file, funcSignature string) string {
	t.Helper()
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	src := string(content)
	start := strings.Index(src, funcSignature)
	if start < 0 {
		t.Fatalf("function %q not found in %s", funcSignature, file)
	}
	rest := src[start+len(funcSignature):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		return rest[:end]
	}
	return rest
}

// TestNew_GatewayUsesNewServerWithAuth is a source-level wiring guard for
// GH-4756: gateway mode's server must be built via
// gateway.NewServerWithAuth(gatewayCfg, cfg.GatewayAuthConfig()) rather than
// the auth-less gateway.NewServer(gatewayCfg) — the latter silently ignores
// any configured gateway.auth_token and leaves /api/v1/ (including the
// pre-merge approval decision route) unauthenticated in production
// regardless of config. See the GH-4738 lesson: wire both production
// construction sites and prove each with a composed test
// (internal/config.TestGatewayAuthConfig_Composed covers the runtime
// behavior; this test guards against this call site silently reverting to
// the unauthenticated constructor).
func TestNew_GatewayUsesNewServerWithAuth(t *testing.T) {
	body := funcBody(t, "pilot.go", "func New(cfg *config.Config, opts ...Option) (*Pilot, error) {")

	if !strings.Contains(body, "gateway.NewServerWithAuth(gatewayCfg, cfg.GatewayAuthConfig())") {
		t.Error("New must construct the gateway via gateway.NewServerWithAuth(gatewayCfg, cfg.GatewayAuthConfig()), not gateway.NewServer")
	}
}
