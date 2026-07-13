# feat(autopilot): release cycles — scope-triggered + scheduled releases (TASK-389)

**Status**: ✅ **6/6 SHIPPED** (2026-07-07/08). E #3994 landed on retry-3 as **PR #4096** (+269/−2, merged 2026-07-08 18:47Z → v2.235.8, daemon live) — first clean run after #4050 (SDK label drop) + #4052 (decomposer feed) fixed the pipeline; `require_ci` now gates the polled merge path fleet-wide. **Trains ACTIVATED 2026-07-08**: 6 quiet repos on `on_schedule` `"0 16 * * 1-5"` (16:00 CEST weekdays = 10:00 ET, US/EU audience window; pilot stays per-merge for dogfooding; backup `config.yaml.bak-20260708-release-trains`). Ready to archive. — Original: 5/6 (2026-07-07, v2.230.0–v2.233.0). A #3989 ✅ 33min · B #3990 ✅ · C #3991 ✅ · D #3992 ✅ retry-2 → PR #4018 merged 17:58 (v2.233.0: aggregated scope notes + LLM What's New + scope notifications) · F #3993 ✅ (trains live) · E #3994 ⚠️ NOT LANDED — require_ci still bypassed on the polled path; today's v2.233.x releases cut via the old instant path. **Full forensic chain (2026-07-08):** attempt-1 (19:47) + attempt-2 (22:36) BOTH ghosted at the ledger level (dispatcher logged `pr_url=""` both times; v2.233.2 lacked the #4028/#4031 finalization fix). Attempt-2's PR #4047 existed only because an in-session subtask agent ran `gh pr create` itself and the **reconciler adopted the untracked PR** (23:07:38) — the #4006→#4012 accident, non-deterministic. The `no-decompose` recovery label was a **no-op**: root cause **#4050** — the SDK poller (`handleGithubIssueEventSDK`, `handlers.go:1227`) drops `task.Labels`+`State` fleet-wide (GH-201 regression from M7 4b), so every label gate no-ops for SDK-dispatched tasks. PR #4047 then wedged 3 ways → **CLOSED unrecoverable**: merge conflict + >500-line approval gate (inflated to 586 by `.agent/**` bookkeeping, #4051) + per-tick `ON CONFLICT` persist loop on the adopted row (#4053). **Re-land plan: after #4050 lands, re-arm GH-3994 (`+pilot-retry-ready`) → no-decompose honored → clean ~150-line single-task PR.** Defects filed: #4050 (HIGH, SDK label/state drop) · #4051 (merge-gate counts .agent churn) · #4052 (epic consolidateEpicPlan feeds decomposer) · #4053 (adopted-PR persist wedge). Bonus shipped: #4001 per-project opt-in ✅ · #4006 epic loop breaker ✅ · #4008 poller noise ✅ · navigator#25 updater hardening ✅. Residue from D's retry storm: **#4020** ✅ shipped via epic children #4022/#4023 → PRs #4024/#4026 (v2.233.1/.2; parent awaiting reconciler close) · **#4021** attempt-1 ghost-completed (4 subtasks green, branch never pushed, `pr_url=""`) → retry-armed with no-decompose constraint · **#4028** filed (decomposed-parent finalization skips push+PR yet marks completed — TASK-359 Shape A on the in-process decomposition path; + subtask-event FK failures + stuck-monitor false eviction). Pitfall mem-088.
**Created**: 2026-07-07
**Assignee**: Pilot

---

## Context

**Problem**: `trigger: on_merge` cuts a release per merged PR — TASK-388 alone produced
6 versions in 14h. There is no way to batch a scope of work into one validated
release, and no scheduled cadence.

**Goal**: opt-in per-repo **release cycles**. Merged PRs belonging to an open scope
are HELD (no tag). When the scope completes — or a cron tick fires — ONE release is
cut, gated on full CI green at the final main SHA, with aggregated release notes.
Per-merge stays the default (pilot dogfoods same-day fixes via per-merge self-upgrade).

**Locked decisions** (user, 2026-07-07):
1. Scope identity = **epic** (auto) + **`scope:<name>` label** (explicit sibling groups).
2. Standalone merges keep per-merge releases (mixed mode).
3. Validation = green CI on final main SHA (reuse post-merge CI gate; no workflow_dispatch).
4. Notes: scope headline · grouped Features/Fixes/Other with #PR + GH-issue links ·
   ⚠️ Breaking Changes · LLM "What's New" (Haiku) · compare link + stats footer.
