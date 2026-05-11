# TASK-41: Declined Pipeline Follow-Up (P3 cleanup)

**Status**: 🚧 In Progress
**Created**: 2026-05-07
**Assignee**: Pilot

---

## Context

PR #2767 (salvage of ghost-closed GH-2753/GH-2754) ships the core DECLINED pipeline (parseDeclinedReason, retry prompt, Result fields, dispatcher wiring, GitHub label notifier). Three small loose ends from P3 didn't make it into the salvage and are tracked here.

Plan ref: `~/.claude/plans/use-nav-research-prepare-delegated-dragon.md` (P3 of 5, cleanup)

## Success Criteria (single AC list — DO NOT decompose)

- [ ] Poller skip-on-label: in `internal/adapters/github/poller.go`, add `pilot-needs-clarification` to the same blocking-label set used for `pilot-failed`. Cover both `checkForNewIssues` and `findOldestUnprocessedIssue` (parallel + sequential modes). Mirror the existing pattern exactly.
- [ ] Test: `TestPoller_SkipsNeedsClarification` in `internal/adapters/github/poller_test.go`, mirroring whatever test covers `pilot-failed`.
- [ ] Dashboard: in `internal/dashboard/...`, add a `declined` counter alongside `completed`/`failed`, sourced from `executions.status='declined'`. Find the existing counter wiring and extend it minimally — no UI redesign.
- [ ] Docs: one paragraph appended to `docs/content/features/...` (the relevant features page; pick the same page the original retry/approval flow is documented in) describing the new DECLINED behaviour and the `pilot-needs-clarification` label.
- [ ] `make test`, `make lint`, `make build` pass.

---

## Out of Scope

- Refactoring anything PR #2767 already shipped
- Changing the DECLINED parser, retry prompt, or label name
- Slack/Telegram notifications for declines (separate Pilot issue if needed)
- Auto-clarification follow-up flow

---

## Implementation Plan

Three small additions, sequential. ~50 LoC total.

1. **Poller guard.** Search `pilot-failed` in `internal/adapters/github/poller.go`. Add `pilot-needs-clarification` to the same set. Mirror tests.
2. **Dashboard counter.** Find the `completed`/`failed` aggregation. Add `declined`. Render alongside.
3. **Docs.** One paragraph. Cross-link from the approval / retry section on the same page.

No changes to runner.go, dispatcher.go, or any package PR #2767 already touched.

---

## Verify

```bash
make test ./internal/adapters/github/... ./internal/dashboard/...
make lint
make build

# After deploy: file an intentionally ambiguous Pilot issue.
# Expect: Sonnet returns DECLINED, label applied, poller skips on next tick.
```

---

## Notes

- ~50 LoC, single PR, deliberately small to avoid decomposition triggering again.
- DO NOT split into subtasks. The three changes are independent but tiny — keep as one commit.
- Master plan: `~/.claude/plans/use-nav-research-prepare-delegated-dragon.md`

---

**Last Updated**: 2026-05-07
