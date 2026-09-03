package executor

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// GH-5281: full-matrix regression coverage for modelSubprocessEnv
// (model_env.go, GH-5275/GH-5276) and the config-driven passthrough seam
// (SetModelEnvPassthrough, GH-5277).
//
// modelSubprocessEnv is a denylist, not an allowlist: adapter secrets get
// dropped, CLI/runtime plumbing survives untouched, and a name listed in
// claude_code.env_passthrough survives even though it would otherwise match
// a deny rule. These tests pin every category called out in GH-5281 in one
// place so a future edit to the deny/keep/passthrough tables can't silently
// narrow (or widen) the scrub without a test noticing, and separately prove
// the helper's logging never emits a secret VALUE even though it's allowed
// to log names/counts.

// modelEnvScrubMatrix enumerates every adapter/credential-shaped variable
// GH-5281 requires to be scrubbed, and which deny rule each one is expected
// to hit (documentation only — the assertion doesn't care which rule fired,
// just that the var is gone).
var modelEnvScrubMatrix = []struct {
	name string
	rule string
}{
	{"TELEGRAM_BOT_TOKEN", "suffix _TOKEN"},
	{"SLACK_BOT_TOKEN", "suffix _TOKEN"},
	{"LINEAR_API_KEY", "suffix _API_KEY"},
	{"AZURE_DEVOPS_PAT", "suffix _PAT"},
	{"GITLAB_WEBHOOK_SECRET", "suffix _WEBHOOK_SECRET"},
	{"AWS_SECRET_ACCESS_KEY", "prefix AWS_SECRET_"},
	{"PILOT_GATEWAY_TOKEN", "explicit deny + suffix _TOKEN"},
}

// modelEnvKeepMatrix enumerates every variable GH-5281 requires to survive
// the scrub untouched, either because it's on the keep-list (overrides deny
// rules) or because it never looked credential-shaped to begin with.
var modelEnvKeepMatrix = []struct {
	name  string
	value string
}{
	{"GITHUB_TOKEN", "gh-token-value"},
	{"ANTHROPIC_API_KEY", "anthropic-key-value"},
	{"CLAUDE_CODE_MAX_OUTPUT_TOKENS", "8192"},
	{"HOME", "/home/tester"},
	{"PATH", "/usr/bin:/bin"},
	{"SSH_AUTH_SOCK", "/tmp/ssh-agent.sock"},
}

// TestModelSubprocessEnv_ScrubMatrix proves every adapter secret in the
// GH-5281 matrix is dropped, in isolation, so a failure points at exactly
// which variable regressed.
func TestModelSubprocessEnv_ScrubMatrix(t *testing.T) {
	for _, tc := range modelEnvScrubMatrix {
		t.Run(tc.name, func(t *testing.T) {
			base := []string{tc.name + "=super-secret-value", "PATH=/usr/bin"}
			out := modelSubprocessEnv(base)
			for _, kv := range out {
				name, _, _ := strings.Cut(kv, "=")
				if name == tc.name {
					t.Errorf("expected %s to be scrubbed (%s), but it survived: %q", tc.name, tc.rule, kv)
				}
			}
		})
	}
}

// TestModelSubprocessEnv_RetainMatrix proves every variable in the GH-5281
// retain matrix survives unchanged, in isolation.
func TestModelSubprocessEnv_RetainMatrix(t *testing.T) {
	for _, tc := range modelEnvKeepMatrix {
		t.Run(tc.name, func(t *testing.T) {
			base := []string{tc.name + "=" + tc.value}
			out := modelSubprocessEnv(base)
			if len(out) != 1 || out[0] != tc.name+"="+tc.value {
				t.Errorf("expected %s=%s to be retained verbatim, got %v", tc.name, tc.value, out)
			}
		})
	}
}

// TestModelSubprocessEnv_ConfigPassthroughSurvives covers GH-5277: a name
// listed via SetModelEnvPassthrough (claude_code.env_passthrough in config)
// must survive the scrub even though it matches a deny rule (MY_REPO_API_KEY
// ends in _API_KEY, which is denied by default).
func TestModelSubprocessEnv_ConfigPassthroughSurvives(t *testing.T) {
	SetModelEnvPassthrough([]string{"MY_REPO_API_KEY"})
	t.Cleanup(func() { SetModelEnvPassthrough(nil) })

	base := []string{
		"MY_REPO_API_KEY=passthrough-secret-value",
		"LINEAR_API_KEY=still-denied-value", // same suffix, NOT on the passthrough list
	}
	out := modelSubprocessEnv(base)

	found := map[string]string{}
	for _, kv := range out {
		name, val, _ := strings.Cut(kv, "=")
		found[name] = val
	}

	if got, ok := found["MY_REPO_API_KEY"]; !ok || got != "passthrough-secret-value" {
		t.Errorf("expected MY_REPO_API_KEY to survive via passthrough, got %v (present=%v)", got, ok)
	}
	if _, ok := found["LINEAR_API_KEY"]; ok {
		t.Errorf("expected LINEAR_API_KEY to remain scrubbed despite an unrelated passthrough entry, but it survived")
	}
}

