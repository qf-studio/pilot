# TASK-28: Wire GitHub PR-review approval handler

**Status**: ✅ Completed
**Created**: 2026-05-05
**Completed**: 2026-05-05 (PR #2646, shipped in v2.118.3)
**Assignee**: Pilot

---

## Context

**Problem**:
The approval package already has a complete GitHub handler implementation in `internal/approval/github.go`:
- `NewGitHubHandler(client GitHubReviewClient, cfg *GitHubHandlerConfig) *GitHubHandler` (line 45)
- `GitHubReviewClient` interface with `HasApprovalReview(ctx, owner, repo, number)` (lines 14-16)
- Polling loop in `pollForApproval` (line 106)
- Message formatter
- Tests in `internal/approval/github_test.go`

But **there is no registration call site** in `cmd/pilot/main.go`. The block at lines 418-440 (gateway) and 1299-1323 (start) only registers Telegram and Slack. Operators who want to use GitHub PR review (Approve/Request Changes on the PR) as the approval mechanism cannot — even though the entire handler is sitting in the codebase ready to use.

**Goal**:
Wire `NewGitHubHandler` into both registration sites with an opt-in config flag (`Adapters.GitHub.Approval.Enabled`, default `false`). Once enabled, operators can leave a PR review as the approval signal — no chat clients required, no DM gymnastics, leverages the same UI used for normal code review.

**Success Criteria**:
- [ ] Setting `adapters.github.approval.enabled: true` registers the GitHub handler at startup.
- [ ] Default `Enabled: false` — zero impact for current users.
- [ ] When registered alongside Telegram/Slack with `approval_source: github-review` (depends on TASK-26), GitHub handler is selected deterministically.
- [ ] No new wrapper adapter — `*github.Client` satisfies `GitHubReviewClient` directly via `HasApprovalReview` (`internal/adapters/github/client.go:763`).

---

## Implementation Plan

### Phase 1: Config struct
**Goal**: Add an opt-in config field, parallel to Slack and (post-TASK-27) Telegram.

**Tasks**:
- [ ] Add `ApprovalConfig{Enabled bool, PollInterval time.Duration}` struct in `internal/adapters/github/` (new file `approval_config.go`, or alongside existing config — match the Slack location pattern).
- [ ] Default constructor: `Enabled: false, PollInterval: 30 * time.Second`.
- [ ] Add `Approval *ApprovalConfig` field to the GitHub adapter Config struct in `internal/config/config.go` (mirror `Slack.Approval`).

**Files**:
- `internal/adapters/github/approval_config.go` (new) — struct + default constructor.
- `internal/config/config.go` — add `Approval *github.ApprovalConfig` field on GitHub adapter struct.

### Phase 2: Wire the registration sites
**Goal**: Register `NewGitHubHandler` on both gateway and start paths when enabled.

**Tasks**:
- [ ] Inspect `cmd/pilot/main.go:418-440` (gateway) — locate the existing `ghClient := github.NewClient(ghToken)` instantiation (current approx line 453, *after* approval block). Either:
  - **Option A**: Move the GitHub handler registration to live inside the autopilot block where `ghClient` and the owner/repo split already exist. Cleanest, avoids duplicate client construction.
  - **Option B**: Hoist `ghClient` + `parts := strings.Split(cfg.Adapters.GitHub.Repo, "/")` above the approval block. More refactor-y.
  - Choose Option A (less churn, single client instance).
- [ ] Same treatment for `cmd/pilot/main.go:1299-1323` (start path).
- [ ] Registration code:
  ```go
  if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Approval != nil && cfg.Adapters.GitHub.Approval.Enabled {
      pollInterval := cfg.Adapters.GitHub.Approval.PollInterval
      if pollInterval == 0 {
          pollInterval = 30 * time.Second
      }
      ghHandler := approval.NewGitHubHandler(ghClient, &approval.GitHubHandlerConfig{
          Owner:        owner,
          Repo:         repo,
          PollInterval: pollInterval,
      })
      approvalMgr.RegisterHandler(ghHandler)
  }
  ```

**Files**:
- `cmd/pilot/main.go:418-440` — gateway-mode registration.
- `cmd/pilot/main.go:1299-1323` — start-mode registration.

### Phase 3: Test
**Goal**: Lock in default config + handler construction.

**Tasks**:
- [ ] In `internal/approval/github_test.go`, ensure `NewGitHubHandler` is exercised with a stub `GitHubReviewClient` and verify the handler's `Name()` returns the expected string for Manager lookup (alignment with TASK-26's `PreferredChannel` lookup key).
- [ ] Add a config-default test asserting `github.DefaultApprovalConfig().Enabled == false` and `PollInterval == 30s`.
- [ ] If a `cmd/pilot/main_test.go` integration smoke exists, extend it; otherwise skip — wiring is config-shape work and the unit-test surface is already covered.

