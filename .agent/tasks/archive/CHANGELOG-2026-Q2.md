# Pilot Changelog — 2026 Q2 (April)

Archived from `.agent/DEVELOPMENT-README.md` on 2026-04-30.
For context see: git log, GitHub releases, or `.agent/tasks/archive/`.

---

### 2026-04-30

| Item | What |
|------|------|
| **v2.103.1** | `refactor(dashboard)`: banner polish + reintegrate splash inside main tea.Program. Solid horizontal rule replaces tick separator. 3-space inner gutter matches AUTOPILOT/QUEUE/HISTORY. Drop duplicate "PILOT" inside the frame; banner now leads with the version. Strip `claude-`/`gpt-` vendor prefixes from model IDs (`OPUS-4-7 / SONNET-4-6`). Smart adapter chip overflow: pack DAEMON + active adapters first, then inactives until full, then `+N idle` summary. Splash now folded into the main dashboard model — no alt-screen flicker, no init-log scroll between splash and dashboard. `--no-splash` flag removed. Released directly from `fix/dashboard-avionics-wiring` (PR #2459). |
| **v2.103.0** | `feat(dashboard)`: avionics-style TUI redesign — splash boot screen with animated lamps, 3-line bordered banner with version/env/model stack/adapter status dots/uptime/UTC clock, autopilot panel rework with STATE/PR/AGE block, CI/MERGE/RETRY mini-gauges, and 5-node pipeline rail (GH-2454 epic, GH-2455 sub-issue, PR #2456). Initial release shipped with three wiring gaps (logo persisted in steady state, banner state never populated, terminal pipeline stage missing marker) — fixed in v2.103.1 follow-up. |
| **Design pattern** | Pipeline rail switched from connector-based marker to lamp-per-node (`✓` done / `●` current / `○` pending). Connector approach left terminal stages with no current marker because there's no trailing connector; lamp-per-node always shows position. |
| **Banner adapter semantics** | `●` filled = configured AND CLI flag passed this run; `○` empty = configured but no flag; hidden = not in `cfg.Adapters`. Implemented via cobra `cmd.Flags().Lookup(name).Changed` check in `applyDashboardBannerMeta`. |
| **Splash UX gotcha** | First splash implementation used a separate `tea.Program` that ran before the dashboard's program. Caused alt-screen flicker: splash shows → exits alt screen → init logs scroll in normal terminal → dashboard re-enters alt screen. Fix: fold splash into the dashboard model itself with `EnableSplash()` + `splashFrame` + a `splashTickMsg` ticker; single program lifecycle, single alt-screen. |

### 2026-04-26 to 04-27

| Item | What |
|------|------|
| **v2.100.4** | `fix(ci)`: docs deploy chained-trigger fix — `docs-version-sync.yml` resolves token via PAT-or-GITHUB_TOKEN; `sync-docs.yml` adds `workflow_dispatch` (GH-2423, PR #2424). Wires future releases to auto-deploy `pilot.quantflow.studio` end-to-end without manual nudge. |
| **v2.100.3** | `fix(executor)`: send OpenCode `model` as `{providerID, modelID}` object — re-do of closed external PR #2408. Modern OpenCode (≥1.4.x) requires this; pre-fix Pilot couldn't send any message at all (GH-2407, GH-2413, PR #2414). |
| **v2.100.2** | `fix(executor)`: OpenCode response parsing — decodes `{info, parts}` shape from `POST /session/:id/message`, surfaces text parts as `Output`, tool/step parts as BackendEvents (GH-2409). Without this, even after schema fix, runs reported success with empty Output → silent failure downstream. |
| **Pipeline gotcha closed** | "GitHub Actions is not permitted to create or approve pull requests" — root caused to **org-level** setting (qf-studio org), not repo-level. Org-level toggle blocked all repo-level overrides. Memory updated. |
| **Chained-trigger pattern** | `peter-evans/create-pull-request` + `gh pr merge --auto` using `GITHUB_TOKEN` does NOT trigger downstream `push`-event workflows (well-known limitation). Fix: pass a PAT (`PILOT_DOCS_PAT`) instead. PAT secret created with fine-grained scope: `Contents: r/w`, `Pull requests: r/w` for qf-studio/pilot only. Annual rotation reminder needed. |
| **Ghost-close pattern recurrence** | PR #2414 and #2424 both got `pilot-done` label set on parent issue while PR remained OPEN with green CI. Autopilot eventually merged both, but the desync makes status reporting unreliable. Logged for follow-up but no fix queued. |
| **Bench / TASK-26** | No movement — feat/aws-bench branch parked; main branch active for production work. |

### 2026-04-18

| Item | What |
|------|------|
| **v2.99.1** | `fix(executor)`: self-review `--resume` fallback mirrors Qwen logic + `sanitizeFilename` strips path separators (GH-2377, PR #2378) |
| **v2.99.0** | `refactor(autopilot)`: remove dead `prod-X.Y.Z` tag auto-push — `sync-docs.yml` already handles GitLab deploy (GH-2374, PR #2375 reverts #2370) |
| **v2.98.1 / v2.98.0** | Executor env injection of `api_base_url`/`default_model`/`api_auth_token` into Claude Code subprocess (GH-2287/GH-2371); `default_branch` honored (GH-2286); conventional-commit rewrite suggestion after 2nd reject; docs version sync unblocked manually via [PR #2373](https://github.com/qf-studio/pilot/pull/2373) (GitHub Actions lacks PR-create permission at repo level) |
| **Jira search migration** | GH-2289 deprecated `/rest/api/2|3/search` → `/rest/api/3/search/jql` (PR #2376, ghost-abandoned on first retry then recovered via escape-hatch dispatch) |
| **Known gotcha** | `docs-version-sync.yml` can't auto-PR after release — `GITHUB_TOKEN` lacks PR-create permission (repo Settings toggle required) |
| **Reverted premise** | [PR #2370](https://github.com/qf-studio/pilot/pull/2370) shipped on a wrong assumption: `prod-X.Y.Z` GitHub tags trigger nothing — deploy lives in `sync-docs.yml`'s tag-push to GitLab. v2.99.0 reverts. |

### 2026-04 (between 2026-03-13 and 2026-04-17)

| Cluster | Highlights |
|---------|------------|
| **Autopilot hardening** | `ScanExistingPRs` no longer clobbers `RestoreState` on startup; idempotent merge-completion notification (dedup re-entry); skip `pilot-retry-ready` when `pilot-done` already set; periodic external-merge scan (GH-2251); cross-repo PR release-op fix (GH-2243); `notifyExternalClose` now closes parent (GH-2198); strip `GH-XXXX` prefix from squash titles for conventional-commit detection (GH-2312); post success comment on merge close (GH-2297) |
| **Executor / non-Anthropic providers** | `default_model` + `api_base_url` for Z.AI/GLM/OpenRouter/LiteLLM (GH-2287, TASK-24); fix bare `NewRunner()` callsites ignoring executor config (GH-2286); cache-aware token tracking + cost estimation (GH-2164); 1M context window control + updated budget defaults (GH-2163); OOM/SIGKILL handling for Claude Code subprocess on large Navigator contexts (GH-2324); gate PR creation on conventional-commit title (GH-2325); signal executor mode + classify no-diff (GH-2328); persist backend stderr + final message on failure |
| **Dispatcher / poller** | `hasLiveWorker` guard on running-task reaper (GH-2331); persist `Task.Labels` across queue round-trip (GH-2326); delete orphan rows for already-completed tasks; check execution store before re-dispatch (GH-2242); skip retry of closed issues with stale `pilot-failed` (GH-2252); auto-retry stuck `pilot-failed` (GH-2176); clear stale `pilot-in-progress` on startup (GH-2301); retry grace period + TaskChecker (GH-2201); resolve stale-threshold defaults treating 0 as unset (GH-2226) |
| **Epic / sub-issues** | Validate sub-issue titles — reject LLM analysis-style titles (decomposition); native sub-issue GraphQL linking (GH-2210/2211/2212); merge-wait wired via `SubIssueMergeWaitFn` (GH-2178/2179); worktree isolation for sub-issues (GH-2178); sub-issues branch from real repo (GH-2177); `gh pr create --head` to bypass dirty worktree check |
| **Dashboard** | Adapter poller tasks visible in gateway+dashboard mode (GH-2291); refresh history/metrics from DB every 5s (GH-2248) and stop wiping sparklines (GH-2280); gate stdout prints on dashboard mode (GH-2333); zero-poller startup warning (GH-2307) |
| **Adapters** | GitLab: string type for `MergeRequest.detailed_merge_status` (#2295), MR creation fixes (#2271), parallel `OnIssue` with result (#2236); GitHub: adapter-specific PR/MR number extraction (GH-2293); auto-retry issues with `pilot-retry-ready` (GH-2276); Discord greeting-prefix misclassification (#2162); broken Discord invite link (#2231); GitHub issue templates (GH-2298) |
| **Release / packaging** | GoReleaser `brews` section for Homebrew auto-publish (GH-2188); migrate tap to `qf-studio/homebrew-pilot` (#1); LLM-generated GitHub release notes summary (GH-2173); `pilot doctor` GitHub config validation (GH-2304/2306); install script jq-based version parsing (GH-2260) |
| **Bench / Harbor** | Self-verification harness for `PilotAgent` (#2238); remove oracle test access (GH-2237 — Harbor compliance); AWS orchestrator signal handling (GH-2267); always regenerate stale task manifest (GH-2269) |
| **Alerts** | `task_stuck` Slack flood fix — wire progress events, per-rule cooldown, orphan cleanup (GH-2204) |
| **Docs / project** | Repo rename `alekspetrov` → `qf-studio` across non-Go files (GH-2196), llms.txt URLs (GH-2192), brew tap refs (GH-2189); navbar version-sync workflow trigger fix via `workflow_dispatch` (GH-2206); Terminal-Bench #1 ranking + flow diagram fixes; epic merge-wait docs (GH-2183); Navigator-only rules scoped to interactive sessions (#2327) |
