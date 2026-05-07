# TASK-27: Per-adapter approval-enabled config field for Telegram

**Status**: ✅ Completed
**Created**: 2026-05-05
**Completed**: 2026-05-05 (PR #2644, shipped in v2.119.0 — `feat:` triggered minor bump)
**Assignee**: Pilot

---

## Context

**Problem**:
The Telegram approval handler registers whenever `Adapters.Telegram.Enabled && BotToken != ""` (`cmd/pilot/main.go:421` gateway path, `cmd/pilot/main.go:1302` start path). There is no way to keep Telegram on for briefs, voice, status messages, alerts while routing **approvals** through Slack or GitHub. Slack already has this granularity via `Adapters.Slack.Approval.Enabled` (`internal/adapters/slack/webhook.go:231-235`, gated at `cmd/pilot/main.go:428-439`). Telegram's adapter config (`internal/adapters/telegram/notifier.go:13-23`) has no `Approval` substruct.

**Goal**:
Add a Slack-parallel `Adapters.Telegram.Approval.Enabled` config field so the operator can disable Telegram approval registration without disabling the adapter entirely. Default `Enabled: true` for back-compat — existing configs continue to behave identically.

**Success Criteria**:
- [ ] Setting `adapters.telegram.approval.enabled: false` skips Telegram approval handler registration on both gateway and start paths.
- [ ] Omitting `adapters.telegram.approval` (back-compat) registers the handler exactly as today.
- [ ] No unrelated `adapters.telegram.enabled` semantics change — the 30 read sites of that field are untouched.

---

## Implementation Plan

### Phase 1: Config struct
**Goal**: Mirror Slack's pattern in the Telegram adapter package.

**Tasks**:
- [ ] Add `ApprovalConfig{Enabled bool}` struct in `internal/adapters/telegram/notifier.go` (or a sibling file if cleaner — match Slack's location pattern).
- [ ] Add `Approval *ApprovalConfig` field to `telegram.Config`.
- [ ] Add `DefaultApprovalConfig() *ApprovalConfig` returning `&ApprovalConfig{Enabled: true}`.

**Files**:
- `internal/adapters/telegram/notifier.go:13-23` — extend `Config`, add `ApprovalConfig` + `DefaultApprovalConfig()`.

### Phase 2: Wire the gate
**Goal**: Use the new config at registration sites.

**Tasks**:
- [ ] Update `cmd/pilot/main.go:421` (gateway path): gate becomes
  `cfg.Adapters.Telegram.Enabled && cfg.Adapters.Telegram.BotToken != "" && (cfg.Adapters.Telegram.Approval == nil || cfg.Adapters.Telegram.Approval.Enabled)`.
  Nil-check preserves back-compat — configs without the substruct register the handler as before.
- [ ] Update `cmd/pilot/main.go:1302` (start path) identically.

**Files**:
- `cmd/pilot/main.go:421` — gateway-mode gate.
- `cmd/pilot/main.go:1302` — start-mode gate.

### Phase 3: Test
**Goal**: Lock in default behavior.

**Tasks**:
- [ ] Add a unit test in `internal/adapters/telegram/notifier_test.go` (or matching test file) asserting `DefaultApprovalConfig().Enabled == true`.
- [ ] No test required for the registration gate itself (config-shape change), but if a `_test.go` already covers Config defaults, extend it.

**Files**:
- `internal/adapters/telegram/notifier_test.go` (or adjacent) — default assertion.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Default for `Approval.Enabled` | (a) false (opt-in); (b) true (opt-out) | (b) true | Back-compat: existing configs without the substruct must continue to register the handler. Setting opt-in would silently break every prod operator on upgrade. |
| Where to put the struct | (a) `notifier.go` next to `Config`; (b) new `approval_config.go` file | (a) | Slack's `ApprovalConfig` lives next to its `Config` in `webhook.go`; mirroring keeps grep-discoverability. |
| Nil substruct semantics | (a) treat as disabled; (b) treat as enabled | (b) | Matches the "default true" decision. Nil pointer = use defaults = enabled. |

---

## Dependencies

**Requires**:
- [ ] **TASK-26 (#2638)** — deterministic handler selection. Without it, disabling Telegram approval may not actually route to Slack/GitHub deterministically because the Manager picks via Go map iteration; with only one handler registered the point is moot, but the use case for this task is "two handlers, prefer one" which only works after TASK-26.

**Blocks**:
- [ ] None directly. TASK-28 (GitHub PR-review handler) is independent but combines well with this once both land.

---

## Verify

```bash
# Test the new default
go test ./internal/adapters/telegram/... -run TestDefaultApprovalConfig -v

# Full adapter package
go test ./internal/adapters/telegram/...

# Build + vet
go build ./cmd/pilot/...
go vet ./internal/adapters/telegram/... ./cmd/pilot/...
```

Manual smoke (after merge + hot-upgrade):
1. Add `adapters.telegram.approval.enabled: false` to `~/.pilot/config.yaml`.
2. Ensure Slack approval is enabled with `approval_source: slack` (depends on TASK-26).
3. Trigger a pre-merge approval on a stage PR.
4. Expect Slack message, no Telegram message.
5. Remove the substruct (back-compat) → next approval lands on Telegram again.

---

## Done

- [ ] `telegram.ApprovalConfig` struct exists with `Enabled bool`.
- [ ] `telegram.Config.Approval *ApprovalConfig` field added.
- [ ] `DefaultApprovalConfig()` returns `Enabled: true`.
- [ ] Both registration sites (`main.go:421`, `main.go:1302`) gated on the new field with nil = back-compat = on.
- [ ] Unit test for default value passes.
- [ ] No regressions in existing telegram adapter tests.
- [ ] No changes to the 30 unrelated read sites of `Adapters.Telegram.Enabled`.

---

## Notes

- This is a config-shape addition. Operators with existing configs are unaffected (nil substruct preserves current behavior).
- Once landed, the user-facing knob is `adapters.telegram.approval.enabled: false` — same syntax as Slack.
- Wider plan: combined with TASK-26 (deterministic selection) and TASK-28 (GitHub PR-review handler), the operator gets a coherent matrix where each channel is independently switchable and `approval_source` deterministically picks among the registered ones.

---

## Completion Checklist

- [ ] Implementation finished
- [ ] Tests written and passing
- [ ] No regressions in existing tests
- [ ] PR opened with conventional title `feat(adapters/telegram): per-adapter approval-enabled config field`
- [ ] Linked to GitHub issue (filled in below after issue creation)

---

**GitHub Issue**: [#2641](https://github.com/qf-studio/pilot/issues/2641)
**Last Updated**: 2026-05-05
