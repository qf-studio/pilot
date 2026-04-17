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
