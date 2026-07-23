# TASK-416: Stall-class fixes — silent-turn false stalls, hard-cap wedge, orphaned children

**Created**: 2026-07-22 · **Status**: ✅ PROVEN 2026-07-23 eve → ARCHIVED — [#4501](https://github.com/qf-studio/pilot/issues/4501)→PR #4504 / [#4502](https://github.com/qf-studio/pilot/issues/4502)→PR #4505 / [#4503](https://github.com/qf-studio/pilot/issues/4503)→PR #4506, live on the box since the 12:18Z manual rebuild (`v2.245.0-6`, then `v2.245.1-6`). **Proof**: GH-26 (B8) ran two long claude-sonnet-5 effort=high turns — 43m54s (gen 3) and ~45m (gen 4, PR#30 delivered) — with ZERO false stall-kills; #4505's carve-out observed live ("prior claim was stall-killed — claiming next generation without counting toward repick hard cap"). The class is dead. Two NEW failure modes surfaced during the proof runs (out of this doc's scope): heartbeat-timeout SIGKILL under stdout base64 flood (unfiled, watch), and dirty-worktree no_op work destruction (pilot#4517 → PR#4518 ✅ merged same day).
**Incident**: pilot-console GH-24 (2026-07-22) — 4 identical stall-kills on long
claude-sonnet-5 effort=high turns → `dispatcherRepickHardCap=5` wedge → manual
re-arm → gen-5 delivered. Plus one orphaned `claude` process surviving 1h14m.
**Marker origin**: `2026-07-22_v2.245.0-live-task415-reconciler-shipped.md` (queue item 2).

## Root causes (navigator-research, 2 agents, file:line-verified)

### D1 — False stall on silent model turns (the trigger)

- Watchdog reset logic is CORRECT: `opts.EventHandler` fires unconditionally per
  stdout line (`backend_claudecode.go:751-753`) and resets `lastEventAt`
  (`runner.go:2799-2801`) regardless of parsed event type.
- The CLI is spawned with `--verbose --output-format stream-json` but WITHOUT
  `--include-partial-messages` (3 sites: `backend_claudecode.go:468-469, 480-481,
  490-491`). Without it, a turn emits ONE complete `assistant` message at the end
  — zero bytes during the turn. Long high-effort thinking > 3m ⇒ kill at
  `watchdog.go:61-71` (`runner.go:2902-2904`).
- `parseStreamEvent` (`backend_claudecode.go:975-1057`) has no cases for
  delta/partial chunk types — consistent with the flag never being passed.
- #4364/GH-4357 (background bash) fix used the CLI's `task_started` signal to
  suspend the clock (`watchdog.go:37-41,58-60`); no analogous signal exists for
  a silent generation turn — that fix category is structurally inapplicable.
- Defense-in-depth is free: `complexity`, `selectedModel`, `selectedEffort` are
  all in scope at the watchdog-arm site (`runner.go:2736`, cf. `:2069/:2444/:2450`);
  precedent for complexity-keyed durations: `model_routing.go:157`
  (`GetTimeoutForComplexity`). `stall_timeout_ms` today is global-only
  (`backend.go:386-435`).

### D2 — Stall-kills burn the repick hard cap (the amplifier)

- `terminalExecutionStatuses` includes `"stalled"` (`dispatcher.go:46`);
  `HasTerminalCompletion` (`memory/store.go:1125-1144`) does not ⇒ every
  stall-kill takes the generic retry path and increments `consecutiveDrops`
  (`dispatcher.go:1142`) → cap at `dispatcherRepickHardCap=5` (`:937`, enforced
  `:1125-1128`).
- Existing carve-outs (the pattern to mirror): operator-cancel
  `priorClaimWasOperatorCancelled` (`dispatcher.go:946-965`, consulted `:1065`);
  restart-churn boot reconciliation (`:318-335`, #4455). None for `stalled`.
- #4484 outcome taxonomy classifies stall correctly as its own class
  (`runner.go:189-221, 2902-2940`) but is telemetry-only — zero connection to
  hard-cap accounting.

### D3 — Single-PID kill leaks orphaned children

- All kill paths do `cmd.Process.Kill()` on the direct child only: heartbeat
  (`backend_claudecode.go:663-677`), absolute watchdog (`:709-721`), stall
  ctx-cancel grace path (`:809-854`, kill at `:841`), graceful `CancelAll`
  (`runner.go:4764-4809`). No `Setpgid`/`SysProcAttr`/negative-pid kill/
  `WaitDelay` anywhere in `internal/executor/` (repo-wide grep).
- Claude's Bash tool backgrounds children (grandchildren of the daemon,
  GH-4357); they survive the kill and reparent to the daemon — matches the
  observed `ppid=daemon`, 335M RSS orphan.
- No process reaper exists. The cgroup v2 leaf (`resource_limits_linux.go:43-88`)
  captures the tree but is RSS-only; `cgroup.kill` never written.

## Dispatch

| Defect | Issue | Scope |
|---|---|---|
| D1 | [#4501](https://github.com/qf-studio/pilot/issues/4501) | `--include-partial-messages` + parse cases + effort/complexity-aware stall timeout |
| D2 | [#4502](https://github.com/qf-studio/pilot/issues/4502) | `stalled` carve-out from `consecutiveDrops`, separate stall cap → escalate-and-hold |
| D3 | [#4503](https://github.com/qf-studio/pilot/issues/4503) | `Setpgid` + process-group kill at all kill sites + `WaitDelay` |

Ordering: independent. Note D2 must NOT be a pure bypass (until D1 ships, a
complex task stalls deterministically → pure bypass = infinite retry loop);
spec requires a separate, higher stall cap with truthful escalate-and-hold.

## Interim ops (NOT part of the issues, founder-gated restart)

`stall_timeout_ms: 600000` in box `~/.pilot/config.yaml` + daemon restart —
mitigates D1 until the fix rides a train. Not applied as of 07-22.

## Open unknowns

- Installed CLI version's support for `--include-partial-messages` (issue A has
  a verification AC + fallback to timeout-only fix).
- Whether `"infra"` outcomes deserve the same hard-cap carve-out as `stalled`
  (flagged in issue B as an explicit judgment call, default = out of scope).
- Live box `subprocessLimits.Enabled` (cgroup) — if on, `cgroup.kill` would be a
  fuller D3 mechanism; issue C keeps process-group kill as the portable fix.
