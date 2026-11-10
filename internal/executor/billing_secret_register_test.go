package executor

import (
	"context"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// mutation pin: Resolve does not register the api_key_command output for redaction -> this test fails
func TestBillingResolve_RegistersCommandKeyForRedaction(t *testing.T) {
	skipWithoutShell(t)
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)

	const key = "zq-command-printed-key-6274"
	r := NewBillingResolver(apiKeyConfig("printf %s " + key))
	if _, err := r.Resolve(context.Background()); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := secretpatterns.Redact("log " + key + " end"); got != "log <redacted> end" {
		t.Errorf("Redact = %q; command output not registered", got)
	}
}
