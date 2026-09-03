# TASK-491: Executor env scrub + destructive-command hook on by default

✅ SHIPPED 2026-09-03 (same day) → [pilot#5275](https://github.com/qf-studio/pilot/issues/5275) decomposed into 10 children, **9 PRs merged 10:03–12:11Z** (#5288–#5296). Helper = `internal/executor/model_env.go` (`modelSubprocessEnv`, landed via #5278→PR#5289 after #5276's PR#5286 closed on lint); all 7 spawn sites wired; AST regression test `TestModelSpawnSitesUseScrubHelper`; hooks default flipped (`hooks.go:65`); config `claude_code.env_passthrough`; docs. **Post-merge review DONE 2026-09-03 — APPROVE-w-defects** (verdict on #5275; 4/4 mutations killed in an isolated worktree). Defects → follow-up [pilot#5302](https://github.com/qf-studio/pilot/issues/5302): `env_passthrough` parsed+documented but `SetModelEnvPassthrough` has ZERO callers (no-op, TASK-460 class) · OpenCode server now loses non-Anthropic provider keys (`OPENROUTER_API_KEY` etc.) with no working escape hatch · Qwen backend silently excluded from scrub + AST guard. **Review owed on #5302's PR.** Cleanup: #5276 + orphan autopilot-fix #5287 closed manually; the repick loop they caused → daemon bug [pilot#5297](https://github.com/qf-studio/pilot/issues/5297).

## Problem

Every model-invoking subprocess inherits the daemon's full environment (`backend_claudecode.go:638`, `epic.go:280`, plus 8 sites with `cmd.Env` nil). Config secrets reach the env via `os.ExpandEnv` (`config.go:801`): Telegram/Slack/Discord/Linear/Plane/GitLab/Azure tokens, provider keys, webhook secrets, gateway token. With `--dangerously-skip-permissions` and `Bash` allowed, an injected issue body exfiltrates them into a PR diff. Second gap: `DefaultHooksConfig()` has `Enabled: false` (`hooks.go:57`), so the Bash guard is off on stock installs.

Origin: external security question 2026-09-02 (Arden) — the honest answer is "the process is the boundary", which makes the process env the surface.

## Decisions

- **Denylist, not allowlist** — Claude Code needs HOME/PATH/TMPDIR/locale/SSH_AUTH_SOCK/XDG_*; an allowlist would break it. Suffix rule (`_TOKEN`, `_SECRET`, `_API_KEY`, `_PAT`, `_PASSWORD`, `AWS_SECRET_*`, `AWS_SESSION_*`) + explicit names.
- **GITHUB_TOKEN / GH_TOKEN stay** — the model's read-only `gh` via the ghguard shim relies on the ambient token (`gh_credentials.go:87-90` injects only on Pilot's own commands, and only in App mode). Hiding a token from a same-UID process is theater; the mitigation is scope (App installation token). Follow-up: TASK-461 operator step — confirm the founder box is on the App token, not a PAT.
- **ANTHROPIC_* / CLAUDE_* keep-list** wins over the suffix rule.
- **Escape hatch** `claude_code.env_passthrough: []` for repos whose tests read a `*_API_KEY`.
- **One helper, every site** + a regression guard (test or CI grep) so a new spawn site cannot bypass it.
- **Hooks on by default** is a speed bump (pattern list, bypassable), not a boundary. Merge path (`hooks.go:217`) already preserves user-owned `.claude/settings.json` hooks — acceptance pins that.

## Not in scope

- Sandboxing the executor (per-tenant VM is the hosted boundary; self-hosters own their host).
- Pre-push secret scanning (GitHub push protection covers it).
- Hardening the intent judge (advisory by design; CI + approval is the merge gate).

## Refs

- Research 2026-09-03 (nav-research, partial — agent cut off by API safeguard; remainder filled by direct grep): spawn-site table, credential path, config expansion, hooks defaults, test names.
- Sibling controls untouched: ghguard policy (`ghguard/policy.go`), repo guardrail, autopilot merge gate.
- Reply to Arden drafted 2026-09-02 (session), not yet sent.