5. **`on_schedule` trigger ships now** (user requirement: e.g. "Fridays 21:00") —
   cron-driven release trains batching everything since the last tag.

---

## Known Pitfalls & Patterns

<!-- From knowledge graph — these MUST shape the implementation -->

- **DECISION** (decision_release_pipeline_tag_only): never pre-create a Release on a
  GoReleaser repo — publish-mode interplay unchanged; scope releases go through the
  existing publish-mode switch (`workflow|api|tag_only`), no new publish path.
- **PATTERN** (pattern_autopilot_pr_state_ephemeral): `autopilot_pr_state` rows are
  deleted on completion — scope durability lives in a NEW `autopilot_scope_release`
  table, never in PR rows.
- **PITFALL** (bug_pilot_ghost_closes class): an issue can close without merged code —
  scope completion MUST reuse `verifyChildrenShippedForClose` semantics (closed member
  without merged PR/verified no-op = veto), never raw issue state.
- **PATTERN** (pattern_squash_merge_mergedat_null): merge detection via existing
  scanner checks, not `mergedAt` alone.

---

## Architecture

```
merge ─► release decision (4 sites: hijack :3822 · handleMerged fast path :1882 ·
         handlePostMergeCI success :2149 · ScanRecentlyMergedPRs loop)
          ├─ on_merge / not member ─► per-merge release (byte-identical)
          ├─ on_scope_close + member ─► HOLD (drain, no tag, zero new state)
          └─ on_schedule ─► HOLD everything
completion signal (pluggable):
  ├─ epic closes   → reconcileEpicParent already has mergedPRs → enqueue "epic:<N>"
  │                  + closed-parent lookback sweep (crash backstop, ScopeLookback 24h)
  ├─ label done    → new reconcileLabelScopes → all-closed+all-shipped → "label:<name>"
  └─ cron tick     → commits since last tag → "train:<tick RFC3339>"
        ▼
autopilot_scope_release row (PK repo+scope_key; pending|releasing|done|failed;
INSERT OR IGNORE + atomic claim = exactly-once)
        ▼ CARRIER = real highest merged member PR registered at StagePostMergeCI,
          PostMergeSHA="" → captures current main SHA → existing CI gate verbatim
          (green → releasing · red → fix issue + row→pending · 5 attempts → failed+alert)
        ▼ handleReleasing scope branch: commits = union GetPRCommits(members)
          (train: CompareCommits(lastTag→SHA)); bump = DetectBumpType(union);
          HeadSHA := PostMergeSHA; SKIP tagCoveringCommit/GetTagForSHA drains
          (table is the dedup — interleaved standalone tags would falsely drain);
          KEEP guardReleaseSHAReachable + publish modes + afterTagCreated + notify
        ▼ one tag + one Release (aggregate notes) → row done → removePR
```

Ground truth notes (verified on origin/main @ 30f2c9eb): the de-facto per-merge release
entry is the external-merge hijack (`checkExternalMergeOrClose` :3822), NOT handleMerged —
hold enforced at all four sites. `PurgeTerminalPRStates` has zero prod callers. LLM
summary generator exists but was never wired in main.go (Issue D wires it).
`robfig/cron/v3` already a dep with precedent `internal/briefs/scheduler.go`.

---

## Issue ledger

