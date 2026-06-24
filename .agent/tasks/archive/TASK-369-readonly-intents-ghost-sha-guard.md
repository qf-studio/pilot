# TASK-369: Read-only comms intents bypass the ghost-SHA commit guard

**Status**: ✅ Shipped — v2.192.1 (PR #3643, commit 3465ba6f)
**Created**: 2026-06-24
**Assignee**: Pilot

---

## Context

**Problem**:
Read-only conversational intents (`question`, `research`, `chat`) sent through the
shared comms handler are **silently dropped** when the daemon runs with
`UseWorktree` enabled (the production/autopilot config). The user sees the
"🔍 Looking into that..." placeholder and then nothing — the answer Claude Code
generated is discarded.

Confirmed live on 2026-06-24 via Slack: `@Pilot what is in the queue?` →
task `Q-1782293200` → Claude ran for 56s, exited cleanly → executor failed with
`"no new commit produced — worktree HEAD matches base branch parent"`.

**Root cause**:
1. `internal/comms/handler.go` — `handleQuestion` (~L258), `handleResearch` (~L290),
   and `handleChat` (~L400) build their `executor.Task` **without** setting
   `LocalMode`. `handleQuestion` does not even set `CreatePR: false`.
2. `internal/executor/runner.go:2609-2633` — the ghost-SHA guard (GH-3126) runs
   unconditionally. For a read-only task Claude makes no commit, so the harvested
   SHA equals the parent (already on base) → the guard flips
   `result.Success = false` and sets `result.Error = "no new commit produced …"`.
   There is **no exemption** for read-only / `LocalMode` tasks.

Because the guard only triggers when a worktree/commit is expected, the bug is
invisible in non-worktree configs but breaks every question/research/chat reply
in worktree-enabled (production-like) configs.

**Goal**:
Read-only intents return their answer to the user regardless of whether a commit
was produced. A task that legitimately produces no commit must not be treated as
a failure when it is read-only.

---

## Acceptance Criteria

- [ ] `handleQuestion`, `handleResearch`, `handleChat` mark their task as read-only
      (set `LocalMode: true`, keeping `CreatePR: false`).
- [ ] The ghost-SHA guard at `runner.go:2623` is skipped for read-only/`LocalMode`
      tasks (add `!task.LocalMode` to the condition) so "no commit" is not a failure.
- [ ] A read-only task that produces no commit returns `Success: true` with
      `result.Output` intact (answer preserved).
- [ ] The ghost-SHA guard still fires for normal PR/commit tasks (no regression to
      GH-3126: a no-op PR task is still rejected).
- [ ] Slack/Telegram: `@Pilot <question>` returns an answer in the channel instead
      of silence/error.

---

## Implementation

### Phase 1: Mark read-only comms tasks as LocalMode
**Goal**: Tell the executor these tasks have no PR/commit expectation.

**Tasks**:
- [ ] In `internal/comms/handler.go`, set `LocalMode: true` on the `executor.Task`
      built in `handleQuestion`, `handleResearch`, and `handleChat`.
- [ ] Ensure `CreatePR: false` is explicit on all three (handleQuestion currently omits it).

**Files**:
- `internal/comms/handler.go` — read-only intent handlers.

### Phase 2: Exempt read-only tasks from the ghost-SHA guard
**Goal**: A read-only run with no commit is a success, not a failure.

**Tasks**:
- [ ] In `internal/executor/runner.go` (~L2609-2633), add `!task.LocalMode` to the
      ghost-SHA rejection branch so the "no new commit produced" failure is not set
      for read-only/LocalMode tasks.
- [ ] Confirm the non-LocalMode path is byte-for-byte unchanged (GH-3126 intact).

**Files**:
- `internal/executor/runner.go` — ghost-SHA guard.

### Phase 3: Regression tests
**Goal**: Lock the behavior in both directions.

**Tasks**:
- [ ] Table-driven test: `LocalMode` task whose harvested SHA == base →
      `Success=true`, `Output` preserved, `Error==""`.
- [ ] Negative test: non-LocalMode PR task whose harvested SHA == base → still
      rejected with "no new commit produced" (GH-3126 guard preserved).
- [ ] (If feasible) comms-handler test that `handleQuestion` builds a task with
      `LocalMode=true` / `CreatePR=false`.

**Files**:
- `internal/executor/runner_test.go`
- `internal/comms/handler_test.go`

---

## Out of Scope

- DM / `message.im` support for Slack (separate task — P0 feature gap).
- Vague-task push-back / pre-flight `IntentJudge` on the comms path (separate P1).
- Operational/meta questions about Pilot's own state ("what is in the queue?")
  answering from daemon state instead of a codebase scan (separate P1 — this task
  only fixes the silent-drop; it does not make the question route correctly).
- Wiring the LLM intent classifier for Slack (separate P2).

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| How to flag read-only tasks | New `ReadOnly` field; reuse `LocalMode` | Reuse `LocalMode` | Already means "problem-solving prompt without PR/commit constraints" (GH-2103); the runner already branches on it for git-clean/Navigator/timeout. No new field/plumbing. |
| Where to fix the guard | Only flag handlers; only guard; both | Both | Flagging alone is insufficient — the guard at L2623 has no `LocalMode` check, so it would still fail. Guard-only would leak Navigator/PR prompt behavior into read-only runs. |

---

## Verify

```bash
make build
go test ./internal/executor/... -run GhostSHA -v
go test ./internal/comms/... -run Question -v
go test ./...
make lint
```

Manual: restart daemon with `--slack --github`, send `@Pilot <a real codebase
question>` in the allowed channel, confirm an answer returns (not silence).

---

## Done

- [ ] `LocalMode: true` set on the three read-only comms handlers.
- [ ] Ghost-SHA guard skipped when `task.LocalMode` is true.
- [ ] New regression tests pass (read-only no-commit = success; PR no-commit = fail).
- [ ] `go test ./...` and `make lint` green.
- [ ] Manual Slack question returns an answer.

---

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/3642 (`pilot` + `no-decompose`, single worker)
- Superseded attempt: #3640 (epic) / #3641 (child) — closed; over-decomposed + OOM-killed during a concurrent-daemon race.
- Diagnosed live 2026-06-24 (Slack `#engineering`, daemon `--slack --github`, stage).
- Ghost-SHA guard: `internal/executor/runner.go:2609-2633` (GH-3126).
- `LocalMode` semantics: `internal/executor/runner.go:301-304` (GH-2103).
- Read-only handlers: `internal/comms/handler.go:244-420`.

---

**Last Updated**: 2026-06-24
