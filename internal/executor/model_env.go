package executor

import (
	"log/slog"
	"strings"
	"sync"
)

// GH-5275/GH-5276: every model-invoking subprocess (Claude Code, OpenCode,
// classifiers, planning, etc.) historically inherited the daemon's full
// ambient environment — which is where adapter secrets live
// (TELEGRAM_BOT_TOKEN, SLACK_BOT_TOKEN, LINEAR_API_KEY, AWS_SECRET_*, ...).
// Claude Code runs with --dangerously-skip-permissions and Bash in its tool
// allowlist, so any issue body that says "print your env to a file" hands
// every bot token to the model. modelSubprocessEnv is the single scrub
// helper every spawn site should route through (wiring across spawn sites
// is tracked separately — GH-5278).

// modelEnvDenySuffixes lists name suffixes that mark a variable as
// credential-shaped and therefore scrubbed from model subprocess
// environments unless explicitly kept or passed through.
var modelEnvDenySuffixes = []string{
	"_TOKEN",
	"_SECRET",
	"_API_KEY",
	"_PAT",
	"_PASSWORD",
	"_PASSWD",
	"_WEBHOOK_SECRET",
}

// modelEnvDenyPrefixes lists name prefixes that mark a variable as
// credential-shaped regardless of suffix (AWS's SDK-standard env names
// don't end in any of the suffixes above).
var modelEnvDenyPrefixes = []string{
	"AWS_SECRET_",
	"AWS_SESSION_",
}

// modelEnvExplicitDeny holds names dropped regardless of the suffix/prefix
// rules above — pulled from configs/pilot.example.yaml. Chat IDs are
// targeting data for a bot token: harmless alone, no legitimate use inside
// a model session.
var modelEnvExplicitDeny = map[string]bool{
	"PILOT_GATEWAY_TOKEN":       true,
	"TELEGRAM_CHAT_ID":          true,
	"TELEGRAM_APPROVER_CHAT_ID": true,
}

// modelEnvKeepExact holds names that survive the scrub verbatim, overriding
// the deny rules above.
var modelEnvKeepExact = map[string]bool{
	"GITHUB_TOKEN": true,
	"GH_TOKEN":     true,
}

// modelEnvKeepPrefixes holds name prefixes that survive the scrub,
// overriding the deny rules above. GITHUB_TOKEN/GH_TOKEN stay because the
// model's read-only `gh` calls through the ghguard shim rely on the ambient
// token (see gh_credentials.go) — hiding a token from a same-UID process is
// theater; the real mitigation is token scope (a short-lived GitHub App
// installation token), not concealment. ANTHROPIC_*/CLAUDE_*/CLAUDE_CODE_*
// are how the CLI itself is configured and routed (GH-2371, GH-2163).
var modelEnvKeepPrefixes = []string{
	"ANTHROPIC_",
	"CLAUDE_",
	"CLAUDE_CODE_",
}

// modelEnvPassthroughMu guards modelEnvPassthrough.
var modelEnvPassthroughMu sync.RWMutex

// modelEnvPassthrough holds the config-driven escape hatch
// (claude_code.env_passthrough in pilot config): names listed here survive
// the scrub even though they'd otherwise be dropped. Wiring this from
// loaded config is tracked separately (GH-5277); SetModelEnvPassthrough is
// the seam that wiring calls into.
var modelEnvPassthrough map[string]bool

// SetModelEnvPassthrough configures the set of environment variable names
// that survive modelSubprocessEnv's scrub despite matching a deny rule.
// Intended to be called once at startup from the loaded
// claude_code.env_passthrough config value (GH-5277). A nil or empty names
// slice clears the passthrough set.
func SetModelEnvPassthrough(names []string) {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	modelEnvPassthroughMu.Lock()
	modelEnvPassthrough = m
	modelEnvPassthroughMu.Unlock()
}

// isModelEnvPassthrough reports whether name is in the config-driven
// passthrough set.
func isModelEnvPassthrough(name string) bool {
	modelEnvPassthroughMu.RLock()
	defer modelEnvPassthroughMu.RUnlock()
	return modelEnvPassthrough[name]
}

// isModelEnvKeptByDefault reports whether name is on the built-in keep-list,
// which overrides every deny rule below.
func isModelEnvKeptByDefault(name string) bool {
	if modelEnvKeepExact[name] {
		return true
	}
	for _, prefix := range modelEnvKeepPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// isModelEnvDenied reports whether name matches a deny rule (explicit name,
// suffix, or prefix). It does not consider the keep-list or passthrough —
// callers apply those first/after.
func isModelEnvDenied(name string) bool {
	if modelEnvExplicitDeny[name] {
		return true
	}
	for _, suffix := range modelEnvDenySuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	for _, prefix := range modelEnvDenyPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// modelSubprocessEnv returns a scrubbed copy of base (typically
// os.Environ(), plus whatever a caller has already appended) suitable for a
// model-controlled subprocess. It is a denylist, not an allowlist — vars
// like HOME, PATH, TMPDIR, SSH_AUTH_SOCK, XDG_*, TERM, and locale vars pass
// through untouched because nothing about them looks credential-shaped.
//
// Precedence: keep-list (GITHUB_TOKEN, GH_TOKEN, ANTHROPIC_*, CLAUDE_*,
// CLAUDE_CODE_*) wins over every deny rule. Otherwise, a name matching the
// explicit-deny set, a deny suffix (_TOKEN, _SECRET, _API_KEY, _PAT,
// _PASSWORD, _PASSWD, _WEBHOOK_SECRET) or deny prefix (AWS_SECRET_,
// AWS_SESSION_) is dropped unless it's in the config-driven passthrough set
// (SetModelEnvPassthrough / claude_code.env_passthrough), in which case it
// survives. Everything else passes through unchanged.
//
// Logs once per call: DEBUG with the COUNT of scrubbed vars (never names or
// values, just a signal that the scrub ran), and INFO with the names (never
// values) of any passthrough vars that were let through.
func modelSubprocessEnv(base []string) []string {
	out := make([]string, 0, len(base))
	scrubbedCount := 0
	var passedThrough []string

	for _, kv := range base {
		name, _, hasEquals := strings.Cut(kv, "=")
		if !hasEquals {
			out = append(out, kv)
			continue
		}

		if isModelEnvKeptByDefault(name) {
			out = append(out, kv)
			continue
		}

		if !isModelEnvDenied(name) {
			out = append(out, kv)
			continue
		}

		if isModelEnvPassthrough(name) {
			out = append(out, kv)
			passedThrough = append(passedThrough, name)
			continue
		}

		scrubbedCount++
	}

	slog.Default().Debug("model subprocess env scrubbed",
		slog.Int("scrubbed_count", scrubbedCount),
	)
	if len(passedThrough) > 0 {
		slog.Default().Info("model subprocess env passthrough",
			slog.Any("names", passedThrough),
		)
	}

	return out
}