// TestModelSubprocessEnv_ConfigPassthroughClearedByNil proves
// SetModelEnvPassthrough(nil) actually clears a previously configured
// passthrough set, rather than leaving stale entries wired in from a prior
// caller/test.
func TestModelSubprocessEnv_ConfigPassthroughClearedByNil(t *testing.T) {
	SetModelEnvPassthrough([]string{"MY_REPO_API_KEY"})
	SetModelEnvPassthrough(nil)
	t.Cleanup(func() { SetModelEnvPassthrough(nil) })

	out := modelSubprocessEnv([]string{"MY_REPO_API_KEY=passthrough-secret-value"})
	if len(out) != 0 {
		t.Errorf("expected MY_REPO_API_KEY to be scrubbed after passthrough was cleared, got %v", out)
	}
}

// TestModelSubprocessEnv_FullMatrix_NoSecretValuesInLogs is the GH-5281
// centerpiece: builds the entire scrub/keep/passthrough matrix into one
// base environment (mirroring a real daemon's ambient env), captures every
// log line modelSubprocessEnv emits during the call, and asserts:
//  1. every scrubbed name is actually absent from the output env,
//  2. every retained name survives with its original value,
//  3. the config-driven passthrough entry survives,
//  4. no secret VALUE (scrubbed or passed-through) appears anywhere in the
//     captured log output — modelSubprocessEnv is documented to log only
//     counts and passthrough NAMES, never values.
func TestModelSubprocessEnv_FullMatrix_NoSecretValuesInLogs(t *testing.T) {
	secretValues := map[string]string{}
	var base []string

	for _, tc := range modelEnvScrubMatrix {
		val := "secret-value-for-" + tc.name
		secretValues[tc.name] = val
		base = append(base, tc.name+"="+val)
	}

	for _, tc := range modelEnvKeepMatrix {
		base = append(base, tc.name+"="+tc.value)
	}

	passthroughName := "MY_REPO_API_KEY"
	passthroughValue := "secret-value-for-" + passthroughName
	secretValues[passthroughName] = passthroughValue
	base = append(base, passthroughName+"="+passthroughValue)

	SetModelEnvPassthrough([]string{passthroughName})
	t.Cleanup(func() { SetModelEnvPassthrough(nil) })

	var logBuf bytes.Buffer
	prevDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prevDefault)

	out := modelSubprocessEnv(base)

	outVals := make(map[string]string, len(out))
	for _, kv := range out {
		name, val, _ := strings.Cut(kv, "=")
		outVals[name] = val
	}

	for _, tc := range modelEnvScrubMatrix {
		if got, present := outVals[tc.name]; present {
			t.Errorf("expected %s to be scrubbed, but found in output env with value %q", tc.name, got)
		}
	}

	for _, tc := range modelEnvKeepMatrix {
		got, present := outVals[tc.name]
		if !present {
			t.Errorf("expected %s to be retained, but it was scrubbed", tc.name)
			continue
		}
		if got != tc.value {
			t.Errorf("expected %s=%q, got %q", tc.name, tc.value, got)
		}
	}

	if got, present := outVals[passthroughName]; !present {
		t.Errorf("expected env_passthrough entry %s to survive the scrub, but it was dropped", passthroughName)
	} else if got != passthroughValue {
		t.Errorf("expected %s=%q, got %q", passthroughName, passthroughValue, got)
	}

	logOutput := logBuf.String()
	if logOutput == "" {
		t.Fatalf("expected modelSubprocessEnv to emit at least a debug scrub-count line, got no log output")
	}
	for name, val := range secretValues {
		if strings.Contains(logOutput, val) {
			t.Errorf("secret value for %s leaked into log output: log=%q", name, logOutput)
		}
	}

	// The passthrough NAME is expected/documented to appear (INFO log lists
	// passthrough names, never values) — assert it's there so this test
	// would fail if that logging behavior silently regressed to log values
	// instead of names.
	if !strings.Contains(logOutput, passthroughName) {
		t.Errorf("expected passthrough NAME %s to appear in log output (names, not values, are logged), got: %s", passthroughName, logOutput)
	}
}
