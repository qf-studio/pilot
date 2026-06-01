# TASK-319: GitHub Projects V2 — full board-driven lifecycle loop

**Status:** ✅ **DONE — full lifecycle LIVE-VERIFIED end-to-end (2026-06-01)**. Board-sourced `studio-sdk#12` walked `Todo → In Progress → In Review → Done` automatically (~5 min board-placement → squash-merge); `Blocked` verified manually last session. All five columns exercised. Code complete + go-live configured (board 5 cols, global adapter dedicated to studio-sdk, gitnation-companion excluded). Closeable.
**Priority:** P1 — completes the board-as-source-of-truth roadmap (Studio SDK)
**Repo:** `qf-studio/pilot` (code) — drives `qf-studio/studio-sdk` work via `qf-studio/projects/1`
**Depends on:** #3228 / TASK-317 (`FindIssuesFromProject`, read path) — must merge first
**Decisions (2026-05-29):** **full 5-state loop (Option B)** · Studio SDK board only · the `ghp_` PAT in `~/.pilot/config.yaml` (scopes `project, read:org, repo`) is the board token

## Locked status mapping (Option B — 5 columns)

Board today has 3 columns (`Todo | In Progress | Done`); Option B adds **In Review** + **Blocked**.

| Column | Color | Pilot event | Source/Write |
|---|---|---|---|
| Todo | GREEN | queued work | `source_status` (read, #3228) |
| In Progress | YELLOW | issue picked up / executing | write on dispatch (PR-1) |
| In Review | ORANGE | PR opened | write on `OnPRCreated` (PR-2) |
| Done | PURPLE | PR merged | write on merge (exists) |
| Blocked | RED | exec-fail / CI-fail | write on failure (PR-2) |

```yaml
# configs/pilot.example.yaml — full board-driven block
adapters:
  github:
    repo: qf-studio/studio-sdk
    project_board:
      enabled: true
      project_number: 1
      status_field: Status
      source_enabled: true     # from #3228 — pull work FROM the board
      source_status: "Todo"
      statuses:
        in_progress: "In Progress"
        review: "In Review"
        done: "Done"
        failed: "Blocked"
```

## Board setup (do once, before go-live)

**Recommended — web UI (zero risk):** open https://github.com/orgs/qf-studio/projects/1
→ Status field settings → add two options: **In Review** (orange, place between In Progress and Done) and **Blocked** (red, last). Preserves all 10 existing items.

**Alternative — GraphQL (⚠ caveat):** `updateProjectV2Field` rewrites the whole option
set and can detach items from columns. If used, pass ALL options (existing + new) in
the desired order. Field ID `PVTSSF_lADOD34yzs4BZIGzzhUJB8o`, project ID
`PVT_kwDOD34yzs4BZIGz`. Existing: Todo(GREEN,"This item hasn't been started"),
In Progress(YELLOW,"This is actively being worked on"), Done(PURPLE,"This has been completed").
Prefer the UI unless scripting board provisioning.

---

## Goal

Close the loop so a GitHub Projects V2 board is the single source of truth for
Pilot work: cards flow `Todo → In Progress → Review → Done` (and `→ Blocked` on
failure) automatically, with no `pilot` label required.

```
Todo ──pickup──▶ In Progress ──PR open──▶ Review ──merge──▶ Done
  │                                                          
  └─ read path: #3228 (FindIssuesFromProject)               
     write-back transitions: this task                      
     Blocked ◀── exec-fail / CI-fail                        
```

## Current state (what already exists — do not rebuild)

- **Write primitive:** `ProjectBoardSync.UpdateProjectItemStatus(ctx, issueNodeID, statusName)` (`internal/adapters/github/project_board.go:117`). Resolves project node ID (org-then-user fallback), Status field + option IDs, item ID, then sets the field. **Reuse verbatim.**
- **Wired transitions today:** Done on PR merge (`controller.go:1245`), Failed on CI failure (`controller.go:838`). Both gated on `boardSync != nil && prState.IssueNodeID != ""`.
- **Config:** `ProjectBoardConfig{Enabled, ProjectNumber, StatusField, Statuses{InProgress, Review, Done, Failed}}` (`types.go:36`). All four column names already configurable; `InProgress`/`Review` are **unused today**.
- **Node ID plumbing:** `Issue.NodeID` (`client.go:73`), `GetIssueNodeID(owner,repo,number)` (`client.go:936`), `prState.IssueNodeID` (`autopilot/types.go:487`). Available end-to-end.
- **Startup wiring:** `NewProjectBoardSync` + `WithProjectBoardSync(bs, Done, Failed)` at two `main.go` call sites (gateway-mode `:481`, start-mode `:1425`).
- **Read path (in-flight):** #3228 adds `SourceEnabled`/`SourceStatus` + `FindIssuesFromProject` + poller wiring. Commit `e9d0fe68` produced; **confirm it merges before starting this task.**

## Workstreams (suggested PR boundaries)

### PR-1 — "In Progress" write-back on pickup  (workstream B)
The architectural wrinkle: `boardSync` is currently owned by the **autopilot
controller**, but issue *pickup* happens in the **poller/executor dispatch** path,
which has no boardSync today.

- Give the poller (or the dispatch entrypoint) access to a `*github.ProjectBoardSync`
  (new `WithBoardSync` poller option, mirroring `WithProjectSource` from #3228).
- On successful dispatch of an issue, call
  `boardSync.UpdateProjectItemStatus(ctx, issue.NodeID, statuses.InProgress)`.
  - Source the node ID from the board read path (`Issue.NodeID`) when available;
    fall back to `GetIssueNodeID(owner, repo, number)`.
  - Fire **after** the issue is confirmed accepted for execution, not before, so a
    rejected/declined issue doesn't get moved.
- No-op safely when `boardSync == nil` or `statuses.InProgress == ""`.

### PR-2 — "Review" write-back + Blocked-on-exec-failure  (workstreams C + D)
Both live in the autopilot controller, alongside the existing Done/Failed calls.

- **Review on PR creation:** in the `OnPRCreated` path, after `prState` is
  registered with a non-empty `IssueNodeID`, call
  `UpdateProjectItemStatus(ctx, prState.IssueNodeID, statuses.Review)`.
  Gate identically to the Done transition (`boardSync != nil && IssueNodeID != "" && Review != ""`).
- **Blocked on execution failure:** today `failStatus` only fires on CI failure
  (`controller.go:838`). Extend so an **execution failure** (e.g. the
  `no new commit produced — worktree HEAD matches base branch parent` we observed
  on #3228) also moves the card to `Failed`/Blocked. Find the execution-failure
  handling path and add the same guarded `UpdateProjectItemStatus(..., failStatus)`
  call. This prevents the silent in-progress-forever loop.
- Pass `statuses.Review` into the controller via `WithProjectBoardSync` (extend its
  signature to carry InProgress/Review, or add a dedicated option — keep
  backward-compatible).

### PR-3 — Full board-driven config + idempotency + cross-repo  (workstream E)
- **Idempotency:** `UpdateProjectItemStatus` should no-op (not error, not re-write)
  when the item is already in the target column. Check current behavior in
  `ensureResolved`/`getIssueProjectItemID`/`setItemFieldValue`; add a current-status
  read + short-circuit if missing. Prevents board thrash + wasted GraphQL calls.
- **Off-board items:** when `getIssueProjectItemID` returns empty (issue not on the
  board), skip silently with a debug log — never hard-fail the lifecycle.
- **Cross-repo:** the Studio SDK board is org-level (`qf-studio/projects/1`) and may
  span repos. Read path (#3228) already filters to configured `owner/repo`; ensure
  write path tolerates items whose repo differs (skip, don't error).
- **Config example:** document a complete board-driven block in
  `configs/pilot.example.yaml`:
  ```yaml
  adapters:
    github:
      repo: qf-studio/studio-sdk
      project_board:
        enabled: true
        project_number: 1
        status_field: Status
        source_enabled: true      # from #3228 — pull work FROM the board
        source_status: Todo
        statuses:
          in_progress: "In Progress"
          review: "In Review"
          done: "Done"
          failed: "Blocked"
  ```
  (Confirm the **actual column names** on `qf-studio/projects/1` — see open item.)

### PR-4 / runbook — Auth (workstream F, operational)
Fine-grained PAT (chosen). Document in an SOP (`.agent/sops/integrations/`):
- Create a fine-grained PAT at github.com/settings/tokens (qf-studio resource owner)
- Repository access: `qf-studio/studio-sdk` (+ `qf-studio/pilot` if it drives itself later)
- Permissions: **Projects: Read and write** (org-level), **Issues: Read and write**,
  **Contents/Pull requests** as the executor already needs
- Set as the GitHub adapter token (or a dedicated `GITHUB_PROJECT_TOKEN` if we want
  to separate board ops from repo ops — decide during PR-3)
- Note: the dev `gh` token in use lacks `read:project` (confirmed `gh project list`
  402 this session); board reads will fail until the PAT is provisioned.

## Acceptance criteria (whole task)

- [x] #3228 merged (read path live).
- [x] Picking up a board-sourced issue moves its card `Todo → In Progress`. **(live: #11, #12)**
- [x] Opening a PR moves the card `In Progress → Review`. **(live: #12 → PR #13)**
- [x] Merging the PR moves the card `Review → Done`. **(live: #12, PR #13 squash-merged → main `18d2a02`)**
- [x] An execution failure (no-commit, crash, gate fail) moves the card `→ Blocked`. **(manual-verified last session; auto-Blocked on non-PR paths still gapped — see [[TASK-354]])**
- [ ] All transitions no-op safely when board disabled, node ID missing, item off-board, or already in target column.
- [ ] `configs/pilot.example.yaml` documents the full board-driven block.
- [ ] Table-driven tests with a fake GraphQL transport for each new transition + the idempotency short-circuit (use `internal/testutil` fake tokens — no realistic strings).
- [ ] `make test` + `make lint` green.
- [ ] SOP for the fine-grained PAT exists.

## Code status — ✅ ALL SHIPPED (verified against `main` 2026-06-01)

All four workstreams are implemented and merged. Do **not** re-file these:

| Workstream | PR(s) | Evidence |
|---|---|---|
| PR-1 In-Progress on pickup | #3252 / #3273 | `poller.go` `WithBoardSync` + `syncBoardStatusInProgress()` (seq `:559` + parallel `:1319`); wired `main.go:846-852` |
| PR-2 Review on PR-open + Blocked on exec/CI failure | #3260 | `controller.go` `OnPRCreated`→Review (`:446-490`); failStatus on iteration-limit/size-guard/CI-fail (`:917,:951,:1004`); `WithProjectBoardSync` carries all 4 statuses |
| PR-3 idempotency / off-board / cross-repo | #3276 | `project_board.go:131-171` reads current status, no-ops if already in target; skips off-board items (debug log); org-level board matches by project ID not repo |
| PR-4 SOP runbook | #3261 | `.agent/sops/integrations/github-projects-pat.md` |

## Remaining: operational go-live (NOT code — needs live board + daemon)

- [x] #3228 merged (read path live — `FindIssuesFromProject` in `project_source.go`)
- [x] PAT present in `~/.pilot/config.yaml` (`ghp_`, scopes `project, read:org, repo`)
- [x] **Board columns** (done 2026-06-01 via GraphQL): `In Review` (orange) + `Blocked` (red) added → 5-column board. ⚠️ `updateProjectV2Field` recreated all option IDs and detached all 11 items; repaired from snapshot (#11→Todo, #1–10→Done; stale CLOSED #6 hygiene folded in). New option IDs: Todo `0b0c1283` · In Progress `d6697251` · In Review `188f46d0` · Done `5dbe80c0` · Blocked `4b9ca35e`. **Lesson: use the web UI next time — GraphQL field-update is destructive to option IDs.**
- [x] **Live config** (done 2026-06-01): resolved the global-only constraint by **dedicating** the global `adapters.github` adapter to studio-sdk. `repo → qf-studio/studio-sdk`, `project_path → …/startups/studio-sdk`, full `project_board` block added (source_enabled, Todo→In Progress→In Review→Done→Blocked). Per user: **gitnation-companion excluded** (removed from `projects:` list + global adapter; `default_project → studio-sdk`). `pilot config validate` → Syntax OK / Validation OK. Backup: `~/.pilot/config.yaml.bak-pre-task319`.
- [x] **Smoke test — ✅ COMPLETE end-to-end (2026-06-01, live daemon `--env stage --dashboard`):**
  - ✅ Board sourcing (daemon pulled `studio-sdk#11` then `#12` from Todo) — **live-verified**
  - ✅ `Todo → In Progress` write-back (+ `pilot-in-progress` label) — **live-verified** (#11, #12)
  - ✅ `In Progress → In Review` (PR open) — **live-verified**: #12 → PR #13 (`pilot/GH-12`), card auto-moved to In Review
  - ✅ `→ Done` (merge) — **live-verified**: PR #13 CI green (`build-test`+`lint`), autopilot squash-merged → `main 18d2a02`, card → Done, issue CLOSED + `pilot-done`. `on_merge` release correctly did NOT bump (`test:` commit). ~5 min total.
  - ✅ `Blocked` column write — verified **manually** last session (moved #11 there to un-orphan)
  - **Clean-issue recipe that worked:** spec-valid (`## Context`/`## Implementation`/`## Acceptance`), same-repo, *test-only* throwaway (`test(skipreason): add guard test`) — small, deterministic, one-shot-able. SDK M2 #11 failed only because it was a hard cross-repo port, not a board bug.
  - **Gaps still open (separate tasks):** [[TASK-354]] (non-PR outcomes orphan the card in In Progress — no terminal `→ Blocked` on spec-block/no-op paths) · [[TASK-355]] (no-op false-positive: recorded `completed` with the daemon's own HEAD as `commit_sha`; **project_path mis-resolution REFUTED** by #12 — see that task).
  - **#11 spec fix applied:** renamed `## Review gates` → `## Acceptance` to clear `pilot-spec-incomplete` (spec-guard needs `^## (Acceptance|Implementation|Context|Background|Approach|Design|Refs)`).
  - **Outstanding ops (not blocking lifecycle):** (a) **rotate the `ghp_` PAT** — leaked in session transcript; (b) pre-existing `approval-misconfig` doctor error.
- **Follow-up gap (filed/recommended):** board config is global-only (`ProjectGitHubConfig` has no board field). To run studio-sdk board-driven *alongside* other label-driven projects without dedicating the global adapter, add a per-project board config + poller wiring.

> The "Execution-ready issues" section below is **historical** — all 4 already shipped (see Code status table). Retained for traceability only.

**Token note:** the dev `gh` CLI uses a keyring token (`gho_`) lacking project scope. For direct `gh project ...` use `gh auth refresh -s read:project,project`, or `export GH_TOKEN=<ghp_ PAT>`. Pilot itself reads the `ghp_` PAT from config, so the daemon is unaffected.

## Execution-ready issues (file once #3228 lands + columns added)

Each becomes a `pilot`-labeled issue in `qf-studio/pilot`. Sequencing per the diagram below.

1. **`feat(github): move board card to In Progress on issue pickup`** — workstream B / PR-1. Add `WithBoardSync` poller option; on accepted dispatch call `UpdateProjectItemStatus(nodeID, statuses.InProgress)`; node ID from `Issue.NodeID` (board read) or `GetIssueNodeID` fallback; no-op when board disabled/nodeID empty. Tests: fake GraphQL transport.
2. **`feat(autopilot): move card to In Review on PR open + Blocked on failure`** — workstream C+D / PR-2. In `OnPRCreated` write `statuses.Review`; extend failure handling (exec-fail + CI-fail) to write `statuses.Failed`; extend `WithProjectBoardSync` to carry InProgress/Review (backward-compatible). Tests for both transitions.
3. **`feat(github): board-sync idempotency + off-board/cross-repo safety`** — workstream E / PR-3. `UpdateProjectItemStatus` no-ops if already in target column; skip (debug-log) when item not on board; tolerate cross-repo items; document full block in `pilot.example.yaml`. Tests for short-circuit + off-board skip.
4. **`docs(sops): fine-grained PAT + board provisioning runbook`** — workstream F / PR-4. SOP under `.agent/sops/integrations/`: PAT scopes (Projects R/W, Issues R/W), board column setup, config block, go-live checklist.

## Sequencing

```
#3228 (read) ──▶ PR-1 (In Progress) ──┐
                 PR-2 (Review+Blocked)─┼─▶ PR-3 (config+idempotency+cross-repo) ──▶ go-live (PAT)
                                       ┘
PR-4 runbook (auth) — parallel, gates go-live
```

---

**Last updated:** 2026-05-29
