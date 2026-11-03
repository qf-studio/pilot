package executor

import (
	"os"
	"strings"

	"github.com/qf-studio/pilot/internal/memory"
)

// KeySourceEnv names the env var the hosted console's fetch-secrets step
// writes on a tenant box to say which Anthropic key the daemon runs on:
// "platform" (included allowance) or "own" (the customer's key, overage).
const KeySourceEnv = "PILOT_ANTHROPIC_KEY_SOURCE"

// KeySourceFromEnv returns the Anthropic key source for an execution starting
// now: "platform" or "own" as set in KeySourceEnv, otherwise "unknown"
// (unset, empty or any other value). GH-5676.
func KeySourceFromEnv() string {
	return memory.NormalizeKeySource(os.Getenv(KeySourceEnv))
}

// DerivedBillingMode is the billing_mode of an execution that runs with
// billing.mode unset (GH-6032): api_key when environ carries a non-empty
// ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN, since the claude CLI then bills
// that key, otherwise subscription (its OAuth login). environ is in
// os.Environ form.
func DerivedBillingMode(environ []string) string {
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if ok && modelEnvBillingCredentialNames[name] && value != "" {
			return memory.BillingModeAPIKey
		}
	}
	return memory.BillingModeSubscription
}
