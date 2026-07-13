# Sub-issue siblings duplicate each other's work when wait_for_merge is off — dependency-aware selective merge-wait + stop emitting verify-only children

**Status**: ✅ **SHIPPED 2026-07-12 (code on main; live verification pending next epic run).** Despite the cascade, the work WAS delivered: PR [#4236](https://github.com/qf-studio/pilot/pull/4236) (part b — `dependency_detector.go`, selective merge-wait gated per child, unconditional `main.go` wiring, fail-loud wait/no-wait logs) and PR [#4237](https://github.com/qf-studio/pilot/pull/4237) (part c — `foldVerifyOnlySubtasks` decomposer post-processing) both reviewed + squash-merged interactively 2026-07-12 evening. Rides the next release train (daemon on v2.238.1 does NOT have it yet). Follow-ups: (1) dedupe the verify-shape regexes duplicated between `dependency_detector.go` and `epic_verify_fold.go` (noted in both files); (2) spec gap accepted: verification-shaped child waits only on the *immediately preceding* sibling's PR, not ALL prior siblings — mitigated by part (c) removing verify children at the source. NOTE: the earlier "gate BREACHED" claim was **refuted by timeline** — #4216 closed 18:49:47Z, #4217 dispatched ~18:50, i.e. after close; the cascade cause was deploy lag (fixes merged but pre-fix daemon still running), not a gate defect. Parent #4217 auto-closed 20:26 "all sub-issues complete" — vacuous at the time (children #4230–#4233 closed not-planned) but retroactively true via #4236/#4237.
**Type**: bug (executor epic sequencing — duplicate-PR class, Defect B)
**Evidence**: GH-4190 children #4194/#4195 → PRs #4197 vs #4198 with identical 4-file deletions (2026-07-10)

Blocked by: #4216

## Problem

With `orchestrator.execution.wait_for_merge: false` (the live throughput setting),
sequential epic children advance as soon as the previous child's PR is **created**,
not merged. A later child whose spec implicitly depends on an earlier sibling's
merged state re-does the work. Live repro:

- #4194 spec: "delete the three orchestrator methods + three test files" → PR #4197
- #4195 spec: "run the acceptance grep, confirm zero hits, run the full suite" → PR #4198
- #4198 was created at 22:28:14Z, **24 seconds before** #4197 merged (22:28:38Z).
  #4195's verification grep found the methods still present on main and
  "self-remediated" by deleting them again — identical 4-file diff
  (orchestrator.go −112, 3 test files −407). Legitimate LLM behavior against a
  broken precondition.

**The guard for this already exists and is deliberately disabled.**
`SetSubIssueMergeWait` (GH-2178/2179) makes `executeSubIssuesTracked` block on
`subIssueMergeWait(ctx, prNum)` + `syncMainBranch` between children
(`internal/executor/epic.go:2157-2181`), but is wired only when
`cfg.Orchestrator.Execution.WaitForMerge` is true (`cmd/pilot/main.go:2196-2203,
2360-2372`). Live config: `wait_for_merge: false` (throughput decision — do NOT
just flip it globally).

## Chosen design (decided): selective merge-wait + decomposer hygiene

**(b) Dependency-aware selective merge-wait.** Keep `wait_for_merge:false` as the
global default. In `executeSubIssuesTracked`, before starting child K, apply the
merge-wait + `syncMainBranch` **only when child K depends on a prior sibling**:
- explicit: child spec/body declares `Depends on: #N` / `Blocked by: #N`
  referencing a sibling, or the decomposition plan marks an ordering dependency;
- implicit heuristic: child title/description is verification-shaped
  ("verify", "confirm", "run the acceptance", "grep for zero hits",
  "regression-test the previous") — treat as depending on ALL prior siblings.
Wire the merge-wait callback unconditionally in `main.go` (it's cheap when
unused); the per-child decision lives in the executor, with a fail-loud log line
stating why a wait was or wasn't applied.

**(c) Decomposer hygiene: stop emitting verify-only children.** In the epic
planner/decomposer prompt + post-processing: a subtask whose only content is
verifying/confirming an earlier subtask's result must be FOLDED into that
subtask's acceptance criteria, not emitted as a standalone child. Post-process
guard: if a planned subtask matches the verification-shape heuristic and
references no new implementation surface, merge it into the preceding subtask.

Option (a) — global `wait_for_merge: true` — explicitly rejected (throughput).

## Must NOT change

- Global `wait_for_merge:false` default and its throughput semantics for
  independent siblings (they must still run without merge-waits).
- The merge-waiter itself (`NewMergeWaiter` / in-tree `merger.go`) — consume, don't rewrite.
- Defect A's fixes (#4216) — this issue layers on top; base your branch on main
  after #4216 merges (`Blocked by:` gate).

## Acceptance criteria

- [ ] Independent siblings: no merge-wait applied; wall-clock behavior unchanged
  (test: 2 children, no dependency markers → no wait calls).
- [ ] Explicit `Depends on: #<sibling>` child: waits for that sibling's PR to
  merge + `syncMainBranch` before starting (test with fake merge-waiter).
- [ ] Verification-shaped child: treated as dependent on prior siblings (table
  test over the heuristic: "verify…", "confirm zero hits…", "run acceptance…"
  positive; "add feature…", "fix bug…" negative).
- [ ] Decomposer: a plan containing a verify-only subtask emits N−1 children,
  with the verification folded into the implementing subtask's acceptance
  criteria (test on the post-processing guard at minimum).
- [ ] Fail-loud log on every wait/no-wait decision.
- [ ] `go test -race ./internal/executor/... ./cmd/pilot/...` + full `go test -race ./...` green.

## Verify

```bash
go build ./... && go vet ./... && go test -race ./...
```

## Refs (origin/main)

- Merge-wait mechanism: `internal/executor/epic.go:2157-2185` · wiring `cmd/pilot/main.go:2196-2203,2360-2372` · config `~/.pilot/config.yaml` `orchestrator.execution.wait_for_merge`
- Repro: #4194/#4195, PRs #4197/#4198 (identical deletions, 24s race)
- Defect A (sibling issue, gate): #4216
- Prior art: GH-2178/2179 (merge-wait), TASK-393 (throughput program — why global flip is rejected)
