# Operational Issues & Mitigations

Long-running issues observed in production, with current mitigations and
open questions. Keep entries short — file is read by incident responders.

## Claude Code Subprocess OOM-Killed on Long Runs (GH-2332)

**Symptom**: Claude Code child process exits with code 137 (SIGKILL).
Failure appears as `oom_killed: Process killed by SIGKILL (exit code 137)`
in `execution_logs` after 5–15 minutes on COMPLEX/EPIC tasks running
Opus 4.7 with Navigator context injected.

**Observed contributing factors**:
- Heavy Navigator prompt (project README + SOPs + memories +
  knowledge-graph learnings) drives cache-creation tokens past 20K per
  turn on Opus 4.7.
- Long sessions (100+ tool calls) accumulate state inside the `claude`
  CLI that Pilot cannot see.
- Pilot's own stderr capture was previously unbounded — see mitigation 1.

### Mitigations (shipped)

1. **Bounded stderr buffer** (`internal/executor/bounded_buffer.go`):
   capped at 1 MiB with tail truncation. Prevents Pilot itself from
   drifting into OOM territory while the child process runs.
2. **Distinct `oom_killed` alert type**
   (`AlertEventTypeOOMKilled`): OOM kills no longer hide behind the
   generic `task_failed` bucket, so dashboards and alert rules can
   target them.
3. **Escape hatch config** `claude_code.disable_navigator_for_epic:
   true`: when set, COMPLEX/EPIC tasks fall back to the lean
   non-Navigator prompt (no README / SOPs / knowledge graph).
   Default is `false` — only turn on if OOM kills are recurrent.

### Diagnosis steps

1. `SELECT task_id, error_type, duration FROM execution_logs WHERE
   error_type = 'oom_killed' ORDER BY started_at DESC LIMIT 20;`
2. Inspect Pilot RSS during the failing run:
   `top -pid $(pgrep -f 'pilot start')` — if Pilot itself is growing
   past ~1 GiB, the buffer cap is not applying (check the build).
3. Inspect child `claude` process:
   `top -pid $(pgrep -f 'claude -p')` — if the CLI itself climbs past
   ~4 GiB, the root cause is inside Claude Code. File upstream.

### When to toggle the escape hatch

Enable `claude_code.disable_navigator_for_epic: true` when:
- Two or more OOM kills hit COMPLEX/EPIC tasks in the same 24h window.
- Host memory can't be expanded further.
- The Navigator context is not load-bearing for the failing tasks (i.e.
  they fail on big refactors that don't benefit from project README).

Leave it off by default — Navigator context materially improves success
rate on smaller tasks.

## v2.126–v2.128.2 Hand-Tag Streak (5 consecutive releases)

**Symptom**: Auto-release did not fire after merge for v2.126.0, v2.127.0,
v2.128.0, v2.128.1, and v2.128.2. Each release required manual
`git tag vX.Y.Z && git push origin vX.Y.Z`.

**Root cause**: `approval_pending` rows were inserted with a zero
`CreatedAt` timestamp. On daemon restart, `Rehydrate` pruned those rows
(treated as expired), leaving the in-memory pending map empty. When the
Telegram user tapped Approve, the handler returned "request not found" and
the decision was lost — the approval gate never resolved, so the autopilot
release step stalled.

**Fix**: PR #2694 (shipped in v2.128.0) sets a default `CreatedAt = NOW()`
and freezes the value on UPSERT so it is never overwritten by a zero.

**Why v2.128.0/1/2 still needed hand-tagging after the fix**: classic
hot-upgrade bootstrap. The running daemon predated the fix; until it
self-upgraded and restarted with the new binary, newly inserted rows still
used the old (zero-timestamp) code path. Once the upgraded daemon was live,
the first subsequent approval-gated PR should auto-release without
intervention.

**Latent gaps tracked separately**:
- Gap 1 — Releaser init from env-scoped config: GH-2716 (fix in v2.128.3)
- Gap 2 — Blocking post-merge CI: GH-2717 (fix in v2.128.3)

**Smoke-test**: GH-2718 (this doc change) is the third approval-gated PR
after v2.128.2. Its merge verifies:
1. `executions.approval_decision` columns now populate (TASK-33 / #2694).
2. Auto-release fires for v2.129.0 without hand-tagging.

Post-merge, confirm with:
```sql
SELECT task_id, approval_request_id, approval_decision, approval_decision_by
FROM executions WHERE task_id = 'GH-2718';
-- Expect: all four columns populated
```
Then check whether v2.129.0 (or next) tagged automatically.

See also: memory pattern `bug_telegram_approval_callback_unwired.md`
(resolved v2.121.0→v2.126.0) and memory entry
`pattern_approval_chat_id_bootstrap.md`.
