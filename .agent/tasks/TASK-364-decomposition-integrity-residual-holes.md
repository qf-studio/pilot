# TASK-364: Decomposition integrity — residual holes after v2.183.0 (GH-3535 live run)

**Status**: 🟢 **largely RESOLVED by TASK-361 wave 2, shipped in v2.186.0** (PR [#3565](https://github.com/qf-studio/pilot/pull/3565), merged `b02ee1c1` 2026-06-10) — **MANUAL** (TASK-320 B2 rationale: Pilot cannot reliably patch its own completion/supersession machinery)
**Priority**: P1 → P2 residue (see "Wave-2 coverage map" below)
**Origin**: TASK-361 live verification on GH-3535, 2026-06-10 (daemon on v2.183.0)

## Wave-2 coverage map (v2.186.0)

| Hole | Wave-2 fix | Status |
|---|---|---|
| 1 — premature parent close (new path) | Root-caused: `WaitForExecution` hang + `selfHealForPR` child-PR adoption. Fixes 1a–1d (terminal statuses, `shouldDeferIssueClose`, gated self-heal, `issueHasOpenChildren` on TASK-321 path) | ✅ closed |
| 1 variant — standalone-sibling PR adoption (#3546/#3554) | 1a kills the hang→adopt window; self-heal of the task's own row still possible for genuinely-matching standalone tasks | ⚠️ partially — watch live |
| 2 — supersession of PR-less children | Positive evidence required (merged `pilot/GH-N` PR or completed execution row), else dispatch | ✅ closed |
| 3 — cross-project bleed / empty children | Empty-description filter + foreign-parent plan rejection (code); knowledge-store purge (ops, alekspetrov instance DONE; mvanhorn/ylcn91 pending) | ✅ code / ⏳ ops |
| 4 — model-stamped `Parent:` refs | NOT covered — `subIssueBody()` should stamp `Parent:` programmatically and strip model-emitted lines | ❌ open (only residue) |
| 5 — recovered children execute with empty prompts | NOT covered by wave 2 (the empty-description filter guards CREATION, not the `recoverExistingSubIssues` rehydration path) | ❌ open |

**Remaining work**: Hole 4 (programmatic `Parent:` stamping), Hole 5 (rehydrate recovered children from issue body/DB), ops purge on teammate instances, PAT rotation/scoping. Live verification of wave 2 on next decomposed epic pending (checklist in TASK-361).

## Incident summary (all timestamps 2026-06-10 UTC, evidence on GitHub)

The v2.183.0 daemon decomposed #3535 (TASK-285) into #3536 (memory/CLI), #3537 (TUI wiring), #3538 (hallucinated OAuth child). Timeline:

- 10:32:55 — child #3536's PR [#3539](https://github.com/qf-studio/pilot/pull/3539) merged (base=main ✅, in-scope ✅)
- 10:33:03 — parent #3535 comment "✅ Pilot completed! **Duration 0s**, Branch `pilot/GH-3535`, PR #3539" — claims the **child's** PR (actual head `pilot/GH-3536`) as the parent's own
- 10:33:08 — #3538 auto-closed superseded, comment names "parent epic **#201**" (wrong parent)
- 10:33:55 — parent #3535 **closed `pilot-done` while #3537 still open and unshipped**
- 10:51:59 — #3537 auto-closed "parent already shipped this work" — parent had NOT shipped the TUI slice
- 11:07:18 — re-decomposition attempt → `ErrParentDone` guard fired ✅ → `pilot-failed` stacked on `pilot-done` (known cosmetics, TASK-361 chain link 3)

Net: TUI slice silently dropped; recovered manually via standalone issue [#3552](https://github.com/qf-studio/pilot/issues/3552).

## What v2.183.0 (#3527) did fix — confirmed live ✅

- Scope fence present in all child bodies; shipped child stayed in-slice
- `ErrParentDone` blocks re-decomposition of a done parent
- Child PR based on `main` (note: #3540/#3541, TASK-362's children, carry the systematic base fix)

## Hole 1 — Premature parent close, NEW path (not `maybeCloseParentIssue`?)

Parent #3535 was closed at 10:33:55 with a completion comment ("Duration 0s", child's PR attributed to the parent, wrong branch name) **while child #3537 was verifiably open**. The #3527 fix hardened `openSubIssueCount()` (native count + text-search confirm) inside `maybeCloseParentIssue`/`recoverStaleParentIssues` — yet the parent closed anyway.

**Investigate**: which code path posted "✅ Pilot completed / ✅ PR merged successfully" on the parent and closed it? Suspect the executor/autopilot completion-notification flow attributes a child's merged PR to the parent task (the parent's task record likely held the child's PR number) and closes the issue directly, bypassing `openSubIssueCount` entirely. The "Duration 0s" suggests the parent task no-op'd and adopted the child's result.

**Fix direction**: any parent-close must go through one gate that (a) counts open children (existing `openSubIssueCount`) AND (b) verifies each known child has a **merged** PR (TASK-361 follow-up "PR-merge verification in `closeParentNow`", Option 2: track `PRState.IssueNumber` → merged). No completion comment may claim a PR whose head branch doesn't match the task's own branch.

## Hole 2 — Supersession of PR-less children

`skipSupersededByParent` (`internal/adapters/github/poller.go:1905`) closes any child whose parent is closed+`pilot-done`. The #3527 veto only fires when the child has an **open PR** — a child whose work never started (no PR at all) is unguarded. That is exactly #3537.

**Fix direction**: invert the default for PR-less children — a child with no PR and no merged-PR evidence must NOT be superseded; leave open + alert ("parent done but child unshipped — possible premature close"). Supersede only on positive evidence (child's own merged PR, or parent-close gate from Hole 1 verified all children).

## Hole 3 — NOT hallucination: cross-project sub-issue bleed (auth-service OAuth loop) — ROOT-CAUSED 2026-06-10

The "OAuth provider integration" children are **real tasks from a different project, created into the wrong repo**. Verified chain:

1. **Origin**: `qf-studio/auth-service#18` "OAuth2 social login: Google, GitHub, Apple" (closed) — a real epic from April. `auth-service#201` ("GH-197: Data layer…", closed) is one of its decomposition descendants. The children's `Parent: GH-201` refers to the **auth-service** namespace.
2. **Repo bleed**: sub-issue creation drops the children into `qf-studio/pilot` (the daemon's default/wrong repo), where `#201` is an unrelated ancient merged PR (`pilot-done`). Task IDs are repo-unqualified `GH-N` — namespace collision.
3. **The loop**: in pilot's namespace the "parent" looks done → poller supersedes/humans delete the child → the source state still shows the slice unshipped → re-created next cycle. **Deleting the children re-arms the loop.** Running since 2026-04-05 (`GH-18 completed`); local DB has **40 OAuth execution rows across ~33 task IDs**, **32 of them executed with `project_path = …/pilot`** (17 recorded `completed` against the wrong repo — same family as TASK-355's wrong-repo commit SHA).
4. **Multi-instance**: three daemons spawn them — alekspetrov, `mvanhorn`, `ylcn91` (30+ issues in qf-studio/pilot since 2026-06-02 alone; ~70+ in the 2026-05-08 burst). Each instance carries its own copy of the stale state; purging one doesn't stop the others.

### FEEDER FOUND (2026-06-10 evening session): knowledge-store poisoning + prompt injection

The re-creation vector is **NOT** a queue row — `executions` has zero `GH-201` rows, auth-service execution stopped 2026-04-07, auth-service repo has no open pilot issues, `global_patterns.json` is clean. The feeder is:

1. **`~/.pilot/data/knowledge.json` holds 73 OAuth-poisoned entries** (verified) — verbatim issue records ingested as learnings: title `feat(auth): add OAuth provider integration`, content `GitHub Issue #2500: … Parent: GH-201`. Plus 34 OAuth `learning` rows in the `memories` table ("Completed GH-2568: feat(auth)…", all 2026-05-04, project_id=pilot — the April/May wrong-repo executions were *learned* under the pilot project).
2. **GH-2147 prompt injection**: `BuildPrompt` (`internal/executor/prompt_builder.go:105-118`) appends up to 3 knowledge-graph nodes as `## Related Learnings` (**Title: Content** verbatim) via `GetRelatedByKeywords(extractTaskKeywords(title+description))`. With 73 entries, generic keyword overlap retrieves an OAuth learning into unrelated epic executions.
3. **The model copies the learning into its sub-issue output** — title byte-identical, `Parent: GH-201` copied from the learning content (this is why the "wrong parent" is identical everywhere; it's quoted text, not inference). Empty descriptions = the learning content has nothing after the parent line.
4. **Self-reinforcing**: each spawned issue's execution/ingestion writes ANOTHER OAuth learning → retrieval gets more likely. Closing/deleting issues never touches the store. Each operator's instance (alekspetrov, mvanhorn, ylcn91) carries its own poisoned knowledge.json from processing the same issue stream.

Timeline fits: today's spawns trail each epic execution by minutes (#3547 at 13:29 local during GH-3532's 13:07 re-dispatch; #3538 ten minutes into GH-3535's run).

**Fix direction (final)**:
- **Operational kill-switch (per instance, all three operators)**: purge OAuth/GH-201 entries from `knowledge.json` + `memories` table (daemon stopped while editing; keep `.bak`). This stops the spawning immediately.
  - ✅ **DONE on alekspetrov's instance 2026-06-10 ~17:00 CET** (daemon stopped, backups `*.bak-oauth-purge-20260610T165902`): `knowledge.json` 4327 → 4303 entries (24 feeders removed: 18 verbatim spam-issue records + 6 original GH-18/GH-201 epic-children records); `memories` table −18 spam rows (16 legit distilled auth-service completions kept); `cross_patterns` clean; WAL checkpointed. One benign mention remains (entry "Guardrail: refuse issue creation on repos outside…", a meta-learning quoting the spam title as diagnosis of the earlier #3021–#3026 burst — note: that guardrail can't stop this loop, pilot IS a configured repo).
  - ⏳ mvanhorn + ylcn91 instances still poisoned — they will keep spawning until they purge.
  - **Live test result (2026-06-10 evening): 3 new spawns post-purge (#3562/#3563/#3564, 16:05–16:29Z, "author: alekspetrov") — but NOT from this machine.** Evidence: (a) the `autopilot-meta parent: GH-201` block is **code-stamped** from `plan.ParentTask.ID` (`epic.go:1183`), so each spawn requires a live decomposition of a `Task{ID: GH-201}`; (b) this DB has zero GH-201 executions ever and no local pilot activity at the spawn times; (c) `gh issue create` runs with the daemon's configured PAT — **the "alekspetrov" author is the shared Pilot PAT, not proof of this machine**. Any teammate instance configured with the shared token (mvanhorn/ylcn91 also author under their own identities at other times) produces alekspetrov-authored spam. Conclusion: the local purge holds; remote instances still carry both the poisoned stores **and a live GH-201 task in their own queue/state** (something still dispatches `Task{ID: GH-201}` there — find it on their machines: executions table / board config / adapter_processed).
  - **Action items**: (1) teammates run the same purge + search their DB for `task_id='GH-201'`; (2) rotate or per-operator-scope the shared PAT so issue authorship identifies the spawning instance; (3) until then, alekspetrov-authored spam ≠ local feeder.
- **Code**:
  (a) ingestion hygiene — never ingest raw issue records (`GitHub Issue #N: <title>\nParent: GH-N`) as learnings; learnings must be distilled text, repo-qualified;
  (b) injection hygiene — `Related Learnings` must exclude anything shaped like an issue record, and the epic/decomposition prompt must instruct that learnings are CONTEXT, never subtask candidates;
  (c) repo-qualify task identity (`owner/repo#N`) end-to-end; sub-issue creation inherits the parent's source repo, refuses cross-repo;
  (d) reject empty-description children at creation (kills the degenerate copies even if retrieval recurs).

This also reframes **Hole 4**: the `Parent: GH-201` refs are not model hallucination — they are correct refs in the auth-service namespace, mis-resolved in qf-studio/pilot. The fix is the same repo-qualification.

## Hole 3 (cont.) — original hallucination framing (superseded by the above)

"feat(auth): add OAuth provider integration" generated **6 times on 2026-06-10**: #3524/#3525/#3526/#3538 (alekspetrov daemon) + #3553/#3556 (a **second Pilot instance, author `mvanhorn`**). All carry `Parent: GH-201`; #3553/#3556 have **empty slice descriptions** (scope fence wrapping nothing).

Lineage: this is the **2026-05-08 GH-201 incident** (OAuth dispatch loop, 70+ spurious sub-issues — see `epic.go:612` comments, `epic_test.go:2247+`). The post-incident gates (`isParentDone` + live GitHub fallback + `State` propagation in `handlers.go:297`) only guard the task being *decomposed* — they cannot catch a model-emitted extra child under a legitimately-open parent (#3538 spawned during #3535's decomposition), nor whatever stale state mvanhorn's instance re-dispatches. The literal string "OAuth provider integration" appears nowhere in the repo — the model fills empty/placeholder slices with a generic title (note `epic.go` uses `"feat(auth):"` as the type-scope regex example).

**Fix direction**: validate decomposer output before creating issues — **reject children with empty descriptions** (would have killed #3553/#3556), reject children whose title/body share no file paths or key terms with the parent spec, cap children to the parent's enumerated scope.

**Operational**: coordinate with `mvanhorn` — their instance polls qf-studio/pilot and produced #3553/#3556 with scope-fence-era code; it should be upgraded/pointed elsewhere or its GH-201 queue entry purged.

## Hole 4 — Parent ref stamped by the model, not the system

#3538's supersession compared against parent **#201** because `ParseParentIssueNumber` (`internal/adapters/github/grouping.go:38`, regex `^Parent:\s*(?:GH-|#)(\d+)`) read the hallucinated `Parent:` line. The decomposer *knows* the real parent number — the `Parent:`/`autopilot-meta` block must be stamped programmatically by `subIssueBody()` (`internal/executor/epic.go`), and any model-emitted `Parent:` line stripped.

## Hole 5 — Recovered children execute with EMPTY prompts (root-caused 2026-06-10 evening)

After a daemon restart, a re-dispatched epic parent recovers its children via `recoverExistingSubIssues` (`internal/executor/epic.go:885`), which rebuilds each as `CreatedIssue{Number, Identifier, URL, State}` — **`Subtask` is the zero value**. `ExecuteSubIssues` (epic.go ~:1466-1474) then builds the child task with `Description: issue.Subtask.Description` = `""`. The worker receives a prompt reading `## Task: GH-N` followed by nothing, spins up a full Claude run with nothing to implement, and dies (`unknown: exit status 1`). Observed live on #3558: three wasted laps (13:44, 14:31, 15:05) + a fourth caught in the act — the DB had the description all along (1520 chars), and the GitHub body was intact; the recovered path just never reads either.

**Fix**: when a recovered `CreatedIssue` has empty `Subtask.Title/Description`, fetch the issue body (gh/client) before building the child Task — or persist the plan's subtask specs and rehydrate from there. Cheap guard: refuse to execute any task whose description is empty (same spirit as Hole 3 fix (d)).

**Workaround applied 2026-06-10**: closed parent #3557 manually (stops the orchestrator loop; `isParentDone` blocks re-decomposition), stripped `pilot-in-progress` from #3558 so the poller dispatches it standalone with the real body — the same path that executed #3540/#3541 correctly.

Related friction same day: one `gh` HTTP 401 during #3557's sub-issue creation (15:27, transient around restarts), and 4 daemon restarts cost each in-flight task its full execution window (e.g. #3552 executed 4× before its PR landed).

## Acceptance criteria

- [ ] Root cause of the #3535 parent-close path identified with file:line; all parent-close paths route through one verified gate (open-children count + per-child merged-PR check)
- [ ] Completion comments never attribute a PR whose head branch ≠ the task's branch; no "Duration 0s" completions close issues
- [ ] PR-less children are never superseded; alert instead
- [ ] Decomposer children validated against parent scope; parent ref stamped by code, not model output
- [ ] Regression tests: GH-3535 scenario (parent closes after 1 of 2 children; second child must survive supersession), hallucinated-child rejection, model-emitted `Parent:` line stripped
- [ ] Live verification on next decomposed epic: parent stays open until ALL children's PRs merge

## Reproduction #2 — same day, GH-3532 (TASK-362), ~1h later

Hole 1 reproduced verbatim on the second decomposed parent:

- 11:36:52 — child #3540's PR [#3548](https://github.com/qf-studio/pilot/pull/3548) merged (base=main ✅, CI green ✅, artifact-verified ✅)
- 11:37:00 — parent #3532 "✅ Pilot completed! **Duration 0s**, Branch `pilot/GH-3532`, PR #3548" — child's PR adopted again
- 11:37:51 — parent #3532 closed `pilot-done` **while child #3541 still open/in-progress**
- 11:54:24 — re-decomposition refused (`ErrParentDone`) → `pilot-failed` stacked

#3541 (tag-reachability guard) survived because it was already `pilot-in-progress` — but it has no PR yet, so a daemon restart before its PR opens would falsely supersede it (Hole 2 live exposure). Side noise: duplicate PR #3550 from the same branch (SHA `84b76b4`) failed CI and spawned stale fix-request #3551 (`pilot-needs-clarification`) — obsolete, the work merged green via #3548.

**Pattern now confirmed deterministic**: every decomposed parent closes the moment its FIRST child's PR merges, with a "Duration 0s" completion comment attributing that PR to the parent. Two-for-two (GH-3535, GH-3532).

## Hole 2 confirmed live — #3541 dropped (12:14:29Z)

As predicted 40 minutes prior: #3541 (tag-reachability guard, `pilot-in-progress`, no PR yet) was auto-closed `pilot-superseded` — "parent epic #3532 already shipped this work." No `pilot/GH-3541` branch was ever pushed; the work is simply gone. An in-progress child with no PR has zero protection the moment its parent wears `pilot-done`.

## Hole 1 variant — adoption across STANDALONE siblings (#3546/#3554, 12:36Z)

The "Duration 0s, adopt another task's PR" pattern is not decomposition-specific. Docs issues #3546 and #3554 (near-identical scope, both standalone) each posted "✅ Pilot completed! Duration 0s … PR #3555" — but PR #3555 (head `pilot/GH-3554`, merged 12:35:58Z) belongs only to #3554. #3546 never pushed a branch; its completion is phantom (TASK-320/TASK-355 no-op family). Whatever flow matches "a merged PR exists that satisfies my task" credits it to any task whose scope looks similar, in 0 seconds.

## Cross-refs

- TASK-361 (incident record + live-verification log) · PR #3527 (v2.183.0 guards) · TASK-362 / #3540 / #3541 (base-branch fix, in flight)
- Memories: `mem-030` (premature close), `mem-031` (label-trust supersession), `mem-032` (full-spec inheritance), `mem-033` (verify artifact not status), `bug_premature_parent_close_partial_links`, `bug_false_supersession_label_trust`
- **Watch now**: #3532's children #3540/#3541 — same trap; when the first merges, parent #3532 may close prematurely and the sibling get superseded
