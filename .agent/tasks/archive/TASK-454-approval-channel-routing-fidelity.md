# fix(approval): channel-routing fidelity — rehydrate/sweep channel scoping, Slack destination parity, github-review alias

## Problem

Four defects in `internal/approval` make running Telegram AND Slack approval handlers simultaneously unsafe. Both can already be registered at once (independent `if` blocks, `cmd/pilot/main.go:1865-1904`), and per-project channel routing (roadmap `.agent/system/approval-architecture-roadmap.md`, leg B3) will make dual-channel the steady state — these must land first.

## Context (verified 2026-08-06, origin/main)

- (a) **Cross-channel rehydrate leak**: `TelegramHandler.Rehydrate` (`internal/approval/telegram.go:200-236`) iterates ALL `approval_pending` rows and re-posts a prompt for each — including rows originally dispatched to Slack. `PreferredChannel` is read into the Request (`telegram.go:213`) but never used to skip. `SlackHandler.Rehydrate` (`slack.go:126-166`) has the same missing filter (it re-arms rather than re-posts, so the symptom is milder).
- (b) **Slack destination asymmetry**: `SlackHandler.SendApprovalRequest` uses `h.channel` unconditionally (`slack.go:234-245`), ignoring `req.Approvers`, while its own `Rehydrate` honors `req.Approvers[0]` as the channel (`slack.go:153-155`). First send and post-restart rehydrate can target different destinations for the same request.
- (c) **`github-review` alias hard-error**: `ApprovalSourceGitHubReview = "github-review"` (`internal/autopilot/types.go:36`) but `GitHubHandler.Name()` returns `"github"` (`internal/approval/github.go:62-64`). `approval_source: github-review` therefore ALWAYS hits the `preferredMissing` hard error (`manager.go:271-274`). No alias map exists.
- (e) **Channel-blind expiry sweep**: `PrunePendingApprovals` is `DELETE ... WHERE expires_at < ?` with no channel predicate — Slack's sweep deletes Telegram's expired rows before Telegram can edit its message (and writes the timeout decision), and vice versa. Latent with one live channel; steady-state breakage under dual-channel.
- Rows persist `preferred_channel` (`internal/memory/approval_store.go`, DDL `store.go:341-352`) — the filter key already exists.
- Preserve GH-4380 semantics: preferred-channel lookup stays a **hard error** when the named handler is unregistered — never a silent fallback (`manager.go:264-274`).

## Acceptance

1. Both `Rehydrate`s process only rows with `PreferredChannel == h.Name()` (after (c)'s normalization). Legacy rows with empty `preferred_channel`: claimed by exactly one handler — the one matching the configured global `approval_source` default — rule documented in code.
2. Slack `SendApprovalRequest` resolves its destination the same way its `Rehydrate` does (`Approvers[0]` when present, else `h.channel`).
3. Channel-name normalization in ONE place in `internal/approval` (registration and/or lookup): `"github-review"` resolves to the `github` handler; existing configs keep working; the autopilot constant is unchanged.
4. `PrunePendingApprovals` (and each handler's sweep call) is channel-scoped: a handler only deletes/decides rows it owns. No expired row becomes unprunable — rows whose channel matches no registered handler are swept by a documented fallback (simplest correct rule; the Manager-level orphan sweep is roadmap leg B4, do not build it here beyond keeping rows collectable).
5. Tests: seeded mixed-channel table (one telegram + one slack + one legacy-empty + one unknown-channel row) → each Rehydrate touches exactly its own rows; sweep isolation; alias resolution; Slack first-send/rehydrate destination parity; `-race` clean.
6. `make build` / `make test` / `make lint` green. No behavior change for single-channel deployments beyond the fixes above.

## Scope fence

All changes confined to `internal/approval` + `internal/memory/approval_store.go` · no controller/config surface · no new channels (console channel is roadmap B4) · no changes to Manager's hard-error lookup semantics · dead stages (`pre_execution`/`post_failure`) untouched.

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

## Refs

- Roadmap: `.agent/system/approval-architecture-roadmap.md` (leg B1 — hard prerequisite of B3)
- Research pass 2026-08-06 (approval stage/channel map, file:line-grounded)

- **Dispatched**: https://github.com/qf-studio/pilot/issues/4772
