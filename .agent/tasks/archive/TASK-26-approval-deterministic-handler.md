# TASK-26: Deterministic approval handler selection via approval_source

**Status**: ✅ Completed
**Created**: 2026-05-05
**Completed**: 2026-05-05 (PR #2640, shipped in v2.118.2)
**Assignee**: Pilot

---

## Context

**Problem**:
`internal/approval/manager.go:148-153` selects the approval channel via Go map iteration with `break` after the first hit. Map iteration order is non-deterministic in Go, so with Telegram + Slack both registered the handler that fires is random per process. The autopilot config key `orchestrator.autopilot.approval_source` (`internal/autopilot/types.go:23-31`) is defined and documented in user memory (`reference_approval_telegram_wiring.md`) but is **never consulted by the Manager** — it is a no-op today.

**Goal**:
Make handler selection deterministic by routing the autopilot's `approval_source` config through to the Manager via the existing `approval.Request` boundary, with graceful fallback to the existing first-available behavior when the preferred channel isn't registered.

**Success Criteria**:
- [ ] With `approval_source: telegram` and Slack also registered, every approval send goes to Telegram across restarts.
- [ ] With `approval_source` unset, behavior matches today (first-available; tests still pass).
- [ ] When `approval_source: slack` is set but the Slack handler is not registered, autopilot logs a WARN and falls back to first-available — does not deadlock.

---

## Implementation Plan

### Phase 1: Plumb PreferredChannel through Request
**Goal**: Carry channel preference from autopilot caller to approval Manager without changing constructor signatures.

**Tasks**:
- [ ] Add `PreferredChannel string` (omitempty) field to `approval.Request`.
- [ ] In `auto_merger.go:requestApproval`, set `req.PreferredChannel = string(m.cfg.ApprovalSource)` when building the Request.

**Files**:
- `internal/approval/types.go:33-43` — add field to `Request` struct.
- `internal/autopilot/auto_merger.go:208-218` — set the field when constructing the Request.

### Phase 2: Deterministic lookup in Manager
**Goal**: Use the new field to pick the handler, with graceful fallback.

**Tasks**:
- [ ] In `manager.go:147-154`, when `req.PreferredChannel != ""`, look up `m.handlers[req.PreferredChannel]`.
- [ ] On miss, emit a WARN log (`"preferred approval channel %q not registered, falling back to first-available"`) and fall through to existing loop.
- [ ] On hit, use that handler directly; skip the fallback loop.

**Files**:
- `internal/approval/manager.go:147-154` — preferred-channel lookup before existing loop.

### Phase 3: Tests
**Goal**: Lock in the new behavior, document the fallback path.

**Tasks**:
- [ ] Update `internal/approval/manager_test.go:725-756` (existing multi-handler test) — assert that when `PreferredChannel` is set, the named handler receives the request and others do NOT.
- [ ] Keep the existing first-available assertion for the path where `PreferredChannel` is unset (rename test to clarify scope).
- [ ] Add a fallback-warning test: `PreferredChannel: "slack"` but only Telegram registered → warning logged, Telegram receives request, no error.

**Files**:
- `internal/approval/manager_test.go:725-756` — update + extend.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where to plumb `approval_source` | (a) into `approval.Config` via `NewManager(cfg, source)`; (b) via `Request.PreferredChannel`; (c) read autopilot config inside Manager | (b) Request.PreferredChannel | Keeps autopilot-specific concern in the autopilot package; no constructor signature change; Request is already caller-built; per-request override possible if ever needed. |
| Fallback on miss | (a) hard fail; (b) graceful WARN + first-available | (b) graceful | Avoids deadlock when config drifts (e.g. operator sets `approval_source: github-review` before wiring the GitHub handler). Deadlocks are the single worst failure mode for this surface area — see incident_oauth_cascade_series. |
| Default when `approval_source` unset | (a) error; (b) preserve first-available | (b) preserve | Backward compatibility for configs without `approval_source` set. |

---

## Dependencies

**Requires**:
- [ ] None — independent change.

**Blocks**:
- [ ] TASK-27 (per-channel Telegram approval flag) — only meaningful if handler selection is deterministic; otherwise a Telegram-disable can be silently overridden by map iteration.
- [ ] TASK-28 (wire GitHub PR-review handler) — must land after this so `approval_source: github-review` actually routes correctly when a third handler is present.

---

## Verify

```bash
# Unit tests
go test ./internal/approval/... -run TestManager -v

# Full approval package
go test ./internal/approval/...

# Vet + lint
go vet ./internal/approval/... ./internal/autopilot/...
golangci-lint run ./internal/approval/... ./internal/autopilot/...
```

---

## Done

- [ ] `approval.Request.PreferredChannel` field exists, omitempty-tagged.
- [ ] `manager.go` consults `PreferredChannel` before falling through; WARN log on miss.
- [ ] `auto_merger.go:requestApproval` sets the field from `m.cfg.ApprovalSource`.
- [ ] Unit tests cover: deterministic match, fallback-warning, no-preference (legacy first-available).
- [ ] All existing approval tests still pass.
- [ ] No new config keys; no constructor signature changes.

---

## Notes

- `manager_test.go:747` has an existing comment "implementation uses first available" and asserts `totalSent == 1`. That assertion stays valid for the no-preference path; new test covers the with-preference path.
- This task does not change `approval.NewManager` signature. Plumbing flows entirely through `Request`.
- Memory `reference_approval_telegram_wiring.md` claims `approval_source` controls routing — that documentation is currently aspirational; this task makes it true.

---

## Completion Checklist

- [ ] Implementation finished
- [ ] Tests written and passing
- [ ] No regressions in existing tests
- [ ] PR opened with conventional title `fix(approval): deterministic handler selection via approval_source`
- [ ] Linked to GitHub issue (filled in below after issue creation)

---

**GitHub Issue**: [#2638](https://github.com/qf-studio/pilot/issues/2638)
**Last Updated**: 2026-05-05