| Issue | Title | Deps | Status |
|---|---|---|---|
| [#3989](https://github.com/qf-studio/pilot/issues/3989) | on_scope_close/on_schedule trigger config + per-PR hold semantics | — | 🚀 dispatched |
| [#3990](https://github.com/qf-studio/pilot/issues/3990) | scope release state table + carrier execution (epic scopes) | Blocked by: #3989 | dispatched (gated) |
| [#3991](https://github.com/qf-studio/pilot/issues/3991) | label-scope reconciler (`scope:<name>` completion) | Blocked by: #3990 | dispatched (gated) |
| [#3992](https://github.com/qf-studio/pilot/issues/3992) | aggregated scope notes + wire LLM summary + notifications | Blocked by: #3990 | dispatched (gated) |
| [#3994](https://github.com/qf-studio/pilot/issues/3994) | fix: honor require_ci on polled merge path (GH-411 hijack + scan-recovery → StagePostMergeCI) | — | 🚀 dispatched 2026-07-07 (human decision: option 1, honor at hijack; regression-pin GH-411 first; spec in body) |
| [#3993](https://github.com/qf-studio/pilot/issues/3993) | on_schedule trigger — cron release trains | Blocked by: #3990 | dispatched (gated) |

---

## Design decisions

| Decision | Chosen | Reasoning |
|---|---|---|
| Hold-time state | none — membership rebuilt from GitHub at completion | Holding = existing drain-without-tag path; `verifyChildrenShippedForClose` already rebuilds merged-PR lists; fail-open on detection errors (never hold forever) |
| Scope idempotence | new `autopilot_scope_release` table | SHA/tag coverage can't answer "did scope X release?" once standalone tags interleave; INSERT OR IGNORE + atomic claim = exactly-once across ticker/backstop races |
| Carrier | real member PR (highest number), `ScopeKey` guard vs hijack | synthetic PR numbers get 404-evicted by processAllPRs; real PR reuses ALL existing machinery (persistence, CI gate, retries, publish modes, verification) |
| Trigger extensibility | enum `on_merge\|manual\|on_scope_close\|on_schedule`; scope keys namespaced `epic:\|label:\|train:` | completion signal is the only pluggable stage; future triggers are new signals, not redesigns |
| Cron engine | robfig/cron/v3, `cron.WithLocation` | already a dependency; `internal/briefs/scheduler.go` is the in-repo pattern |
| Trigger overlay | promote `Trigger` (+Schedule/ScopeLabelPrefix) into ProjectReleaseConfig | release cadence is exactly what varies per repo; reverses TASK-388's documented exclusion (rewrite comment types.go:426-431) |

---

## Config (target shape)

```yaml
autopilot:
  release:
    trigger: on_merge              # on_merge (default) | manual | on_scope_close | on_schedule
    generate_summary: true         # LLM What's New — needs ANTHROPIC_API_KEY
    scope_label_prefix: "scope:"   # on_scope_close only
    scope_lookback: 24h            # completion backstop window
    schedule: "0 21 * * FRI"       # on_schedule only (robfig cron syntax)
    schedule_timezone: "Europe/Berlin"   # default: daemon local
projects:
  - name: pilot
    release: { trigger: on_scope_close }
  - name: some-product
    release: { trigger: on_schedule, schedule: "0 21 * * FRI" }
  - name: studio-sdk
    release: { publish: api }      # keeps per-merge
```

---

## Risks

- Scanner-vs-scope residual race: bounded to one extra per-merge tag of one member; scope release still follows.
- Partial scopes never auto-release (correctness over liveness) — `scope_stale` alert (168h) is the escape hatch.
- >100-commit member PRs lose tail commits in notes (GetPRCommits unpaginated; CompareCommits cross-check, GitHub cap 250).
- Trains with only direct commits (no PRs) are skipped with WARN in v1 (no carrier).
- Ticks missed while daemon down: startup recovery checks last train row vs previous scheduled tick; older than `scope_lookback` → manual re-trigger (documented).
- Wiring the LLM generator enables per-merge What's New wherever `generate_summary: true` (~<1¢/release, Haiku, 200-line input cap).

---

## Verify

```bash
go test ./internal/autopilot/... ./internal/config/... && make lint && make build
```

End-to-end (pilot on `on_scope_close`): 2-issue epic → children merge with NO tags →
parent auto-closes → one tag after green CI on final main SHA → Release body has all
five note elements → one Telegram notification with scope headline. Standalone hotfix
mid-scope tags immediately. Restart mid-scope (between merges; between close and tag)
→ release cut exactly once. Train (`on_schedule` on a test repo): merges accumulate →
tick → one release; empty tick → nothing.

---

## Refs

- Plan: `~/.claude/plans/i-d-like-to-work-keen-bonbon.md` (approved 2026-07-07; F added on user request)
- Base: TASK-388 (publish modes/verification/human merges — #3926 ✅ · #3927/#3928 in flight)
- Key code: `internal/autopilot/controller.go` (:3822 hijack, :2277 handleReleasing, :2105 post-merge CI),
  `epic_reconcile.go`, `state_store.go`, `releaser.go`, `release_summary.go`, `internal/briefs/scheduler.go` (cron pattern)
- Pilot issues: #3989 (config+hold) → #3990 (state+carrier) → #3991 (labels) · #3992 (notes+LLM) · #3993 (trains) · #3994 (require_ci defect, bug-only)

---

**Last Updated**: 2026-07-07
