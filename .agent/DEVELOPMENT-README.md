# Pilot Development Navigator

**Navigator plans. Pilot executes.**

## WORKFLOW: Navigator + Pilot Pipeline

**This session uses Navigator for planning, Pilot for execution.**

### The Pipeline

```
┌─────────────────┐                          ┌─────────────────┐
│   /nav-task     │  ───── plan ──────────►  │  GitHub Issue   │
│   (Navigator)   │       --label pilot      │  (with pilot)   │
└─────────────────┘                          └────────┬────────┘
        ▲                                             │
        │                                             ▼
        │ iterate                            ┌─────────────────┐
        │ if needed                          │   Pilot Bot     │
        │                                    │   (executes)    │
┌───────┴─────────┐                          └────────┬────────┘
│   Review PR     │  ◄──── creates PR ───────────────┘
│   Merge/Request │
└─────────────────┘
```

### Workflow Steps

| Step | Command | Action |
|------|---------|--------|
| 1. Plan | `/nav-task "feature description"` | Design solution, create implementation plan |
| 2. Execute | `scripts/nav-dispatch-legs.py` on the doc's `### LEG` blocks — SOP [`sops/development/dispatch-task-legs-to-pilot.md`](sops/development/dispatch-task-legs-to-pilot.md) | File each leg as a `pilot` + `no-decompose` issue after a path/probe dry run |
| 3. Review | `gh pr view <n>` | Check Pilot's PR |
| 4. Ship | `gh pr merge <n>` | Merge when approved |

### Quick Commands

```bash
# Plan a feature (Navigator does the thinking)
/nav-task "Add rate limiting to API endpoints"

# Hand off to Pilot — legs in the task doc, filed by the dispatch script
#   (SOP sops/development/dispatch-task-legs-to-pilot.md; one --label, so no-decompose first, pilot second)
python3 scripts/nav-dispatch-legs.py .agent/tasks/TASK-XX-slug.md --checkout qf-studio/pilot=. --only A1 --dry-run
python3 scripts/nav-dispatch-legs.py .agent/tasks/TASK-XX-slug.md --checkout qf-studio/pilot=. --only A1 --label no-decompose
gh issue edit <N> --add-label pilot

# Check Pilot's queue
gh issue list --label pilot --state open

# Review PR
gh pr view <number>

# Merge when ready
gh pr merge <number>
```

### Rules

| Do | Don't |
|----|-------|
| Use `/nav-task` for planning | Write code directly |
| Create issues with `pilot` label | Make commits manually |
| Review every PR before merging | Create PRs manually |
| Request changes on PR if needed | Approve without review |
| Let merged work ride the 16:00 CET release train | Cut ad-hoc releases (incidents only — see Release Cycles) |

### Release Cycles (workflow decision, 2026-07-09 — mem-104)

Work is organized in **cycles** (Linear-style), layered ON TOP of the
Navigator + Pilot pipeline above — planning/dispatch/review/merge are
unchanged; cycles govern **scope and release cadence** only:

1. **Ideate & research** — as before (`/nav-task`, navigator-research agents).
2. **Plan the cycle** — pick the updates that ship this cycle; the cycle
   **ends before the release train**, so scope what can merge by then.
3. **Execute & collect** — dispatch to Pilot; merged PRs **accumulate on
   `main` unreleased**. Merged-but-unreleased is the NORMAL state, not an
   incident (do not "fix" it — see mem-093 for what an actual release wedge
   looks like).
4. **Release** — the scheduled train tags at **16:00 Europe/Berlin**. The
   pilot repo is **daily** (`schedule: "0 16 * * *"`); the other project
   repos are Mon–Fri (`0 16 * * 1-5`). Config in `~/.pilot/config.yaml`.

**The one exception**: incidents. A production-impacting fix does NOT wait
for the train — release ASAP (out-of-band tag is safe; the releaser reads
its baseline live from git tags, mem-093).

**Cutover COMPLETE (2026-07-10)**: pilot repo flipped `on_merge → on_schedule`
after two prerequisites landed — #4150 (append ` (#N)` to squash titles so
`resolveTrainMemberPRs` can resolve members; without it `on_schedule` skips
every tick with "no resolvable member PRs") and #4174 (no-tags-repo first
release). Verified live: scheduler runs `0 16 * * *`, next_run correct, no
release cut on restart. Watch item: the train still skips a repo whose
squash commits predate #4150, or a repo with zero tags.

---

## CRITICAL: Core Architecture Constraints

### 1. Navigator Integration (runner.go)

**NEVER remove Navigator integration from `internal/executor/runner.go`**

The `BuildPrompt()` function MUST invoke `/nav-loop` mode when `.agent/` exists. This is Pilot's core value proposition:

```go
// LocalMode takes priority — checked FIRST (GH-2103, bench val10)
if task.LocalMode {
    return r.buildLocalModePrompt(task)  // problem-solving prompt, no PR constraints
}

// Navigator-aware prompt structure for medium/complex tasks
if useNavigator {
    sb.WriteString("Use /nav-loop mode for this task.\n\n")  // <- NEVER REMOVE
    // ... PILOT EXECUTION MODE override for CLAUDE.md rules
}
```

**LocalMode priority (GH-2103)**: `task.LocalMode` MUST be checked before Navigator detection. Sandbox environments (bench, CI) may have `.agent/` directories that hijack the prompt to Navigator path. LocalMode = problem-solving prompt without PR workflow constraints.

**Incident 2026-01-26**: Navigator prefix was accidentally removed during "simplification" refactor. Pilot without Navigator = just another Claude Code wrapper with zero value.

### 2. Navigator Auto-Init (v0.33.16+)

Navigator is now auto-initialized for projects without `.agent/`. In `runner.go Execute()`:

```go
// Auto-init Navigator if configured and missing
if r.config.Navigator.AutoInit && !initialized {
    r.maybeInitNavigator(task.ProjectPath)  // Creates .agent/ from templates
}
```

Disable via config: `executor.navigator.auto_init: false`

---

## Quick Navigation

| Document | When to Read |
|----------|--------------|
| CLAUDE.md | Every session (auto-loaded) |
| This file | Every session (navigator index) |
| `.agent/system/FEATURE-MATRIX.md` | What's implemented vs not |
| `.agent/system/ARCHITECTURE.md` | System design, data flow |
| `.agent/system/PR-CHECKLIST.md` | Before merging PRs in `--env=prod` mode |
| `.agent/system/jev-gates-roadmap.md` | Before adding a Jev/TypeSafe classifier to any gate: triage rule, UPGRADE vs SKIP verdict per gate, sequencing, shadow rollout protocol |
| `.agent/product/PRICING-MODEL.md` | **What we charge for and why** — $499 flat, 100 tickets included (Sonnet 5.5), own key past the cap; v3 (packs) researched 10-13, TASK-546. Canonical; read before any pricing conversation |
| `.agent/product/LEGAL-TERMS.md` | **Source of truth for `/terms`, `/refunds`, `/privacy` and the licence summary on pilotcloud.dev** — DRAFT 10-14, `[FOUNDER]` fields + lawyer review open; numbers live on the pricing page, never here |
| `.agent/product/UNIT-ECONOMICS.md` | Cost, margin, break-even and customer TCO per tenant — measured, not modelled |
| `.agent/tasks/TASK-XX.md` | Active task details |
| `.agent/sops/*.md` | Before modifying integrations |
| `.agent/sops/operations/resolve-pre-pr-review-hold.md` | A run ended `needs_human` with "pre-PR review: blocking defects not fixed": fix the branch, edit the issue, or close it |
| `.agent/system/references/reference_slack_notifications_routing.md` | Before touching Slack/alert/approval destinations on the box (which channel gets what, the 3 keys, restart rule) |
| `.agent/.context-markers/` | Resume after break |

### Research Reports (research/)

- `research/ai-coding-agent-products-price-extra/report.md` — Cost-plus 1-4% per ticket loses money; keep Pilot's flat plan (47 sources, 2026-10-13)
- `research/many-software-development-agencies-3-15/report.md` — Small software agencies: founder-bought tools, cheap seats, consent-gated agent adoption (45 sources, 2026-10-22)

## Current State

