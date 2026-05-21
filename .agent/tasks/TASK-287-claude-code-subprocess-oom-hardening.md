# TASK-287: Harden Claude Code subprocess against OOM-kills

**Status**: in-progress (handed off to Pilot as [#3028](https://github.com/qf-studio/pilot/issues/3028))
**Priority**: P1 (recurring failure mode, masks real progress)
**Estimated Effort**: M (5-7 person-hours)
**Risk Level**: Low-Medium (resource limits + retry classification changes touch hot paths; mitigated by feature flag + tests)

## Problem

In a 24-hour window (2026-05-20 → 2026-05-21) the Claude Code subprocess
spawned by Pilot was SIGKILL'd by the kernel **3 times** with identical
signatures — `exit code 137`, classified `oom_killed` in
`internal/executor/backend_claudecode.go:101-114`.

| Date | Epic | Sub-issue | Duration before OOM | Notes |
|---|---|---|---|---|
| 2026-05-20 14:32 | GH-1  | sub-17 | 10m56s | First observed; recovered via dispatcher re-queue |
| 2026-05-21 14:14 | GH-21 | sub-42 | ~2m    | Recovered on retry |
| 2026-05-21 14:44 | GH-22 | sub-43 | 7m28s  | Retry succeeded in **33s** — confirms wasted run |

All three died **during the RESEARCH phase** (last `pilot-signal`
emitted before kill was `{"phase":"RESEARCH","progress":15}` with text
"Let me read the key schema and task files directly"). The sub-agent
over-reads files into its context window, the Claude Code process'
resident memory grows beyond available RAM, and the kernel kills it.

The 33-second retry success on GH-22 proves the work itself is small —
the **wasted 7m28s** is pure overhead. At 3 incidents/day this is
≥30 minutes/day of throughput lost plus the user-visible noise of
spurious failure alerts.

## Root Cause (verified by `Explore` agent, 2026-05-21)

1. **No memory limit on subprocess.** `internal/executor/backend_claudecode.go:373` spawns
   `exec.CommandContext(ctx, b.config.Command, args...)` with no
   `syscall.Rlimit`, no cgroup, no `--max-old-space-size`, no
   `GOMEMLIMIT`. Subprocess can grow until the OS kills it.

2. **No phase-aware timeout.** `internal/executor/model_routing.go:153-167`
   and `backend.go:429-444` set timeouts by complexity tier only
   (Trivial/Simple/Medium/Complex). RESEARCH-phase blow-ups inherit the
   full epic budget (1h) even though they should never need more than
   a few minutes.

3. **OOM is not in the smart-retry strategy.**
   `internal/executor/retry.go:125-214` (`Retrier.Evaluate`) handles
   `rate_limit | api_error | timeout | invalid_config`. The GH-22 retry
   succeeded only because an *outer* dispatcher loop re-queued the
   sub-issue; the smart retrier never saw the OOM. Retry timing is
   therefore non-deterministic and not measured.

4. **Zero memory telemetry.** `BackendResult` (`backend.go:167-213`)
   captures tokens, cost, file diffs — but no `RSS`, no `PeakRSS`.
   `ExecutionMetrics` (`memory/metrics.go:11-21`) has nothing memory-
   shaped. We're blind to the precondition that predicts the kill.

## Approach (one paragraph)

Three layers, all behind a feature flag (`executor.subprocess_limits.enabled`,
default `false` initially, flip to `true` after one bench cycle): (1)
Apply a configurable RSS cap to the Claude Code subprocess via
`syscall.Rlimit{RLIMIT_AS}` on Linux and a soft `prlimit`/`launchd`-shim
on macOS — when the cap is hit the process gets a clean OOM rather than
a kernel SIGKILL, *and* we know it was us, not the OS. (2) Add OOM to
`Retrier.Evaluate` with `MaxAttempts=2` and `BackoffJitter=10s` so the
auto-retry is owned by the retrier (deterministic, instrumented,
observable in the dashboard's sparkline). (3) Sample `/proc/<pid>/status`
(Linux) or `task_info()` (macOS) every 10s during execution and persist
`peak_rss_mb` + `final_rss_mb` to `executions` so we can graph
RAM-by-phase and tune the cap. RESEARCH-phase timeout is **out of scope**
for this task — fix the kill first, then tune phase budgets in a follow-up
once we have data.

## Files Touched

| File | Change | Why |
|---|---|---|
| `internal/executor/backend_claudecode.go:373` | Inject `applyResourceLimits(cmd, b.config.SubprocessLimits)` before `cmd.Start()` | Single chokepoint |
| `internal/executor/resource_limits_linux.go` *(new)* | `Rlimit{RLIMIT_AS, …}` via `cmd.SysProcAttr` | Linux path |
| `internal/executor/resource_limits_darwin.go` *(new)* | `prlimit`-style soft cap or `setrlimit` syscall | macOS path (best-effort; tests skip if unsupported) |
| `internal/executor/resource_limits_other.go` *(new)* | no-op + WARN log | Build-tag fallback |
| `internal/executor/rss_sampler.go` *(new)* | goroutine reading `/proc/<pid>/status` every 10s, returns `peak_rss_mb` on context cancel | Cross-platform via `gopsutil` if already vendored; else `/proc` only |
| `internal/executor/backend.go:167-213` | Add `PeakRSSMB`, `FinalRSSMB` to `BackendResult` | Telemetry |
| `internal/memory/metrics.go:11-21` | Add `PeakRSSMB`, `FinalRSSMB` to `ExecutionMetrics` | Persist |
| `internal/memory/store.go` | `executions` table migration: `peak_rss_mb INTEGER DEFAULT 0`, `final_rss_mb INTEGER DEFAULT 0`; backfill 0 for existing rows | DB schema |
| `internal/executor/retry.go:125-214` | Add `case ErrorTypeOOM` to `Evaluate`; `MaxAttempts=2`, `Backoff=10s`, log `attempt=N/2` | Move OOM retry into the smart retrier |
| `internal/executor/retry_test.go` | Table tests for `oom_killed → retry`, `oom_killed → giveup after 2`, `oom_killed → not retried when cap-disabled` | Coverage |
| `internal/config/config.go` | New `Executor.SubprocessLimits { Enabled bool; MaxRSSMB int; SampleIntervalSec int }` with safe defaults (`Enabled=false, MaxRSSMB=4096, SampleIntervalSec=10`) | Config surface |
| `configs/example.yaml` | Document the new block + tuning guidance | Discoverability |
| `internal/dashboard/tui.go` | Surface `peak_rss_mb` on the execution detail card (one line, right of token count) | User-visible observability |

## Implementation Steps

### Step 1 — Telemetry first (S, 1.5h)
Land RSS sampling + DB columns + dashboard surface **before** the cap,
so we (a) gather a baseline of normal-vs-doomed RSS curves and (b) can
verify the cap is working post-rollout.

- New `internal/executor/rss_sampler.go` with `StartRSSSampler(ctx, pid) <-chan struct{ Peak, Final int }`.
- Wire into `backend_claudecode.go` around `cmd.Wait()`.
- `BackendResult` + `ExecutionMetrics` + `executions` schema additions; migration script in `memory/migrations/`.
- Render `peak: 3.2 GB / final: 2.8 GB` on dashboard detail.

Acceptance: every run after deploy writes non-zero `peak_rss_mb`; existing
rows backfilled to 0; no behavior change.

### Step 2 — Smart-retry on OOM (S, 1h)
- `retry.go`: add `case ErrorTypeOOM` to `Evaluate`, return a `RetryStrategy{Attempts: 2, Backoff: 10*time.Second, Reason: "oom_killed"}`. Bounded by `state.smartRetryAttempt` so we can't loop forever.
- `retry_test.go`: tests above.
- Verify in dashboard that OOM retries now appear as `retry: 1/2 (oom_killed)` instead of mystery re-queues.

Acceptance: `make test` green; manually injected OOM (kill -9 the subprocess) retries exactly once and surfaces in the retry counter.

### Step 3 — Memory cap (M, 2h)
- `resource_limits_linux.go` / `_darwin.go` / `_other.go` with build tags.
- Linux: `cmd.SysProcAttr.Rlimit = &syscall.Rlimit{Cur: maxBytes, Max: maxBytes}` on `RLIMIT_AS`. Verify behavior with `stress-ng --vm 1 --vm-bytes 5g` smoke test.
- macOS: `setrlimit(RLIMIT_AS, …)` via cgo or shell out to `ulimit -v` wrapper. If support is flaky, log WARN once on startup and degrade to telemetry-only — **do not block this task on a perfect mac story**, RSS sampling alone is a 10× improvement.
- Behind `Executor.SubprocessLimits.Enabled` (default false).

Acceptance: with `enabled=true, max_rss_mb=512`, a deliberately memory-hungry test prompt returns `oom_killed` cleanly **without** triggering the kernel OOM-killer (verify via `dmesg` empty for our PIDs); with `enabled=false` behavior is byte-identical to today.

### Step 4 — Rollout (XS, 0.5h)
- Default-off for one bench cycle. Collect a week of `peak_rss_mb` data from the workshop project (it OOMs the most).
- Pick `max_rss_mb` at p99 of observed peaks × 1.5 (likely 6–8 GB based on the 3 incidents).
- Flip default to `true` in a follow-up commit. Document tuning in a new `.agent/sops/subprocess-oom-tuning.md`.

## Acceptance Criteria

- [ ] Every `executions` row written after Step 1 carries `peak_rss_mb > 0`.
- [ ] OOM-killed executions retry exactly once via `Retrier`, visible in dashboard as `retry: N/2 (oom_killed)`.
- [ ] With cap enabled and a deliberately memory-hungry prompt, the subprocess returns `oom_killed` from our own limiter (no kernel SIGKILL in `dmesg`).
- [ ] With cap disabled (default for this PR), no behavior change — bench delta within noise.
- [ ] `make test` green; no new lint failures.

## Out of Scope

- **RESEARCH-phase budget cap** — file as `TASK-288` once Step 1 gives us a week of data. Premature without baseline.
- **Cgroup-based isolation** — heavier rollout, defer to multi-tenant SaaS work (P1 backlog).
- **Per-prompt context trimming** in the executor — that's a Claude Code config surface, not Pilot's job.

## Why Now

Three OOMs in 24h on the demo project, each costing 5–10 minutes of
wall time + a spurious failure alert. The pattern is reproducible and
the fix is well-bounded. Without telemetry (Step 1) we keep flying
blind; with telemetry + smart-retry (Steps 1–2) the user-visible noise
drops and we have data to tune the cap (Step 3+4).

## References

- Incident triage in this conversation, 2026-05-21
- Code map (Explore agent run): `backend_claudecode.go:373` (spawn), `:101-114` (classifier), `retry.go:125-214` (retrier), `backend.go:429-444` (timeouts), `backend.go:167-213` (BackendResult)
- Pitfall to write after merge: `.agent/knowledge/memories/pitfalls/pitfall_claude_code_subprocess_oom_during_research.md`
- Pattern to write after merge: `.agent/knowledge/memories/patterns/pattern_subprocess_resource_limits_and_telemetry.md`
