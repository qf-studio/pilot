package executor

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// GH-5920: the review checkout's tool allowlist (Read, Grep, Glob) does not
// stop those tools from taking absolute paths, so a prompt-injected diff or
// file could make the model read the daemon home, credentials or the main
// clone and echo them into the posted comment. Each review therefore runs with
// a PreToolUse hook (hookscripts/pilot-review-confine.sh) that denies any
// target outside the worktree, and with a scrubbed environment whose HOME is a
// per-review directory.
//
// Decision: the hook scripts live in the per-review environment directory,
// never under the project checkout — the checkout holds untrusted content and
// its own .claude/settings.json is overwritten, so nothing in it can weaken
// the hook.

// postMergeConfineScript is the embedded hook script that confines a review.
const postMergeConfineScript = "pilot-review-confine.sh"

// postMergeReviewEnvPrefix names the per-review environment directory. It
// shares the pilot-worktree-review- prefix so the startup sweep reclaims one a
// crashed daemon left behind.
const postMergeReviewEnvPrefix = postMergeCheckoutPrefix + "env-"

// postMergeConfinedTools are the tools whose targets the hook resolves.
const postMergeConfinedTools = "Read|Grep|Glob"

// postMergeDeniedTools are denied outright, a second layer under --tools.
const postMergeDeniedTools = "Bash|Edit|Write|WebFetch|WebSearch"

// postMergeGitHubEnv are the variables the scrub keeps for the executor's
// read-only gh calls; the review has no Bash and so no use for them.
var postMergeGitHubEnv = map[string]bool{
	"GITHUB_TOKEN":            true,
	"GH_TOKEN":                true,
	"GITHUB_ENTERPRISE_TOKEN": true,
	"GH_ENTERPRISE_TOKEN":     true,
}

// postMergeAuthEnv are the variables that authenticate the model call. When
// none is set the CLI authenticates from its login file in the config dir.
var postMergeAuthEnv = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
}

// postMergeCredentialsFile is the CLI's login file inside its config dir.
const postMergeCredentialsFile = ".credentials.json"

// postMergeReviewEnv is the sandbox of one review: a directory holding the
// hook scripts, the denial log and the review's HOME.
type postMergeReviewEnv struct {
	dir     string
	home    string
	denyLog string
	cleanup func()
}

// shellQuote single-quotes s for a hook command line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// postMergeConfineSettings is the .claude/settings.json of a review checkout:
// the confinement hook on Read, Grep and Glob, and the same script denying
// every other tool outright. includeCoAuthoredBy stays off like task settings.
func postMergeConfineSettings(scriptPath, root, denyLog string) map[string]interface{} {
	cmd := strings.Join([]string{shellQuote(scriptPath), shellQuote(root), shellQuote(denyLog)}, " ")
	hook := []HookCommand{{Type: "command", Command: cmd}}
	settings := coAuthorSettings()
	settings["hooks"] = map[string][]HookMatcherEntry{
		"PreToolUse": {
			{Matcher: stringPtr(postMergeConfinedTools), Hooks: hook},
			{Matcher: stringPtr(postMergeDeniedTools), Hooks: hook},
		},
	}
	return settings
}

// preparePostMergeReviewEnv confines the review in worktree: it writes the
// hook scripts, a settings file into the worktree (overwriting whatever the
// merged commit carries — that content is untrusted) and a settings file into
// a fresh HOME. cleanup removes the environment; the settings file in the
// worktree goes with the worktree.
func preparePostMergeReviewEnv(worktree string) (*postMergeReviewEnv, error) {
	dir, err := os.MkdirTemp("", postMergeReviewEnvPrefix)
	if err != nil {
		return nil, fmt.Errorf("review env dir: %w", err)
	}
	env := &postMergeReviewEnv{
		dir:     dir,
		home:    filepath.Join(dir, "home"),
		denyLog: filepath.Join(dir, "denied.log"),
		cleanup: func() { _ = os.RemoveAll(dir) },
	}
	fail := func(err error) (*postMergeReviewEnv, error) {
		env.cleanup()
		return nil, err
	}

	hooksDir := filepath.Join(dir, "hooks")
	if err := WriteEmbeddedScripts(hooksDir); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(env.denyLog, nil, 0o600); err != nil {
		return fail(fmt.Errorf("deny log: %w", err))
	}

	settings := postMergeConfineSettings(filepath.Join(hooksDir, postMergeConfineScript), worktree, env.denyLog)
	// A committed settings.local.json outranks the project file and could add
	// hooks of its own.
	_ = os.Remove(filepath.Join(worktree, ".claude", "settings.local.json"))
	for _, p := range []string{
		filepath.Join(worktree, ".claude", "settings.json"),
		filepath.Join(env.home, ".claude", "settings.json"),
	} {
		if err := WriteClaudeSettings(p, settings); err != nil {
			return fail(err)
		}
	}
	return env, nil
}

// postMergeReviewEnviron is the environment of the review CLI: the executor's
// scrubbed model environment minus the GitHub tokens, with HOME and the CLI's
// config dir pointed at the review's own home. When the ambient environment
// carries no model credential, the CLI's login file is copied into that home —
// the one secret the model call needs.
func postMergeReviewEnviron(base []string, home string) []string {
	scrubbed := modelSubprocessEnv(base)
	out := make([]string, 0, len(scrubbed)+2)
	var ambientConfig string
	hasAuth := false
	for _, kv := range scrubbed {
		name, value, _ := strings.Cut(kv, "=")
		switch {
		case name == "HOME" || name == "CLAUDE_CONFIG_DIR" || postMergeGitHubEnv[name]:
			if name == "CLAUDE_CONFIG_DIR" {
				ambientConfig = value
			}
			continue
		}
		for _, a := range postMergeAuthEnv {
			if name == a && value != "" {
				hasAuth = true
			}
		}
		out = append(out, kv)
	}

	configDir := filepath.Join(home, ".claude")
	if !hasAuth {
		src := ambientConfig
		if src == "" {
			for _, kv := range base {
				if v, ok := strings.CutPrefix(kv, "HOME="); ok && v != "" {
					src = filepath.Join(v, ".claude")
				}
			}
		}
		copyPostMergeCredentials(filepath.Join(src, postMergeCredentialsFile), filepath.Join(configDir, postMergeCredentialsFile))
	}
	return append(out, "HOME="+home, "CLAUDE_CONFIG_DIR="+configDir)
}

// copyPostMergeCredentials copies the CLI login file when it exists; a missing
// file is not an error (macOS keeps the login in the keychain).
func copyPostMergeCredentials(src, dst string) {
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(dst, data, 0o600)
}

// logPostMergeDenials logs one Warn line per call the hook denied.
func logPostMergeDenials(log *slog.Logger, denyLog string, pr int) int {
	f, err := os.Open(denyLog)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		tool, path, _ := strings.Cut(sc.Text(), "\t")
		if tool == "" {
			continue
		}
		n++
		log.Warn("post-merge review: tool call denied outside the checkout",
			slog.Int("pr", pr), slog.String("tool", tool), slog.String("path", path))
	}
	return n
}