**Current Version:** **v2.314.0** cut by hand 11-06 11:37Z from green main `c7461d04c` (pre-push gate 547 s clean; release run green, published 11:49:52Z, 10 assets; version-sync PR#6151 armed to squash on green; box hot-upgraded 11:54:28Z `from=2.313.1 to=v2.314.0`, `Pre-PR review initialized mode=enforce` survived, Jira adapter `enabled: false` took effect — 0 `SUSPENDED_INACTIVITY` lines after the restart vs ~78/h before). Ships TASK-631 C1 PR#6104 + fix PR#6118 (budget scope api_key) · sdk pin PR#6113 · docs-sync push retry PR#6114 · recorder tests PR#6105 + PR#6124 · TASK-640 GH-5351 guard PR#6128 · TASK-620 L11 PR#6129 · TASK-508 A1 PR#6132 + A2 PR#6140 + minors PR#6146 · TASK-641 PR#6144 + #6130/#6131. **#5955 closed as delivered 11-06** (all four billing-mode legs released; status table posted). Previous: **v2.313.1** auto-cut by the release pipeline 11-05 15:44Z on the PR#6097 merge (ships TASK-638 PR#6095 pre-push gate failure output + minors PR#6097); box hot-upgraded 15:56:06Z (`from=2.313.0 to=v2.313.1`). **Operator flips 11-05:** TASK-631 fallback enabled 15:21Z (below) · **TASK-620 `pre_pr_review.mode: enforce` 15:57:43Z** (graceful restart, queue idle, `Pre-PR review initialized mode=enforce`; stage-3 read after 20 merges) · **Jira adapter `enabled: false` 21:21Z** (8214 `SUSPENDED_INACTIVITY` 503 warnings since 10-30; `pilot config validate` OK; applies on the next restart = next hot-upgrade; backup `config.yaml.bak-jira-off-20261105`). Previous: **v2.313.0** cut by hand 11-05 13:20Z from green main `e1ffa3b33` (ships TASK-631 B1 PR#6070 + minors PR#6072 + B2 PR#6074 usage-limit fallback, TASK-620 L10 PR#6087, TASK-636 G1 PR#6085 + minors PR#6089 + G2 PR#6088; first tag push failed the pre-push gate on a `go test -short -race` flake under load whose failing test the gate's 20-line head cut hid, clean rerun, second push 570 s gate; release run green, six assets, published 13:22:38Z); box hot-upgraded 13:24:50Z (`ver=2.313.0`, 1 proc, uptime preserved); version-sync PR#6092 armed to squash on green. **TASK-631 fallback enabled on the box 15:21Z** (`billing.mode: subscription`, `api_key_command` → SSM `/pilot-fleet/founder-box/ANTHROPIC_API_KEY`, `usage_limit_pause.fallback: api_key`; graceful restart, queue idle, `billing-mode status` = subscription/config). Previous: **v2.312.0** auto-cut by the release pipeline 11-04 14:47Z on the PR#6066 merge (main `9a3c7379b`; ships TASK-634 A1/B1/C1, TASK-633 F5/F6, TASK-620 L9 + test-depth + minors, TASK-631 A2 + fix + A3 + A4, TASK-635 H1 + fix); box hot-upgraded 15:11:50Z (`from=2.311.0 to=v2.312.0`). Previous: **v2.311.0** tag pushed 11-03 17:21Z on green main cf93e82c6 (TASK-633 F4 PR#6036: unset `billing.mode` passes the inherited key through again, fixes the v2.310.0 env-key regression; 654 s pre-push gate, release run green, six assets, F4 line in the notes); box hot-upgraded 17:27:01Z (`from=2.310.0 to=v2.311.0`, 1 proc). Fleet hold from TASK-633 lifted. Previous: **v2.310.0** tagged 11-03 (tag commit 5d3c8f73e; ships TASK-631 A1 PR#6017 + TASK-633 F1/F2/F3 PR#6024/#6025/#6027, TASK-632 PR#6023 open-PR admission refusal, TASK-620 file_minors); box on it by 15:32Z (`ver=2.310.0`, OAuth, 4 tasks completed). **⚠️ Do not roll 2.310.0 to any env-key daemon (hosted tenants, AMI infra#23, SSM hops): unset `billing.mode` strips the inherited `ANTHROPIC_API_KEY` → F4 [pilot#6032](https://github.com/qf-studio/pilot/issues/6032) P1 restores passthrough; wait for the next tag.** Previous: **v2.309.0** tag pushed 11-03 11:34Z from green main 609326ae4 (TASK-585 P2 pilot#6010 + P3 PR#6013, TASK-630 PR#6015 epic children on the grid; 475 s pre-push gate, release run green, six assets); box hot-upgraded by 11:52Z (`ver=2.309.0`, uptime preserved). Previous: **v2.308.0** cut by hand 11-02 11:26Z from green main bf937e9b7 (TASK-628 PR#6003 absent make target fallback + fast-follow PR#6006, TASK-620 L8 PR#6008 minors sink `file_minors` default off); box hot-upgraded by 11:29Z (`ver=2.308.0`, uptime preserved). Release-day fixes: branch protection on main (test + lint required) broke the Sync Docs Version auto-merge step → repo `allow_auto_merge` enabled 11:3xZ, PR#6009 armed to squash on green. Previous: **v2.307.0** cut by hand 11-01 16:45Z from green main dcbbd0ec5 (TASK-627 P1 PR#6000: Slack click line carries `action_ts` + `delivery_delay_ms`, studio-sdk pin v0.38.5); box hot-upgraded 17:02:46Z, 1 proc, `config reloaded`, both reviewers `passes=2`, pre-PR `mode=shadow`. Earlier 11-01 16:01Z: hand restart after a config edit — studio-sdk project got its own `quality:` block (`make test`; backup `config.yaml.bak-sdkgates-20261101`) because the global `make test-changed` gate fails on repos without that target (TASK-628). v2.306.0 cut by hand 11-01 13:15Z from green main 727094685 (TASK-626 PR#5997 reviewer residue); box hot-upgraded 13:19:25Z, 1 proc, `config reloaded`, both reviewers `passes=2`, pre-PR `mode=shadow`. v2.305.0 cut by hand 10-31 21:08Z from green main 9276b5cdf (TASK-624 D1–D4 PR#5987–#5990 + TASK-625 PR#5993 + autopilot fix PR#5994); box hot-upgraded 21:13:03Z, 1 proc, `config reloaded`, both reviewers `passes=2`, pre-PR `mode=shadow`. v2.304.0 cut by hand 10-30 16:14Z from green main 19ee1e854 (TASK-623 P2 #5981 two-pass mechanics); box 16:21:31Z with **`passes: 2` on both reviewers** (`Pre-PR review initialized … passes=2`, `post-merge reviewer enabled … passes=2`; backup `config.yaml.bak-passes2-20261030`). v2.303.0 cut by hand 10-30 13:28Z from green main 2f84f1e39 (TASK-623 #5978 two-pass reviewer; box 13:34:53Z, `passes=1` logged — box still single-pass). v2.302.0 cut by hand 10-30 10:58Z from green main 521243001 (TASK-621 #5973 live-smoke mutation exclusion, TASK-622 #5975 adjacent-code reviewer item); box 11:03:23Z after one asset-missing retry, shadow re-initialised. v2.301.0 cut by hand 10-29 20:28Z from green main 6fce37a31 (founder "let's train now"; carries TASK-620 L3 PR#5966 + fix PR#5970, L5 PR#5963, L7 PR#5969). Box: self-upgrade paused admission 20:32Z, let GH-5971 finish (20:36:39Z), hot-restarted 20:36:45Z, `Pre-PR review initialized mode=shadow` — **L1+L2+L3+L5+L7 live; enforce still off**. v2.300.0 cut by hand 10-29 16:51Z from green main a371c0cfc (TASK-620 L2 PR#5957 + its post-merge fix PR#5959; box hot-upgraded 16:55:27Z, `Pre-PR review initialized mode=shadow` — **L2 live: PR-body section, `pilot-review-rc` label, `pr_reviews` row as pre-pr-reviewer, counters**). v2.299.0 by the train 10-29 15:00Z (members PR#5945/#5948/#5950/#5951/#5953 — TASK-618 tolerant verdict parse, TASK-619 caller tracing, TASK-620 L1 pre-PR review shadow, TASK-616 G3 scope parser; box hot-upgraded 15:08Z, config reloaded, **`Pre-PR review initialized mode=shadow model=claude-opus-5-5`**). v2.298.0 cut by hand 10-29 11:42Z (`c162601d9`; box hot-upgraded 11:49Z after one asset-missing retry; carries the whole reviewer chain — TASK-613 M1/M2/M3, TASK-614, TASK-617, TASK-615 flake fix + follow-ups, TASK-616 G1/G2 changed-package gate). v2.297.0 cut by hand 10-28 14:04Z (#5902/#5904/#5905/#5907/#5909/#5912). v2.296.0 by the train 10-27 15:02Z (PR#5892–#5896 + the reviewer #5868). Earlier versions in `git tag` and the 10-27 markers.

**PRIORITY (founder directive 2026-07-26 — supersedes 07-17):** **SaaS/platform UNPARKED — TASK-405 is active work again.** The 07-17 ordering (pointer delivery → pilot reliability → SaaS parked) held while the dispatch-reliability chain was open; that chain closed with v2.246.0 on 07-25. Pointer and pilot reliability remain live tracks but no longer gate S-milestone dispatch. Memory: `founder-priority-pointer-first-saas-parked` (superseded).

**Recent (Oct 27 – Nov 6 2026; detail lives in `system/saas-roadmap.md`, `system/side-chat-navigator.md`, `tasks/archive/`, memories and git log — do not re-grow this block, replace it):**
- **11-05→11-06 (console session): EVERY DRAWN FIGMA FRAME IS LIVE — DESKTOP BOARD (7-482) LANDED; MOBILE M5–M8 CLOSED; TENANT CREDENTIALS FOUND DEAD; CREDENTIAL-HEALTH PLAN DISPATCHED.** [TASK-639](tasks/TASK-639-desktop-board-page-grouped-list.md): frame 7-482 read via Figma MCP (a project-grouped list, not lanes) → B1 ui#370 → PR#372 (primitives: ds-red/yellow, Chip failed/blocked, `ListRow layout="board"`, uncapped `buildQueue`, `hasParkedOps` passthrough; founder calls: no Direction card, DONE green + BLOCKED yellow everywhere) · B2 ui#376 → PR#377 (list replaces lanes + DnD, drawer Status select on `attemptMove`, `ContextPanel` headed `Performance · N PRs`, `OrgHeaderRow` shared with Home) merged 11-06 07:26Z, deployed 07:31Z, live-verified at 1264 px. [TASK-637](tasks/TASK-637-mobile-on-the-new-design.md): M5 PR#361 RC (e2e wait, composer behind the tab bar) → split M6 ui#362 → PR#364 (chat-route tab-bar clearance, 7 min) + M7 ui#367 → PR#369; the post-merge reviewer's own ui#371 → PR#373 fixed the stale `hasMeta` computed minutes later and sank M8 PR#375 on a rebase → residue rode B2. Two chains died on same-file rebases (PR#366, PR#375): serialize legs per file; wait for the post-merge round. PR#377: founder approval + an external `gh pr merge` eight seconds apart → `PR merged externally`, post-merge review skipped (daemon gap → Pilots). **Incident 11-05 21:21Z:** the founder's Research turn failed `Chat turn failed: unknown: exit status 1`; tenant `i-0289111000cebcee9` (org 425f9f99) had a revoked own Anthropic key (401 `invalid x-api-key`; Claude's stdout `Invalid API key` dropped by the daemon) and a revoked GitHub PAT (401; daemon `/ready` `github:false`) while the console showed both as Connected — the syncingest `recoverConnection` write overrides the fleet ready probe. Two other tenants crash-loop on `PILOT_HOSTED=1 requires upgrade.auto_hot_upgrade=false` (fleet → Nelya/Pilots). → [TASK-640](tasks/archive/TASK-640-credential-health-console.md) K1 [console#488](https://github.com/qf-studio/pilot-console/issues/488) → PR#489 merged 09:33Z, **live on prod 09:50Z** (the console deploys itself: CI → Auto Release → Deploy, ~17 min) — first sweep: anthropic `ok`, github `error` 401 with the reason; review found the sweep writes `status` and fights the ready probe + a stale-over-fresh race → K3 drafted; the post-merge round filed console#490 (nine minors, same items) → K3 residue → K2 ui. Operator: new Anthropic key saved + tenant restarted 09:46Z → **chat works (41 s research turn)**; new org-owned fine-grained PAT (Contents/Issues/PRs RW) + restart 10:57Z → GitHub 200 `push: true`, `/ready` true, poller started — **tenant incident closed**. K3 [console#492](https://github.com/qf-studio/pilot-console/issues/492) → PR#493 merged 11:07Z (0043 `connections.health_status`; the sweep owns health, the ready probe owns `status`; Navigator review APPROVE-w-notes), console-api live 11:19Z with `health_status: ok` on both rows → **K2 [ui#378](https://github.com/qf-studio/pilot-console-ui/issues/378) → PR#379 merged 12:00Z, live 12:06Z** (Fix key / Fix connection with reason + check time; composer disabled on `anthropic_key_invalid`). Pre-PR enforce fixed one blocking (the poll cleared `keyInvalid`); the post-merge round found the inverse — **a lockout: the disabled composer is the only clearing path, saving a key never clears it** — plus mock lockstep, Slack `Fix key`, stale cached health → [ui#380](https://github.com/qf-studio/pilot-console-ui/issues/380) → PR#381 merged 12:26Z (the `:disabled` dropped: notice shown, composer enabled, a retry clears it — K2's "composer disabled" design withdrawn) → review-minor [ui#382](https://github.com/qf-studio/pilot-console-ui/issues/382) → PR#383 merged 12:57Z (save clears the notice, DOM-level recovery test) → **K4 [ui#384](https://github.com/qf-studio/pilot-console-ui/issues/384) → PR#385 merged 14:37Z, live** (seed `keyInvalid` from `GET /api/v1/credentials` on mount, `syncKeyHealth` action, `force` refetch on Connections, `Fix key` Model row only, mock lockstep). PR#385 went red on 1123 green tests: the new DashboardView spec's memory router lacked the `board` route the view links to; the CI fix size guard declined (259 additions > 200) → hand-filed [ui#386](https://github.com/qf-studio/pilot-console-ui/issues/386) in the autopilot-fix shape → Pilot pushed the routes, merged, #386 closed by hand. Navigator's K4 review found a prod regression the reviewers missed: the console `PUT /api/v1/credentials/{key}` returns `{key, last4}` only, so `syncKeyHealth` after a save sees no health and the notice stays — the K4 leg text asserted the response carries health without reading the handler → **LEG K5** (re-read credentials after save and on mount) **dispatched 15:3xZ as [ui#389](https://github.com/qf-studio/pilot-console-ui/issues/389)** after the post-merge fix [ui#387](https://github.com/qf-studio/pilot-console-ui/issues/387) (mock-mode view spec) merged as PR#388 15:13Z, test-only. Navigator review missed the lockout; memory `disabled-gate-whose-only-clearing-path-is-the-disabled-action-locks-out`. Roadmap: U16/C11 Viewer role PARKED by the founder (important, after the first paying tenant); D4 ticket page + drawer = the next UI gap (v4-vocabulary inventory under the design table). Marker `2026-11-06-0740_console-session-board-landed-tenant-credentials-dead.md`.
- **11-04 pm→11-05: BILLING-MODE COMPLETE + v2.312.0 + GATE TIMEOUTS + PADDLE VERIFICATION SUBMITTED.** [TASK-631](tasks/TASK-631-switchable-claude-billing-mode.md): A2 PR#6057 (post-merge blocking → #6058 reopened by hand → PR#6065) · A3 PR#6061 · A4 #6064 → PR#6066 · **v2.312.0 auto-cut 14:47Z, box 15:11Z** · B1 usage-limit fallback #6069 → PR#6070 + minors PR#6072 + B2 #6073 → PR#6074 (fallback gates admission synchronously) — all on main, unreleased. [TASK-635](tasks/TASK-635-autopilot-ci-poll-api-outage-hold-not-terminal-failed.md) H1 PR#6059 + fix PR#6062 in v2.312.0. [TASK-620](tasks/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md) L10 #6079 → PR#6087 (a "PR — body" defect never blocks or files; minors pin in the test file) — found via the console session's 3× `reject_vague` chain; severity-miss + minors-cap=1 data points for the 11-05 enforce call. [TASK-636](tasks/TASK-636-quality-gate-timeout-fail-closed-process-group.md) (scrappydog #6075/#6076, both verified): G1 PR#6085 fail-closed + G2 PR#6088 `internal/procgroup`; community PRs #6077/#6078 (RyoKusnadi) closed with credit. studio-sdk#156 → PR#157: `GH-<n> in:title` search matched "$299" (ui#299 false-done → ui#302); pitfall memory updated; **pin bump v0.38.5 → v0.38.6 dispatched 11-05 16:10Z as [pilot#6106](https://github.com/qf-studio/pilot/issues/6106)**. [TASK-497](tasks/TASK-497-paddle-billing-integration.md): Nelya confirmed console-api prod-0.1.62 + billing live; live smoke step 1 passed, step 2 → 502 `transaction_checkout_not_enabled` → Paddle **verification submitted 12:21Z** (Chrome), domain in review, default payment link empty until approval, discount `SMOKE1105` (24 h), ui#341 → PR#344 (silent 502 fixed); step-01 audit: everything from L7 in place. paddle-sandbox MCP: plugin placeholder unfilled → user-scope server added with the Doppler key. Canary tenant still pending (does not wait on Paddle).
- **11-03 pm→11-04: BILLING-MODE DAY TWO — v2.311.0 OUT, FLEET HOLD LIFTED, ALL TASK-634 + L9 + F5 LEGS MERGED, A2/A3 + F6 + TASK-635 DISPATCHED, PADDLE FLIPPED ON.** [TASK-633](tasks/TASK-633-billing-mode-post-merge-fast-follow.md): F4 PR#6036 hand-merged 16:56Z (own-PR approve refused; merge = approval) → **v2.311.0** tagged 17:21Z, box hot-upgraded 17:27Z; F5 #6038 → PR#6046; F6 #6050 (F1/F2 residue: alert text + CLI runners + reviewer alert, scaffold order, failed-delete discard, cost assertion). [TASK-634](tasks/TASK-634-declined-run-repick-and-evidence-fail-false-fix-issue.md) (GH-6031 post-mortem: evidence FAIL trimmed away → reviewer blocked by contract → Pilot declined with a reason nobody saw → decline re-picked → cap): A1 PR#6039 · B1 PR#6043 · C1 PR#6044 all merged 11-03, unreleased. [TASK-620](tasks/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md): 11-03 read 6/7 criterion-2 numbers pass (42 runs, 88.6 % agreement, RC 23.8 %, median 109 s), day count 3/5 → enforce call 11-05; L9 (review runs after the evidence) #6037 → PR#6045 merged, its test-depth fix PR#6049 + minors PR#6055 (hand-merged 11-04 11:35Z after a GitHub 500 burst failed it in autopilot → [TASK-635](tasks/TASK-635-autopilot-ci-poll-api-outage-hold-not-terminal-failed.md) H1 #6056: API outage = hold + backoff + redrive, not terminal). [TASK-631](tasks/TASK-631-switchable-claude-billing-mode.md) A2 #6052 (runtime_overrides store, GET/POST `/api/v1/billing-mode`, billing_mode_changed alert) → **PR#6057 merged 12:19Z**, post-merge REQUEST-CHANGES → fix #6058 (queued subscription row re-resolves through a later api_key override; own review + repro agree; closed by hand, reopened) → **PR#6065 14:21Z** · A3 #6053 → **PR#6061 13:38Z** · A4 #6064 (gateway honours `set_by`, `pilot status` sends the token) → **PR#6066 14:47Z** — named billing-mode because the console's `/api/v1/billing/...` is Paddle. H1 #6056 → PR#6059 13:07Z + fix #6060 → PR#6062. **v2.312.0 auto-cut by the release pipeline 14:47Z at `9a3c7379b`, box hot-upgraded 15:11Z.** B1 usage-limit fallback (auto-switch to api_key until the reset, revert by the A2 expiry) #6069 → **PR#6070 merged 17:45Z** (`e41ca4097`, unreleased; own review APPROVE-WITH-NOTES). Redeploy-done note to Nelya posted 12:27Z. ui#299 false-done (SDK title search `GH-299` matched "$299" in PR#163) → refiled ui#302, [studio-sdk#156](https://github.com/qf-studio/studio-sdk/issues/156) → sdk PR#157 merged, pin bump pending. Fleet: Nelya re-baked the golden AMI on **2.311.0** (11-04 10:56 CET) and set `BillingEnabled` on; **console-api redeployed 11:28Z** (prod-0.1.60, task def 83): webhook rejects unsigned POST (400) ✓, `/billing/config` live check needs a signed-in session → [TASK-497](tasks/TASK-497-paddle-billing-integration.md) next = live checkout smoke + canary tenant (TASK-495 exit). False fix issues closed by hand: #6031, #6041 (go-proxy flake). Memory `declined-run-repicked-and-evidence-fail-files-false-fix-issue`.
- **11-02→11-03: WAVE 2 (RESEARCH → TICKETS) PLANNED, DISPATCHED AND CODE-COMPLETE IN 24 H; PADDLE SSM WRITTEN; TASK-630 FILED; v2.309.0 RELEASED.** [TASK-585](tasks/TASK-585-wave2-research-to-tickets.md): seven legs (P2 drafts + Go shippable check · P3 reject → SQLite learning + prompt injection · C5 draft store + approve → `pilot` issue · C4 reject write-through · C10 research cap 50 · C11 chat `action` relay (missed by the plan, found by the PR#291 pre-PR review) · U10 Create tickets + draft review). Console live at prod-0.1.60 = main 11-02 20:58Z; UI deployed 11-03 10:48Z after PR#293. Cost of two legs filed without `no-decompose`: three hand-filed revisions (console#474, ui#296/#297) plus a 0039 migration collision (console#465) and a GitHub stuck merge ref on PR#293 (close/reopen fixed it; autopilot had timed out → hand merge). Six memories written (parallel migrations · epic children on shared files · fix issues never close + Blocked-by deadlock · stuck merge ref + CI-timeout terminal · Slack approval cards · reviewer loops on PR-body defects). [TASK-497](tasks/TASK-497-paddle-billing-integration.md): three `BILLING_PADDLE_*` SecureStrings in SSM 11-02 12:31Z, Nelya pinged 13:33 + nudged 11-03 09:37 CET, flip pending. [TASK-630](tasks/TASK-630-monitor-register-epic-subissues-project.md) → pilot#6014 → PR#6015 merged ("(no project)" grid rows). Docs drift sweep e6a85128d (TASK-583/546/584/466, roadmap rows, saas banners). v2.309.0 released 11:37Z, box on it by 11:52Z; Nelya asked 11:54 CET (ts `1793708051.345689`) for the AMI bake → fresh tenant → exit run. [TASK-631](tasks/TASK-631-switchable-claude-billing-mode.md) A1 PR#6017 failed CI on one stale test key; the size guard declined the fix issue → hand-filed pilot#6019 → **PR#6017 merged 12:14Z**; post-merge minors → [TASK-633](tasks/TASK-633-billing-mode-post-merge-fast-follow.md) F1 #6021 / F2 #6022. **Duplicate run found and killed:** autopilot's hold redrive stripped `pilot-needs-human` from #6016 when #6019's commit landed, the poller re-picked it 19 s later and a fresh worktree re-implemented the merged work → killed 12:37Z (`status=infra`) → [TASK-632](tasks/TASK-632-restart-repick-failed-with-open-pr.md) → pilot#6020 (admission refuses an issue whose pilot branch has an open PR). Three memories (hold-redrive re-pick · running rows have no cancel, kill the Claude group not the gate · hand-filed autopilot-fix shape). **14:16Z: `post_merge_review.file_minors` flipped ON on the box (trial)** — the PR#6017 log showed the pre-PR shadow review found 8 of the 9 post-merge minors on the same lines and fixed none: the fix round needs enforce *and* a `[blocking]` tag, and the reviewer tags unmet issue promises `[minor]`; minors had no automatic path. Next APPROVE-WITH-NOTES post-merge review files one `review-minor` issue (cap 1/project/day, auto-dispatched). Severity calibration (issue requirement not met ⇒ blocking) is the open lever before the enforce flip ([TASK-620](tasks/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md)). **Billing mode (TASK-631/633, from scrappydog #5955 triage):** A1 pilot#6016 → PR#6017 merged 12:14Z in 54 min; post-merge nine minors → nav-research confirmed 8 + found 2 (retry ctx from `context.Background()` drops the credential; bootstrap has none) → F1 #6021/PR#6024, F2 #6022/PR#6025, F3 #6026/PR#6027 all merged same day, all in **v2.310.0**. Release audit found the regression my own spec introduced: unset `billing.mode` = subscription = inherited key stripped → every hosted tenant (env key, no OAuth) breaks on upgrade; none exposed (2.282.0/2.277.4) → **F4 #6032 P1** (unset = passthrough) + fleet hold. New SOP `sops/development/dispatch-task-legs-to-pilot.md` replaces the nav-pilot whole-doc flow (one `--label`: `no-decompose` first, `pilot` second). Pitfalls mem-277/280/281. **16:22Z: GH-6031 closed not-planned** — the first `review-minor` chain (#6026 → PR#6027 → #6028 → PR#6030) ended in a false fix issue: the runner's evidence paste showed `FAIL … 585s` from a loaded `-race` run (the `--- FAIL:` line trimmed away), CI was green, the reviewer blocked by contract, Pilot reproduced `ok 552s` on main and `DECLINED:` with a full reason, and the dispatcher re-picked the decline, hit the cap of 2 and posted the generic stall comment; the reason lived only in `daemon.log`. [TASK-634](tasks/TASK-634-declined-run-repick-and-evidence-fail-false-fix-issue.md) A1 (decline stalls on the first run, comment quotes the reason) · B1 (evidence FAIL with green CI → minor) · C1 (trim keeps failure lines, runner logs failing test names); memory `declined-run-repicked-and-evidence-fail-files-false-fix-issue`.
- **11-02: v2.308.0 RELEASED, 18 TASK DOCS ARCHIVED (39 → 21), MAIN PROTECTED, L8 MINORS SINK SHIPPED.** TASK-628 closed: PR#6003 Navigator review APPROVE-w-notes (Opus pre-PR + post-merge had found 3 of 4 notes; the human pass added only test/mutation execution and one caller hop) → fast-follow [#6005](https://github.com/qf-studio/pilot/issues/6005) → PR#6006 merged in 23 min. TASK-620 L8: minor defects had no sink (only REQUEST-CHANGES files an issue) → [#6007](https://github.com/qf-studio/pilot/issues/6007) → PR#6008 merged 11-01 20:11Z: post-merge APPROVE-WITH-NOTES files one batched Navigator-template issue for filtered minors, `file_minors` default off, cap 1/project/day, label review-minor — **box opt-in pending**. Archive sweep (triage by navigator-research + GitHub state): 627/628 + 11 finished (502, 509, 582, 590, 591, 600, 603–606, 623) + 7 stalled (404 stale duplicate deleted; 489/490/491 shipped or obsolete; 492 step 4 → TASK-629 D; 393 plan stays in `system/throughput-roadmap.md`; 496 residue → TASK-584 § Carried). Repo settings: branch protection on main (test + lint required, no force-push, admins not enforced) → broke the release workflow's version-sync `gh pr merge --auto` → repo `allow_auto_merge` enabled; PR#6009 hand-merged 11:42Z. v2.308.0 cut 11:26Z, box hot-upgraded by 11:29Z. TASK-629: M1 PR#6004 + M2 site#28 merged 11-01 evening; 301 live by 12:11 CET, `Location` carries `:443` (deviation, unreported). Memories: `navigator-pr-review-executes-reviewer-not-verified-list-not-a-reread` · `gate-exit-2-no-rule-to-make-target-was-a-claude-fixable-failure` · `hasmakefiletarget-is-the-make-target-probe-never-make-n-or-q`. Open: file_minors opt-in · TASK-620 enforce read 11-05 / 20 runs (~13 so far) · console tag ruleset · board `RATE limit-hits=181` + stale autopilot row 577/GH-531.
- **11-01: SLACK CLICK LATENCY CLOSED, SDK + PILOT SHIPPED SAME DAY, GATE GAP FOUND ([TASK-627](tasks/archive/TASK-627-pr5990-approval-to-merge-latency-research.md) → [TASK-628](tasks/archive/TASK-628-quality-gate-absent-make-target-fallback.md)).** PR#5990's "≈2 h approval→merge" was 2m00s of daemon time after the click; the 2h16m sat before the click reached the daemon, founder cannot recall an earlier click, the Slack card shows the post time not the click → undecidable. Found on the way: the SDK Socket Mode client never handled Slack's `disconnect` (no `envelope_id` → dead branch, 4 blind ~11 s windows/day) and no click time was logged. S1 sdk#152 → PR#153 (founder approved over a pre-PR RC: the gate retry had rewritten the SDK Makefile) → post-merge RC auto-filed sdk#154 → PR#155 restored it → **studio-sdk v0.38.5 by hand** (`pilot release` from the SDK dir resolves the pilot repo — use `git tag` + `gh release create`). P1 pilot#5999 → PR#6000 merged 16:38Z → **v2.307.0 by hand** ("no Mondays"). Root cause of the Makefile damage = global `make test-changed` gate on a repo without the target → box override for studio-sdk + restart 16:01Z, product fix [TASK-628](tasks/archive/TASK-628-quality-gate-absent-make-target-fallback.md) → **#6002 dispatched** (fallback to the gate's default target / native command; "No rule to make target" = runner failure, no Claude retry). TASK-620 sample 10/20 two-pass runs, agreement 10/10.
- **10-27→10-30: REVIEWER PROGRAM — SEVEN CALIBRATION RUNS, PRE-PR REVIEW IN SHADOW, TWO PASSES MEET THE BAR.** 10-27 reviewer knob ON; scrappydog trio shipped (TASK-607/608/609). 10-28: TASK-610–617 (budget, worktree default, memories+checkout+confinement, severity contract, flake, adoption binding) and the box gates 22 → 3–5 min (TASK-616: `-short` + `-trimpath`, then `make test-changed`; its scope never applied until G3 #5952 fixed a `go list` last-line parse on 10-29). 10-29: Opus for the reviewer; runs 1–3 (recall 32–39 %); TASK-618 tolerant parse / TASK-619 caller tracing; founder "why review after CI at all?" → [TASK-620](tasks/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md): **pre-PR review in shadow mode live 15:08Z** (L1), L2 section+ledger+label, L3 fix round, L5 autopilot RC hold, L7 reason ordering — all on the box by 20:36Z (v2.299.0 train, then v2.300.0/v2.301.0 by hand). First side-by-side: pre-PR and post-merge agree on 3 of 3 PRs; one blocking miss (PR#5966, expired git ctx) caught post-merge only. [TASK-621](tasks/archive/TASK-621-evidence-gate-live-smoke-hold-on-mutation-bullets.md) false live-smoke hold on mutation bullets fixed. 10-30: run 4 44.2 % → [TASK-622](tasks/archive/TASK-622-reviewer-adjacent-code-tracing-and-met-failure-case.md) adjacent-code rule → run 5 43.3 % (flat: prompt rules move *which* defects, not how many) → [TASK-623](tasks/archive/TASK-623-reviewer-two-pass-hunk-and-adjacent-union.md) **two passes** → run 6 **51.0 %** → P2 mechanics fix → run 7 **51.0 % stable, 0 pass failures**; **box on `passes: 2` for both reviewers since 16:21Z (v2.304.0)**; calibration track closed (bench report §"Decision inputs after seven runs"). Founder: keep shadow until 20 two-pass runs or 5 working days (≈ 11-03). Box lint pinned to CI's golangci-lint v2.8.0. Lessons in the 10-29 marker: dispatch probes (receiver form, anchored `absent`), no shell operators in evidence commands, stuck check-run recurrence (job rerun), docs pushes need a foreground `git push` for the fast path. **10-31: the four daemon defects traced, dispatched and ALL MERGED same day as [TASK-624](tasks/archive/TASK-624-daemon-defects-stuck-checkrun-restart-repick-hold-binding-body-cut.md) #5983–#5986 → PR#5987–#5990; review notes → [TASK-625](tasks/archive/TASK-625-task624-review-notes-startup-label-strands-crash-orphans.md) #5991 (one D2 regression: startup label guard strands crash orphans) → PR#5993; v2.305.0 by hand 21:08Z, box 21:13Z; then reviewer residue [TASK-626](tasks/archive/TASK-626-reviewer-residue-union-tolerance-empty-claim-marker-paren-exact-lookup-nv-count.md) → #5996** (stuck check-run = `mapCheckStatus` ignores `conclusion` under `in_progress`; restart re-pick = boot reconcile has no open-PR heal + `StartupRecover` strips the label blind; hold binding = `is:pr #N` search, mention-only; body cut = claim text left in `Location`, 200-byte literal cut). Open founder items: `executor.timeout.complex` 60→90m · branch/stash purge · TASK-596 P1 · Nelya PR#577 + infra#23 · PR#5950's two reviewer notes · release notices since v2.298.0 · nine verified NEW-VALIDs in the bench report.
**Caveat CLEARED 2026-08-26 (was wrong since at least v2.149.4):** the long-standing claim that `gateway.Config.LinearWebhookPublicKey` has no YAML decode is **false** — nav-research verified it is fully wired: `internal/adapters/linear/types.go:20` (`yaml:"webhook_public_key"`) → `internal/pilot/linear_key.go` (PEM/PKIX/Ed25519 parse) → `internal/pilot/pilot.go:492` → `internal/gateway/server.go:126`, with the disabled-path logged at `pilot.go:495`. Ed25519 verification works when the key is configured. The real incident of this shape was **`gateway.Config.Auth` (GH-4784)** — validated + defaulted but both production constructors called the auth-less `NewServer` (memory `unwired-config-field-validated-but-dead`). This entry sat stale for months and was repeated as fact; do not reinstate it without re-grepping.

**Earlier (v2.179.0–v2.187.1, June 9–16 2026):** `pilot project add` gh wizard (TASK-282) · board-GraphQL partial-data tolerance (`ExecuteGraphQLTolerant`) · TASK-322 security audit CLOSED · decomposition-integrity waves 1+2 · hot-upgrade self-verify on boot · executor SHA-harvest fix · `safeGo` panic-recovery sweep · board-orphan defense-in-depth · ancestor-tag release dedup. Detail in `git log` + `.agent/tasks/archive/`.

### Autopilot Environments (v1.59.0+)

The `--env` flag selects a deployment pipeline:

| Flag | CI Wait | Approval | Post-Merge | Use Case |
|------|---------|----------|------------|----------|
| `dev` | Skip | No | none | Fast iteration, trust the bot |
| `stage` | Yes | No | none | CI must pass, then auto-merge |
| `prod` | Yes | Yes | tag | CI + human approval required |

```bash
pilot start --env=stage --telegram --github  # Balanced (recommended)
```

---

## 🚀 Pilot Cloud SaaS Program (TASK-405) — ACTIVE

Building the hosted Pilot SaaS using this daemon to build it (Pilot ships its own SaaS via `pilot`-labeled issues).

- **Plan of record + live status**: [`system/saas-roadmap.md`](system/saas-roadmap.md) (v9.9) — S0 ✅ · S1 ✅ · S2 ✅ (exit met 07-27) · H1–H12 ✅ · R-track ✅ · S6-lite ✅ · **S3 BUILT** (exit gated on founder staging inputs → operator deploy per infra PR#25) · **S4 board: waves 1+2 merged** (C1/C2/C7/C3/C4 + kanban UI) · **wave 3 + UI wave COMPLETE 08-05** (C5 · C6 · C8+fixes · C9 · ui#44/45 · TASK-448 metrics+PR#4739/4741 fixes) · **wave 4 in flight 08-06** (C15 PR#108 ✅ · pilot#4748 C14-pilot + #4749 events endpoint queued · C14-console + timeline legs gated on those merging · close verb dropped as already-built)
- **Positioning + roadmap of record (2026-10-21/22)**: [`system/project/project_pilot_cloud_positioning.md`](system/project/project_pilot_cloud_positioning.md) (decisions §1–11) · [`system/project/project_pilot_cloud_roadmap_tickets.md`](system/project/project_pilot_cloud_roadmap_tickets.md) (71 tickets per repo, filed status) · [`tasks/TASK-584-pilot-cloud-execution-roadmap.md`](tasks/TASK-584-pilot-cloud-execution-roadmap.md) (phases A–H, triggers, owners) · wave docs [TASK-582](tasks/archive/TASK-582-wave0-site-unblock-the-sale.md) (site) · [TASK-583](tasks/TASK-583-wave1-design-update-proof-surface.md) (design + proof)
- **Program doc**: [`tasks/TASK-405-pilot-saas-platform.md`](tasks/TASK-405-pilot-saas-platform.md)
- **Design**: [`system/saas-architecture.md`](system/saas-architecture.md) · [`saas-kanban-sync-design.md`](system/saas-kanban-sync-design.md) · [`saas-fleet-design.md`](system/saas-fleet-design.md) · [`saas-asset-research.md`](system/saas-asset-research.md)
- **New repos** (created 2026-07-14, in `~/.pilot/config.yaml`): `qf-studio/pilot-console` (Go control plane) · `pilot-console-ui` (Vue3/Vite/Bun SPA) · ~~`pilot-cloud-infra` (Go CDK)~~ **archived 2026-09-23** (estate deleted; fleet infra = Nelya's `aws-infrastructure-pilot`) — each has its own `CLAUDE.md`
- **Latest handoff marker**: `.agent/.context-markers/2026-11-06_pm-v2.314.0-stage3-read-l4-dispatched-task460-research-task629-m3m4-released.md` (local, untracked; previous: `2026-11-06_am-task508-done-l11-shipped-enforce-day1-jira-off.md`, `2026-11-05_v2.313.1-billing-fallback-enabled-prepr-enforce-live-6098-flake.md`)
- **Systemic**: TASK-407 atomic dispatch-admission claim — **proven + archived 2026-07-30** ([`tasks/archive/`](tasks/archive/TASK-407-dispatch-admission-claim.md); #4265 closed, `duplicate-pr` green since 07-24). TASK-406 shipped → archived.
- **Dispatch SOP**: [`sops/development/dispatch-task-legs-to-pilot.md`](sops/development/dispatch-task-legs-to-pilot.md) — ADOPTED 2026-11-03: legs are `### LEG` blocks filed by `scripts/nav-dispatch-legs.py` (path + probe pre-flight, `no-decompose` then `pilot`); the nav-pilot whole-doc flow is not used here
- **Ops SOP**: [`sops/operations/safe-daemon-restart.md`](sops/operations/safe-daemon-restart.md) — restart is the operator's action; never relaunch the `--dashboard` daemon from an assistant shell (no single-instance lock yet)
- **Quality SOP**: [`sops/quality/real-stack-verify-gates-ui-merges.md`](sops/quality/real-stack-verify-gates-ui-merges.md) — ADOPTED 2026-07-30: UI-surface merges aren't DONE until operator-verified on the live local stack (daemon gates are fixture-only; 5 drift defects in one night prove it)
- **Incident**: [`system/incident-duplicate-cifix-2026-07-14.md`](system/incident-duplicate-cifix-2026-07-14.md) — the Hardening-track root cause

## Active Work

**Source of truth: GitHub Issues with `pilot` label**

```bash
gh issue list --label pilot --state open
gh issue list --label pilot-in-progress --state open
gh pr list --state open
```

### Backlog

Shipped items live in `git log` + `tasks/archive/` — this table holds **open work only**.
Do not append completed rows here.
_Pruned 11-07: TASK-640 (credential health) and TASK-644 (Figma grid container) shipped 11-06 and archived. Pruned 11-06 after v2.314.0: 13 rows left — TASK-485/508/573/574/616/630/631/633/636/642 shipped (docs await the `/nav-task` archive pass); five pre-console rows (dashboard polish/auth/mobile, GitHub App auth, E2E suite) had no task doc and were superseded — the console, TASK-461 and `e2e/` + `make test-e2e` cover them._

| Priority | Topic | Why |
|----------|-------|-----|
| **P1** | **Pricing v3 — honest allowance + prepaid overage packs** ([TASK-546](tasks/TASK-546-pricing-v3-honest-allowance-overage-packs.md)) | 🟢 **11-02: all dispatched legs merged** — pilot #5676/#5677/#5678, console #401–#404 + **#424 ($300 ceiling, packs 25/$99 · 100/$349, 10-22)**, ui#231; daily cap moved to the daemon (TASK-589). Unfiled: pack checkout endpoint + webhook credit, Buy 25/100 UI, copy/catalog/docs (Phase 5). Price stays $499; D1–D11 price-bearing decisions open. |
| **P1** | **Legacy repo bootstrap — real Navigator context on first attach, persisted, priced** ([TASK-549](tasks/TASK-549-legacy-repo-navigator-bootstrap.md)) | 🚀 10-14: #5679 (bootstrap session, trigger default manual) + #5680 (copy-back) dispatched. Verified: scaffold's injected context sections are placeholders nothing fills; no copy-back from worktrees; chat turns are cold research sessions. Next: cold-start bench (Navigator job), then D1–D3 (auto trigger, console snapshot, 10-ticket price). |
| **P1** | **CI-fix chain loses finished work — sweep guard, pre-flight for autopilot-fix, re-arm keeps the branch, upgrade drain** ([TASK-643](tasks/TASK-643-ci-fix-chain-loses-finished-work.md)) | 🚀 **11-07: A1 [#6181](https://github.com/qf-studio/pilot/issues/6181) · B1 [#6182](https://github.com/qf-studio/pilot/issues/6182) · C1 [#6183](https://github.com/qf-studio/pilot/issues/6183) dispatched; D1 (drain, passive wait) [#6187](https://github.com/qf-studio/pilot/issues/6187) dispatched 11-07.** Was: 📋 11-06: four legs drafted after the L4 incident (PR#6165 → #6167 declined → branch swept → #6157 re-armed from scratch; also #6155, ui#392). Salvaged by hand: branch restored, #6168 running. Founder review before dispatch; D (drain) is a founder call.** |
| **P1** | **Reviewer program — pre-PR review replaces self-review + judge; post-merge becomes a sampled audit** ([TASK-620](tasks/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md) · [TASK-612](tasks/TASK-612-post-merge-reviewer-calibration.md) · [TASK-618](tasks/archive/TASK-618-reviewer-tolerant-verdict-parse-raw-on-unknown-evidence-minor.md) · [TASK-619](tasks/archive/TASK-619-reviewer-caller-tracing-cited-met.md)) | 🚀 **11-06: stage-3 read 5/6 (reverted 2/5 fails, n=5) → L4 dispatched [#6157](https://github.com/qf-studio/pilot/issues/6157), L6 held for a re-read on v2.314.0.** 🚀 10-29 14:30Z: reviewer live on **claude-opus-5-5**; 3 calibration runs (RC 3/3, false 0.07, recall 32–36 % — bar 0.5 not met); **#5946 → PR#5948 (13:05Z) and #5947 → PR#5950 (13:59Z) merged, both Opus APPROVE-w-notes** (PR#5950 notes unfiled: NOT-VERIFIED cap overwrites a real entry; an uncited [blocking] "met" defect is demoted); TASK-618/619 archived; **calibration run 4 (20:58Z, v2.301.0): RC 3/3 · recall 32→44 % (bar 0.5 still not met) · false 0.07 · 0 format failures · over-escalation 5→3 · files_read 5.0 — every structural defect of run 3 fixed; the miss class (adjacent code outside the hunks) is unchanged. **Run 5 (10-30, TASK-622 item 2b): 43.3 % — flat; prompt rules move which defects are found, not how many → next lever is budget-shaped. Run 6 (10-30 14:13Z, TASK-623 two passes): 51.0 % — the bar is met by the union of a hunk pass and an adjacent pass. **Run 7 (10-30 16:55Z, after the P2 mechanics fix): 51.0 % again, 0 pass failures — stable; box on two passes for both reviewers since 16:21Z; calibration track closed.** **TASK-620 L1 #5949 → PR#5951 merged 14:42Z, Opus APPROVE-w-notes** (notes: no test reaches the runner's runPrePRReview call; judge-off gate untested; mode≠off with no claude binary leaves a run with no in-run review; memory recall gated on MemoryInjection) · **box `executor.pre_pr_review.mode: shadow` + Opus live since 15:08Z** (backup `config.yaml.bak-prepr-shadow-20261029`); **L2 #5956 → PR#5957 merged 16:05Z**; **first shadow verdict 15:50Z: REQUEST-CHANGES (1 blocking, 1 minor, 6 files, 51 s) — run pushed and merged anyway (shadow)**; L1 persists counts only, so no text to compare until L2's section is on the box; post-merge Opus on PR#5957 REQUEST-CHANGES (counter never incremented in prod; test self-injects) → daemon fix #5958 → PR#5959 merged 16:38Z (APPROVE-w-notes; its own shadow verdict: APPROVE-w-notes 0 blocking/2 minor, 50 s) → **v2.300.0 by hand 16:51Z, box 16:55Z — L2 LIVE**; **L3 #5961 (enforce fix round) + L5 #5962 (autopilot escalate on RC) dispatched 17:10Z; L5 → PR#5963 merged 17:49Z = first PR with the section + first side-by-side: pre-PR (58 s, shadow) and post-merge both APPROVE-w-notes with the same two defects** (escalation-reason ordering breaks the live-smoke bullet parser; hold event untested → **L7 #5967 filed**); **L3 → PR#5966 merged 19:01Z: first pre-PR MISS of a blocking defect** (fix round reads HEAD on an expired 30 s git ctx → every real fix rolls back; pre-PR 0/2 runs, post-merge 1/1 → daemon fix #5968); box lint gate cannot predict CI lint (2.14.0 vs v2.8.0, `required: false`); then enforce (exit criteria, founder) → L4 → L6, L2→L5∥L3→enforce→L4→L6; box step after L1+L2: `executor.pre_pr_review.mode: shadow`. TASK-591 ✅ archived-in-place (knob ON 10-27). **11-01 enforce sample read: 7 of 20 two-pass runs, all five criteria green (0 errors · 7/7 agree · RC 14 % · median +2.6 min · 0 CreatePR failures); next read at 20 runs or 11-05 (5th working day; the doc's ≈ 11-03 was an estimate). Then stage 3, then L4 + L6 (written, not dispatched).** · **L8 ✅ SHIPPED 11-01 — [#6007](https://github.com/qf-studio/pilot/issues/6007) → PR#6008, in v2.308.0, `file_minors` OFF on the box (opt-in pending)**: minors sink — post-merge APPROVE-WITH-NOTES files one batched Navigator-template issue for filtered `[minor]` defects, `file_minors` default off, cap 1/project/day, label review-minor (PR#6003 had 7 minors, 2 real, hand-filed #6005); reviewer gaps section added to the task doc. **11-05 pm: enforce stage-3 tally 5 APPROVE-w-notes + 1 `pilot-review-rc` hold (PR#6117 → closed, refiled #6122 → PR#6124); L11 → #6126 → PR#6129 merged 21:55Z. 11-06 tally: 12 runs, RC 2 (1 fixed, 1 reverted).** |
| **P1** | **Pilot Cloud wave 1 — design update + proof surface** ([TASK-583](tasks/TASK-583-wave1-design-update-proof-surface.md)) | ✅ **11-05: Home + Research match Figma 7-2 / 13-994 on the live tenant; alignment legs U20–U29 all landed (two false deliveries caught: ui#299 title-search `pilot-done`, U22 binding deleted by fix PR#328); still held: C6/U11/U12 (gate: Nelya I3 SES), U9 events (pilot#5810), U26 daemon blocks; wave-1 exit evidence (P11 report, I2) still open.** Was 🚧 11-02: wave-1 code complete — merged + live: U1–U9, U19, X2, W1 (UI at main `659063ae5`, deployed 10-27) · C1, C2, C3, C7, C8, C14, X1 (console `prod-0.1.51` = main) · P1, P9, P10, P16, P18, P11 fix. **Not met**: P11 real-run report on pilot#5793 (hard gate) → I2 pen test (Nelya); canary checks of Run Story / share / Context / cap banner after 10-27. Open: pilot#5810 (P17, design), ui#266 (decision), D1–D5/D7 Figma, C14 review notes unfiled (RLS on `ticket_pack_draws`, 0037 down path, ordinal read outside the org lock). Review policy since 10-26: tier-1 merges only. History: TASK-583 line 3 / git log. |
| **P1** | **Desktop Board page per Figma 7-482 — grouped list replaces the lanes** ([TASK-639](tasks/TASK-639-desktop-board-page-grouped-list.md)) | 📋 11-05: frame read, two legs drafted (B1 primitives → B2 page); both founder calls decided 11-05 (no Direction card; chips follow the frame everywhere); ✅ **11-06 07:31Z: B1 PR#372 + B2 PR#377 landed and live-verified — every drawn Figma frame (7-2, 13-994, 7-482) is now on the tenant.** Next UI gap = ticket page + drawer (roadmap D4). **U16/C11 Viewer role PARKED by the founder 11-05 (important, after the first paying tenant).** Roadmap row U30. |
| **P1** | **Pilot Cloud wave 2 — research → tickets** ([TASK-585](tasks/TASK-585-wave2-research-to-tickets.md)) | ✅ **11-03 10:44Z: CODE COMPLETE — all seven legs on main; console prod-0.1.60, UI deployed 10:48Z.** Exit run blocked on v2.309.0 (cutting) → AMI bake (Nelya) → fresh tenant. Revisions today: console#465/#474, ui#296/#297; C11 fast-follow console#477 open. Six legs written (P2 drafts + shippable check · P3 reject → learning · C5 draft store + approve → `pilot` issue · C4 reject write-through · C10 research cap 50 · U10 Create tickets + draft review); dry-run pre-flight green for P2/P3/C5/C10, C4/U10 held on upstream pins. Seam findings: no `mode` on any chat wire (pills cosmetic → D4 hide), report not stored (sent inline), reject reason dropped by the console today, no Go memory-file writer (D2: SQLite now). Next: post-merge review PR#463 (tier-1); wave-2 exit run on the canary after U10. |
| **P1** | **Pre-PR review enforce: unfixed blocking defects hold instead of opening a PR; re-gate budget; close handler on closed issues** ([TASK-641](tasks/TASK-641-pre-pr-review-unfixed-hold.md)) | ✅ **A1/B1/A2 RELEASED v2.314.0 · C1 RELEASED v2.315.0 (15:15Z) — post-merge fix #6152 sat 2.5 h with a stale `pilot-in-progress` after the owner-death re-arm (poller skips in-progress) → re-armed by hand 15:12Z, retry running; **D1 → [pilot#6163](https://github.com/qf-studio/pilot/issues/6163)** fixes the re-arm; #6152 then parked a second time (self-upgrade admission pause → stale-label cleaner strips in-progress from a queued row → 3 claim-lost drops strip `pilot` → restart stalls queued rows → re-arm sweep escalates to needs-human): **ALL LEGS MERGED 11-06: D1 PR#6170 · D2 PR#6178 · D3 PR#6179 · C1 fix PR#6180 (unreleased → 11-07 16:00 tick). Archive after the tag.** Origin GH-6109 / PR#6117 (first enforce RC): 5m re-gate cap could not pass this repo's gates; the run opened a PR on every fix outcome but `fixed`; `notifyExternalClose` lied on closed issues. D1: `on_unfixed: hold` default. Process follow-ups: CLAUDE.md § Session checklist · navigator#40/#41 (hooks) · #42 merged PR#44 · #43 red main outside CI. |
| **P1** | **Bootstrap unit economics — deep-research cost per repo size, cap, degrade path** ([TASK-581](tasks/TASK-581-bootstrap-unit-economics.md)) | 🚀 10-21: harness dispatched → [#5788](https://github.com/qf-studio/pilot/issues/5788) (runner over 4 public repos S/M/L/XL × 3 stages, prompt files, report tool, fixture tests; no live spend in the executor). Live 30-session matrix = operator step on the box (≤ $250 guard). Provisional cap $25 soft / $40 hard per repo; degrade = scope by churn → sample → min 5 tickets. D1–D5 open for the founder. Found: `modelPricing` prices Opus 5.5/Sonnet 5.5 ~25–50% high (unfiled). |
| **P1** | **Pilot SaaS platform** ([TASK-405](tasks/TASK-405-pilot-saas-platform.md)) | S0–S2 ✅ · S3 exit met local-first 08-18 · S4 build complete · **S4 exit path UNBLOCKED 09-21: console-api + reconciler LIVE on Nelya's `pilot-fleet` (ECS, `prod-0.1.0`, factory off)** — the minimal-EC2 route ([TASK-490](tasks/archive/TASK-490-s4-minimal-invpc-console.md)) is superseded by the ECS transfer. **Domain decided `pilotcloud.dev`.** **09-25: first fleet tenant provisioned + shipped twice on the hosted path; board live after enabling the sync workers in prod (infra#14) — S4 exit evidence on the fleet.** Next: **Paddle L7** (sandbox MCP key → $299 price object → live checkout) → console model pin. Email track (TASK-501) shipped, smoked and archived 09-29. |
| **P1** | **Fleet TO-BE alignment (Nelya)** ([TASK-495](tasks/TASK-495-fleet-to-be-alignment-nelya.md)) | ✅ **EXIT EVIDENCE MET 09-25**: reconciler-provisioned tenant `i-0289111000cebcee9` shipped two issues end-to-end; tenant id posted to Nelya for her account-side checklist (Slack draft). Old-estate backup chain verified closed 09-25. Open on her side: none blocking. Ours: infra#16 (replicas=1) via her pipeline = our dispatch. |
| **P1** | **Paddle Billing integration** ([TASK-497](tasks/TASK-497-paddle-billing-integration.md)) | Code complete, L6 smoke passed 09-14. **L7 started 09-29: sandbox key in Doppler, sandbox price $499 `pri_01m3rf7qws3gzscj9fttg4aywe` (decided 09-30).** **Nelya template ask POSTED 09-30 09:57Z** (thread `1790762207.809729`; flag-off gated, values later). **10-09: live $499 twin `pri_01m4g870rh98m207cjwhjn8taq` + live client token created via paddle-live MCP; token in Doppler `prd`.** Next, founder (dashboard): live API key · notification destination (`console.pilotcloud.dev/api/v1/billing/webhook`, `subscription.*` + `transaction.completed`) · submit checkout domain · payment link. Nelya template ask unanswered since 09-30 — nudge. Then point prod at live, readiness audit. |
| P2 | **Console Docs page — live verification** ([TASK-466](tasks/TASK-466-console-docs-page.md)) | Page renders through the edge (09-22 founder login) but the console DB has no tenant yet → the real `.agent`-tree proof is TASK-499 L3's acceptance (first fleet tenant). infra#51/TASK-498 closed superseded. |
| **P1** | **Console rail implementation** ([TASK-478](tasks/TASK-478-console-rail-implementation.md)) | 11 approved designs → shipped surfaces. **Build-out COMPLETE 08-15 (overnight run): all 16 autopilotable legs executed + reviewed.** Blocked on founder morning sequence (approve PR#67→72→74→76 · close #73/#77 to arm retries · label ui#78 after) → then GH-69/GH-75 retries re-land · **real-stack verify batch UI-2..12** (SOP; blocked on GH-75 re-land) · CON-5 billing portal (founder Stripe gate) · copy pass ($299 · PR#72 "Includes" line · support@ mailto). Daemon-side follow-ups FILED 2026-08-20: [pilot#5027](https://github.com/qf-studio/pilot/issues/5027) merge-time ancestry check (stacked-superset guard, extends GH-4872) + [pilot#5028](https://github.com/qf-studio/pilot/issues/5028) base-presence check before claim (see pitfall `sequential-gates-on-execution-not-merge-fastfollow-misbase`; 4 family incidents in 6 days, nav-research verdict: both fixes needed). |
| P2 | Delivery-evidence audit — false-success class ([TASK-460](tasks/TASK-460-delivery-evidence-false-success.md)) | 🔎 **11-06 research read**: guard-vs-shape table in the doc; top fix already in sdk v0.38.6, v0.38.7 pin filed [#6158](https://github.com/qf-studio/pilot/issues/6158); next-leg candidate = diff-coverage hold under enforce (founder call). Split from TASK-459 by founder scope call 08-08: green CI is not proof the requested change shipped (`mem-151`: scaffold-only PR merged green, parent auto-closed, zero requirements delivered). Planned, NOT dispatched; TASK-459 Phase 4's inventory hook feeds it the success-side site rows. Candidate legs: diff-surface check · ACs fail-when-unwired · epic-collapse guard. |
| P3 | Audit §3 Wave 4+ candidates | Not yet decomposed: `RecordAPIError` wiring beyond github · `AlertTypeOOMKilled` · multi-gate scanner phase discipline · subprocess migration end-to-end validation · `autopilot` adapter coupling refactor · SQL `withTx` helper · generic `Poller[T]` extraction · `Releaser` frozen-at-startup fix. Source: `.agent/audits/AUDIT-2026-05-25.md` §3. |

**Operator-parked (not autopilotable):** TASK-576 leg D box steps (enable the opt-in Telegram PR-notice key; correct the Slack-routing reference row) — doc archived 10-26, steps still open · ~~restart the hosted daemon~~ **DONE 09-07 09:18Z** — box rebuilt from main (`v2.273.0-23-ge054b01c`, carries #5339–#5347) and restarted; `orchestrator.receipts_digest` active (scheduler started, catch-up digest for 09-06 delivered to Telegram 283716179; next 18:00 Europe/Berlin). daily_brief still on America/New_York (arrives 14:00 CEST). ~~`PILOT_DOCS_PAT` workflow scope~~ obsolete since TASK-494 (sync never writes `.github/`) · **domain pick** (`pilot.build` premium $720/yr · `pilot.engineering` $83 · `pilothq.dev` ~$15; = OIDC issuer, sticky) · branch protection on `qf-studio/pilot` main (TASK-405 founder decision 7 — main is currently unprotected) · EBS restore DRILL (runbook exists at pilot-cloud-infra `docs/RESTORE-RUNBOOK.md`; the drill itself is operator work) · tenant box `i-0a3bf271d598196ca` still on **2.259.3** (predates TASK-485 daemon legs — fleet images are immutable by invariant, rolling-upgrade machinery is console#216; operator binary swap is the interim for Phase 4) · rotate ALL box tracker/API tokens (founder-planned; Jira token exposed in a 08-26 session; **10-19: the laptop config's Slack bot+app, Telegram bot and Discord bot tokens printed into a session transcript — rotate all four**) · infra#2 Golden AMI v2 (stale claim corrected 08-26: `aws-infrastructure-pilot` IS in the box config — issue itself needs re-triage) · console#45 (`pilot-spec-incomplete`/`blocked` since 07-24 — needs rewriting into an implementable spec). NOTE 08-26: infra PR#27 `cdk deploy` DONE at some point — FleetVpc NAT verified at 1, `Environment=fleet` tag live in cost view.

---

## Project Structure

```
pilot/
├── cmd/pilot/           # CLI entrypoint
├── internal/
│   ├── gateway/         # WebSocket + HTTP server
│   ├── adapters/        # Linear, Slack, Telegram, GitHub, Jira
│   ├── executor/        # Claude Code process management + alerts bridge
│   ├── alerts/          # Alert engine + dispatcher + channels
│   ├── memory/          # SQLite + knowledge graph
│   ├── config/          # Configuration loading
│   ├── dashboard/       # Terminal UI (bubbletea)
│   └── testutil/        # Safe test token constants
├── orchestrator/        # Python LLM logic
├── configs/             # Example configs
└── .agent/              # Navigator docs
```

## Key Files

### Gateway
- `internal/gateway/server.go` - Main server with WebSocket + HTTP
- `internal/gateway/router.go` - Message and webhook routing
- `internal/gateway/sessions.go` - WebSocket session management
- `internal/gateway/auth.go` - Authentication handling

### Adapters
- `internal/adapters/linear/client.go` - Linear GraphQL client
- `internal/adapters/linear/webhook.go` - Webhook handler
- `internal/adapters/slack/notifier.go` - Slack notifications
- `internal/adapters/slack/socketmode.go` - Socket Mode client + Listen()
- `internal/adapters/slack/events.go` - Event types + envelope parsing

### Executor
- `internal/executor/runner.go` - Claude Code process spawner with stream-json parsing + slog logging
- `internal/executor/alerts.go` - AlertEventProcessor interface (avoids import cycles)
- `internal/executor/progress.go` - Visual progress bar display (lipgloss)
- `internal/executor/monitor.go` - Task state tracking

### Alerts
- `internal/alerts/engine.go` - Event processing, rule evaluation, cooldowns
- `internal/alerts/dispatcher.go` - Multi-channel alert dispatch
- `internal/alerts/channels.go` - Slack, Telegram, Email, Webhook, PagerDuty
- `internal/alerts/adapter.go` - EngineAdapter bridges executor to alerts engine

### Dashboard
- `internal/dashboard/tui.go` - Bubbletea TUI with token usage, cost, task history

### Memory / Testing
- `internal/memory/store.go` - SQLite storage
- `internal/memory/graph.go` - Knowledge graph
- `internal/testutil/tokens.go` - Safe fake tokens for all test files

## Development Workflow

**Default: release then upgrade — don't run ad-hoc local builds.**

```bash
make test
make fmt && make lint
```

**Cycle-gated exception (2026-07-10):** to run merged-but-unreleased `main`
on the daemon *without* cutting a release (release cycles hold work for the
16:00 train), build from a **detached worktree at `origin/main`** and install
to the daemon's path — NOT the root, NOT `make install` (~/go/bin), NOT brew:

```bash
git worktree add --detach /tmp/pilot-build origin/main
cd /tmp/pilot-build && make build          # bin/pilot, version stamped from git describe
cp -p ~/.local/bin/pilot ~/.local/bin/pilot.bak-<rev>   # rollback
cp bin/pilot ~/.local/bin/pilot            # daemon runs ~/.local/bin/pilot (mem: binary path)
git worktree remove --force /tmp/pilot-build
# restart daemon in the zellij `pilot` pane: pilot start --dashboard --github --telegram --tunnel --replace
```

Config is external (`~/.pilot/config.yaml`) — the new binary shares it
unchanged. Building never releases (release = tag push only). Verify the
running binary with `go version -m ~/.local/bin/pilot | grep -E 'main.version|vcs'`.

## Release Workflow

```bash
# Tag-only: GoReleaser CI handles the rest
git tag v0.X.Y && git push origin v0.X.Y

# Upgrade to new version
pilot upgrade
```

**Fresh Install:**
```bash
curl -fsSL https://raw.githubusercontent.com/qf-studio/pilot/main/install.sh | bash
```

**Known Issue (GH-204):** Install script doesn't auto-configure PATH. Users must add `~/.local/bin` to PATH or open new terminal.

## Configuration

Copy `configs/pilot.example.yaml` to `~/.pilot/config.yaml`.

Key per-adapter env vars:
- `GITHUB_TOKEN` - GitHub polling + PR creation
- `LINEAR_API_KEY` - Linear webhook adapter
- `SLACK_BOT_TOKEN` - Slack Socket Mode adapter
- `TELEGRAM_BOT_TOKEN` - Telegram adapter

## CLI Flags

### `pilot start`
- `--env=ENV` - Enable autopilot mode: `dev`, `stage`, `prod`
- `--dashboard` - Launch TUI dashboard with live task monitoring
- `--telegram` - Enable Telegram polling
- `--github` - Enable GitHub polling
- `--slack` - Enable Slack Socket Mode
- `--daemon` - Run in background
- `--sequential` - Wait for PR merge before next issue (default)

## Documentation Loading Strategy

1. **Every session**: This file
2. **Feature work**: Task doc in `.agent/tasks/`
3. **Architecture changes**: `.agent/system/ARCHITECTURE.md`
4. **Integration work**: Relevant adapter code