**Files**:
- `internal/approval/github_test.go` — assert handler `Name()` and `NewGitHubHandler` happy path (extend existing tests if present).
- `internal/adapters/github/approval_config_test.go` (new) — default-constructor assertions.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Default for `Approval.Enabled` | (a) true (opt-out); (b) false (opt-in) | (b) false | Slack uses opt-in (`Slack.Approval.Enabled` defaults effectively to false unless set). Telegram (TASK-27) defaults true for back-compat because it was always-on before. GitHub has *never* been wired — no back-compat to preserve, opt-in is correct. |
| Where to register in main.go | (a) standalone block above autopilot; (b) inside autopilot block where `ghClient` exists | (b) inside autopilot block | Avoids constructing a second `ghClient`; keeps GitHub-specific concerns colocated with autopilot setup; less diff churn. |
| Need for adapter wrapper | (a) wrapper struct mirroring `telegramApprovalAdapter`; (b) use `*github.Client` directly | (b) direct | `*github.Client` already satisfies `approval.GitHubReviewClient` via `HasApprovalReview` (`internal/adapters/github/client.go:763`). No interface mismatch, no wrapper required. Saves ~30 LoC vs Telegram/Slack patterns. |
| Default `PollInterval` | (a) 15s aggressive; (b) 30s balanced; (c) 60s conservative | (b) 30s | Matches existing GitHub poller default in `cfg.Adapters.GitHub.Polling.Interval`. Acceptable latency for an approval-required prod environment; adjustable via config. |

---

## Dependencies

**Requires**:
- [ ] **TASK-26 (#2638)** — deterministic handler selection. Without it, registering a third handler (alongside Telegram + Slack) makes the random map-iteration lottery strictly worse (1-in-3 instead of 1-in-2). With TASK-26, `approval_source: github-review` deterministically routes to this handler.

**Blocks**:
- [ ] None.

**Related**:
- TASK-27 (#2641) — Telegram per-adapter approval flag. Independent change but together they form a coherent matrix: each channel has its own enable/disable, and `approval_source` picks among the registered ones deterministically.

---

## Verify

```bash
# Test the new handler wiring
go test ./internal/approval/... -run TestGitHubHandler -v
go test ./internal/adapters/github/...

# Build + vet the registration changes
go build ./cmd/pilot/...
go vet ./cmd/pilot/... ./internal/approval/... ./internal/adapters/github/...
```

Manual smoke (after merge + hot-upgrade, requires TASK-26):
1. Set `adapters.github.approval.enabled: true` in `~/.pilot/config.yaml`.
2. Set `orchestrator.autopilot.approval_source: github-review`.
3. Optionally set `adapters.telegram.approval.enabled: false` (post-TASK-27) to avoid registering Telegram.
4. Trigger a stage PR. Expect: autopilot pauses at pre-merge, polls for an approving review on the PR.
5. Open the PR in GitHub UI, click **Approve** in the Review menu.
6. Within `PollInterval`, autopilot detects the review and proceeds to merge.

---

## Done

- [ ] `github.ApprovalConfig{Enabled bool, PollInterval time.Duration}` struct exists with default `Enabled: false, PollInterval: 30s`.
- [ ] `cfg.Adapters.GitHub.Approval` field plumbed in `internal/config/config.go`.
- [ ] Both registration sites (`cmd/pilot/main.go` gateway + start) call `approval.NewGitHubHandler` when enabled, using the existing `ghClient` (no duplicate client construction).
- [ ] Unit tests cover handler construction + default config values.
- [ ] No changes to `auto_merger.go:requestApproval` (verified `Metadata["pr_number"]` is already set at lines 213-217).
- [ ] No new dependencies; no wrapper adapter; no `approval.NewManager` signature change.

---

## Notes

- The handler is fully implemented in `internal/approval/github.go` — this task is purely wiring + config, no new approval logic.
- `auto_merger.go:requestApproval` (lines 208-218) already populates `req.Metadata["pr_number"]`. The GitHub handler reads this at `internal/approval/github.go:71-79` to know which PR to poll. No changes needed there.
- Compose TASK-26 + TASK-27 + this task and the final UX is: operator sets `approval_source: github-review`, handler is deterministic, no chat client needed, just review the PR.
- Possible follow-up (NOT in scope): allow per-environment approval source (`stage` → Slack, `prod` → GitHub-review). Would require threading `ApprovalSource` through `EnvironmentConfig`. File separately if desired.

---

## Completion Checklist

- [ ] Implementation finished
- [ ] Tests written and passing
- [ ] No regressions in existing approval or github adapter tests
- [ ] PR opened with conventional title `feat(approval): wire GitHub PR-review approval handler`
- [ ] Linked to GitHub issue (filled in below after issue creation)

---

**GitHub Issue**: [#2642](https://github.com/qf-studio/pilot/issues/2642)
**Last Updated**: 2026-05-05
