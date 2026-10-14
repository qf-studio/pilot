package executor

import (
	"os"

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
