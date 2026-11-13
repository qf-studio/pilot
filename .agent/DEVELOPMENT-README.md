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

**Current Version:** **v2.316.0** cut by hand 11-09 09:39Z from green main `34f4b81cd` (pre-push gate clean; release run green, published 09:42Z, 6 assets); box hot-upgraded 09:47:20Z, config reloaded, `Pre-PR review initialized mode=enforce`. Ships TASK-647 (A1 PR#6212 · B1 PR#6216 + fix PR#6221 · minors PR#6228) · TASK-649 (PR#6225 · PR#6229) · TASK-620 L6 sampling PR#6235 · TASK-643 E1 PR#6237 · #6172 owner-death re-arm PR#6224 · hooks in worktrees PR#6231 · pre-push merge-base PR#6236. **Box config 11-09 09:33Z** (backup `config.yaml.bak-nav-cichecks-samplerate-202611090933`): `post_merge_review.sample_rate: 5`; the `Navigator` project gets `ci_checks: required_checks: []` (it inherited the global `[test, lint]` → `config_mismatch` on every PR; warning filed as [#6239](https://github.com/qf-studio/pilot/issues/6239)). Previous: **v2.315.2** published 11-08 15:04Z (TASK-620 L4 · TASK-643 B1 · TASK-646).

**PRIORITY (founder directive 2026-07-26 — supersedes 07-17):** **SaaS/platform UNPARKED — TASK-405 is active work again.** The 07-17 ordering (pointer delivery → pilot reliability → SaaS parked) held while the dispatch-reliability chain was open; that chain closed with v2.246.0 on 07-25. Pointer and pilot reliability remain live tracks but no longer gate S-milestone dispatch. Memory: `founder-priority-pointer-first-saas-parked` (superseded).

**Recent (Oct 27 – Nov 6 2026; detail lives in `system/saas-roadmap.md`, `system/side-chat-navigator.md`, `tasks/archive/`, memories and git log — do not re-grow this block, replace it):**
- **11-04 pm→11-05: BILLING-MODE COMPLETE + v2.312.0 + GATE TIMEOUTS + PADDLE VERIFICATION SUBMITTED.** [TASK-631](tasks/archive/TASK-631-switchable-claude-billing-mode.md): A2 PR#6057 (post-merge blocking → #6058 reopened by hand → PR#6065) · A3 PR#6061 · A4 #6064 → PR#6066 · **v2.312.0 auto-cut 14:47Z, box 15:11Z** · B1 usage-limit fallback #6069 → PR#6070 + minors PR#6072 + B2 #6073 → PR#6074 (fallback gates admission synchronously) — all on main, unreleased. [TASK-635](tasks/archive/TASK-635-autopilot-ci-poll-api-outage-hold-not-terminal-failed.md) H1 PR#6059 + fix PR#6062 in v2.312.0. [TASK-620](tasks/archive/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md) L10 #6079 → PR#6087 (a "PR — body" defect never blocks or files; minors pin in the test file) — found via the console session's 3× `reject_vague` chain; severity-miss + minors-cap=1 data points for the 11-05 enforce call. [TASK-636](tasks/archive/TASK-636-quality-gate-timeout-fail-closed-process-group.md) (scrappydog #6075/#6076, both verified): G1 PR#6085 fail-closed + G2 PR#6088 `internal/procgroup`; community PRs #6077/#6078 (RyoKusnadi) closed with credit. studio-sdk#156 → PR#157: `GH-<n> in:title` search matched "$299" (ui#299 false-done → ui#302); pitfall memory updated; **pin bump v0.38.5 → v0.38.6 dispatched 11-05 16:10Z as [pilot#6106](https://github.com/qf-studio/pilot/issues/6106)**. [TASK-497](tasks/archive/PILOT-CLOUD-TASKS-MOVED.md): the infra contractor confirmed console-api prod-0.1.62 + billing live; live smoke step 1 passed, step 2 → 502 `transaction_checkout_not_enabled` → Paddle **verification submitted 12:21Z** (Chrome), domain in review, default payment link empty until approval, discount `SMOKE1105` (24 h), ui#341 → PR#344 (silent 502 fixed); step-01 audit: everything from L7 in place. paddle-sandbox MCP: plugin placeholder unfilled → user-scope server added with the Doppler key. Canary tenant still pending (does not wait on Paddle).
- **11-03 pm→11-04: BILLING-MODE DAY TWO — v2.311.0 OUT, FLEET HOLD LIFTED, ALL TASK-634 + L9 + F5 LEGS MERGED, A2/A3 + F6 + TASK-635 DISPATCHED, PADDLE FLIPPED ON.** [TASK-633](tasks/archive/TASK-633-billing-mode-post-merge-fast-follow.md): F4 PR#6036 hand-merged 16:56Z (own-PR approve refused; merge = approval) → **v2.311.0** tagged 17:21Z, box hot-upgraded 17:27Z; F5 #6038 → PR#6046; F6 #6050 (F1/F2 residue: alert text + CLI runners + reviewer alert, scaffold order, failed-delete discard, cost assertion). [TASK-634](tasks/archive/TASK-634-declined-run-repick-and-evidence-fail-false-fix-issue.md) (GH-6031 post-mortem: evidence FAIL trimmed away → reviewer blocked by contract → Pilot declined with a reason nobody saw → decline re-picked → cap): A1 PR#6039 · B1 PR#6043 · C1 PR#6044 all merged 11-03, unreleased. [TASK-620](tasks/archive/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md): 11-03 read 6/7 criterion-2 numbers pass (42 runs, 88.6 % agreement, RC 23.8 %, median 109 s), day count 3/5 → enforce call 11-05; L9 (review runs after the evidence) #6037 → PR#6045 merged, its test-depth fix PR#6049 + minors PR#6055 (hand-merged 11-04 11:35Z after a GitHub 500 burst failed it in autopilot → [TASK-635](tasks/archive/TASK-635-autopilot-ci-poll-api-outage-hold-not-terminal-failed.md) H1 #6056: API outage = hold + backoff + redrive, not terminal). [TASK-631](tasks/archive/TASK-631-switchable-claude-billing-mode.md) A2 #6052 (runtime_overrides store, GET/POST `/api/v1/billing-mode`, billing_mode_changed alert) → **PR#6057 merged 12:19Z**, post-merge REQUEST-CHANGES → fix #6058 (queued subscription row re-resolves through a later api_key override; own review + repro agree; closed by hand, reopened) → **PR#6065 14:21Z** · A3 #6053 → **PR#6061 13:38Z** · A4 #6064 (gateway honours `set_by`, `pilot status` sends the token) → **PR#6066 14:47Z** — named billing-mode because the console's `/api/v1/billing/...` is Paddle. H1 #6056 → PR#6059 13:07Z + fix #6060 → PR#6062. **v2.312.0 auto-cut by the release pipeline 14:47Z at `9a3c7379b`, box hot-upgraded 15:11Z.** B1 usage-limit fallback (auto-switch to api_key until the reset, revert by the A2 expiry) #6069 → **PR#6070 merged 17:45Z** (`e41ca4097`, unreleased; own review APPROVE-WITH-NOTES). Redeploy-done note to the infra contractor posted 12:27Z. ui#299 false-done (SDK title search `GH-299` matched "$299" in PR#163) → refiled ui#302, [studio-sdk#156](https://github.com/qf-studio/studio-sdk/issues/156) → sdk PR#157 merged, pin bump pending. Fleet: the infra contractor re-baked the golden AMI on **2.311.0** (11-04 10:56 CET) and set `BillingEnabled` on; **console-api redeployed 11:28Z** (prod-0.1.60, task def 83): webhook rejects unsigned POST (400) ✓, `/billing/config` live check needs a signed-in session → [TASK-497](tasks/archive/PILOT-CLOUD-TASKS-MOVED.md) next = live checkout smoke + canary tenant (TASK-495 exit). False fix issues closed by hand: #6031, #6041 (go-proxy flake). Memory `declined-run-repicked-and-evidence-fail-files-false-fix-issue`.
- **11-02→11-03: WAVE 2 (RESEARCH → TICKETS) PLANNED, DISPATCHED AND CODE-COMPLETE IN 24 H; PADDLE SSM WRITTEN; TASK-630 FILED; v2.309.0 RELEASED.** [TASK-585](tasks/archive/PILOT-CLOUD-TASKS-MOVED.md): seven legs (P2 drafts + Go shippable check · P3 reject → SQLite learning + prompt injection · C5 draft store + approve → `pilot` issue · C4 reject write-through · C10 research cap 50 · C11 chat `action` relay (missed by the plan, found by the PR#291 pre-PR review) · U10 Create tickets + draft review). Console live at prod-0.1.60 = main 11-02 20:58Z; UI deployed 11-03 10:48Z after PR#293. Cost of two legs filed without `no-decompose`: three hand-filed revisions (console#474, ui#296/#297) plus a 0039 migration collision (console#465) and a GitHub stuck merge ref on PR#293 (close/reopen fixed it; autopilot had timed out → hand merge). Six memories written (parallel migrations · epic children on shared files · fix issues never close + Blocked-by deadlock · stuck merge ref + CI-timeout terminal · Slack approval cards · reviewer loops on PR-body defects). [TASK-497](tasks/archive/PILOT-CLOUD-TASKS-MOVED.md): three `BILLING_PADDLE_*` SecureStrings in SSM 11-02 12:31Z, the infra contractor pinged 13:33 + nudged 11-03 09:37 CET, flip pending. [TASK-630](tasks/archive/TASK-630-monitor-register-epic-subissues-project.md) → pilot#6014 → PR#6015 merged ("(no project)" grid rows). Docs drift sweep e6a85128d (TASK-583/546/584/466, roadmap rows, saas banners). v2.309.0 released 11:37Z, box on it by 11:52Z; the infra contractor asked 11:54 CET (ts `1793708051.345689`) for the AMI bake → fresh tenant → exit run. [TASK-631](tasks/archive/TASK-631-switchable-claude-billing-mode.md) A1 PR#6017 failed CI on one stale test key; the size guard declined the fix issue → hand-filed pilot#6019 → **PR#6017 merged 12:14Z**; post-merge minors → [TASK-633](tasks/archive/TASK-633-billing-mode-post-merge-fast-follow.md) F1 #6021 / F2 #6022. **Duplicate run found and killed:** autopilot's hold redrive stripped `pilot-needs-human` from #6016 when #6019's commit landed, the poller re-picked it 19 s later and a fresh worktree re-implemented the merged work → killed 12:37Z (`status=infra`) → [TASK-632](tasks/archive/TASK-632-restart-repick-failed-with-open-pr.md) → pilot#6020 (admission refuses an issue whose pilot branch has an open PR). Three memories (hold-redrive re-pick · running rows have no cancel, kill the Claude group not the gate · hand-filed autopilot-fix shape). **14:16Z: `post_merge_review.file_minors` flipped ON on the box (trial)** — the PR#6017 log showed the pre-PR shadow review found 8 of the 9 post-merge minors on the same lines and fixed none: the fix round needs enforce *and* a `[blocking]` tag, and the reviewer tags unmet issue promises `[minor]`; minors had no automatic path. Next APPROVE-WITH-NOTES post-merge review files one `review-minor` issue (cap 1/project/day, auto-dispatched). Severity calibration (issue requirement not met ⇒ blocking) is the open lever before the enforce flip ([TASK-620](tasks/archive/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md)). **Billing mode (TASK-631/633, from scrappydog #5955 triage):** A1 pilot#6016 → PR#6017 merged 12:14Z in 54 min; post-merge nine minors → nav-research confirmed 8 + found 2 (retry ctx from `context.Background()` drops the credential; bootstrap has none) → F1 #6021/PR#6024, F2 #6022/PR#6025, F3 #6026/PR#6027 all merged same day, all in **v2.310.0**. Release audit found the regression my own spec introduced: unset `billing.mode` = subscription = inherited key stripped → every hosted tenant (env key, no OAuth) breaks on upgrade; none exposed (2.282.0/2.277.4) → **F4 #6032 P1** (unset = passthrough) + fleet hold. New SOP `sops/development/dispatch-task-legs-to-pilot.md` replaces the nav-pilot whole-doc flow (one `--label`: `no-decompose` first, `pilot` second). Pitfalls mem-277/280/281. **16:22Z: GH-6031 closed not-planned** — the first `review-minor` chain (#6026 → PR#6027 → #6028 → PR#6030) ended in a false fix issue: the runner's evidence paste showed `FAIL … 585s` from a loaded `-race` run (the `--- FAIL:` line trimmed away), CI was green, the reviewer blocked by contract, Pilot reproduced `ok 552s` on main and `DECLINED:` with a full reason, and the dispatcher re-picked the decline, hit the cap of 2 and posted the generic stall comment; the reason lived only in `daemon.log`. [TASK-634](tasks/archive/TASK-634-declined-run-repick-and-evidence-fail-false-fix-issue.md) A1 (decline stalls on the first run, comment quotes the reason) · B1 (evidence FAIL with green CI → minor) · C1 (trim keeps failure lines, runner logs failing test names); memory `declined-run-repicked-and-evidence-fail-files-false-fix-issue`.
- **11-02: v2.308.0 RELEASED, 18 TASK DOCS ARCHIVED (39 → 21), MAIN PROTECTED, L8 MINORS SINK SHIPPED.** TASK-628 closed: PR#6003 Navigator review APPROVE-w-notes (Opus pre-PR + post-merge had found 3 of 4 notes; the human pass added only test/mutation execution and one caller hop) → fast-follow [#6005](https://github.com/qf-studio/pilot/issues/6005) → PR#6006 merged in 23 min. TASK-620 L8: minor defects had no sink (only REQUEST-CHANGES files an issue) → [#6007](https://github.com/qf-studio/pilot/issues/6007) → PR#6008 merged 11-01 20:11Z: post-merge APPROVE-WITH-NOTES files one batched Navigator-template issue for filtered minors, `file_minors` default off, cap 1/project/day, label review-minor — **box opt-in pending**. Archive sweep (triage by navigator-research + GitHub state): 627/628 + 11 finished (502, 509, 582, 590, 591, 600, 603–606, 623) + 7 stalled (404 stale duplicate deleted; 489/490/491 shipped or obsolete; 492 step 4 → TASK-629 D; 393 plan stays in `system/throughput-roadmap.md`; 496 residue → TASK-584 § Carried). Repo settings: branch protection on main (test + lint required, no force-push, admins not enforced) → broke the release workflow's version-sync `gh pr merge --auto` → repo `allow_auto_merge` enabled; PR#6009 hand-merged 11:42Z. v2.308.0 cut 11:26Z, box hot-upgraded by 11:29Z. TASK-629: M1 PR#6004 + M2 site#28 merged 11-01 evening; 301 live by 12:11 CET, `Location` carries `:443` (deviation, unreported). Memories: `navigator-pr-review-executes-reviewer-not-verified-list-not-a-reread` · `gate-exit-2-no-rule-to-make-target-was-a-claude-fixable-failure` · `hasmakefiletarget-is-the-make-target-probe-never-make-n-or-q`. Open: file_minors opt-in · TASK-620 enforce read 11-05 / 20 runs (~13 so far) · console tag ruleset · board `RATE limit-hits=181` + stale autopilot row 577/GH-531.
- **11-01: SLACK CLICK LATENCY CLOSED, SDK + PILOT SHIPPED SAME DAY, GATE GAP FOUND ([TASK-627](tasks/archive/TASK-627-pr5990-approval-to-merge-latency-research.md) → [TASK-628](tasks/archive/TASK-628-quality-gate-absent-make-target-fallback.md)).** PR#5990's "≈2 h approval→merge" was 2m00s of daemon time after the click; the 2h16m sat before the click reached the daemon, founder cannot recall an earlier click, the Slack card shows the post time not the click → undecidable. Found on the way: the SDK Socket Mode client never handled Slack's `disconnect` (no `envelope_id` → dead branch, 4 blind ~11 s windows/day) and no click time was logged. S1 sdk#152 → PR#153 (founder approved over a pre-PR RC: the gate retry had rewritten the SDK Makefile) → post-merge RC auto-filed sdk#154 → PR#155 restored it → **studio-sdk v0.38.5 by hand** (`pilot release` from the SDK dir resolves the pilot repo — use `git tag` + `gh release create`). P1 pilot#5999 → PR#6000 merged 16:38Z → **v2.307.0 by hand** ("no Mondays"). Root cause of the Makefile damage = global `make test-changed` gate on a repo without the target → box override for studio-sdk + restart 16:01Z, product fix [TASK-628](tasks/archive/TASK-628-quality-gate-absent-make-target-fallback.md) → **#6002 dispatched** (fallback to the gate's default target / native command; "No rule to make target" = runner failure, no Claude retry). TASK-620 sample 10/20 two-pass runs, agreement 10/10.
- **10-27→10-30: REVIEWER PROGRAM — SEVEN CALIBRATION RUNS, PRE-PR REVIEW IN SHADOW, TWO PASSES MEET THE BAR.** 10-27 reviewer knob ON; scrappydog trio shipped (TASK-607/608/609). 10-28: TASK-610–617 (budget, worktree default, memories+checkout+confinement, severity contract, flake, adoption binding) and the box gates 22 → 3–5 min (TASK-616: `-short` + `-trimpath`, then `make test-changed`; its scope never applied until G3 #5952 fixed a `go list` last-line parse on 10-29). 10-29: Opus for the reviewer; runs 1–3 (recall 32–39 %); TASK-618 tolerant parse / TASK-619 caller tracing; founder "why review after CI at all?" → [TASK-620](tasks/archive/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md): **pre-PR review in shadow mode live 15:08Z** (L1), L2 section+ledger+label, L3 fix round, L5 autopilot RC hold, L7 reason ordering — all on the box by 20:36Z (v2.299.0 train, then v2.300.0/v2.301.0 by hand). First side-by-side: pre-PR and post-merge agree on 3 of 3 PRs; one blocking miss (PR#5966, expired git ctx) caught post-merge only. [TASK-621](tasks/archive/TASK-621-evidence-gate-live-smoke-hold-on-mutation-bullets.md) false live-smoke hold on mutation bullets fixed. 10-30: run 4 44.2 % → [TASK-622](tasks/archive/TASK-622-reviewer-adjacent-code-tracing-and-met-failure-case.md) adjacent-code rule → run 5 43.3 % (flat: prompt rules move *which* defects, not how many) → [TASK-623](tasks/archive/TASK-623-reviewer-two-pass-hunk-and-adjacent-union.md) **two passes** → run 6 **51.0 %** → P2 mechanics fix → run 7 **51.0 % stable, 0 pass failures**; **box on `passes: 2` for both reviewers since 16:21Z (v2.304.0)**; calibration track closed (bench report §"Decision inputs after seven runs"). Founder: keep shadow until 20 two-pass runs or 5 working days (≈ 11-03). Box lint pinned to CI's golangci-lint v2.8.0. Lessons in the 10-29 marker: dispatch probes (receiver form, anchored `absent`), no shell operators in evidence commands, stuck check-run recurrence (job rerun), docs pushes need a foreground `git push` for the fast path. **10-31: the four daemon defects traced, dispatched and ALL MERGED same day as [TASK-624](tasks/archive/TASK-624-daemon-defects-stuck-checkrun-restart-repick-hold-binding-body-cut.md) #5983–#5986 → PR#5987–#5990; review notes → [TASK-625](tasks/archive/TASK-625-task624-review-notes-startup-label-strands-crash-orphans.md) #5991 (one D2 regression: startup label guard strands crash orphans) → PR#5993; v2.305.0 by hand 21:08Z, box 21:13Z; then reviewer residue [TASK-626](tasks/archive/TASK-626-reviewer-residue-union-tolerance-empty-claim-marker-paren-exact-lookup-nv-count.md) → #5996** (stuck check-run = `mapCheckStatus` ignores `conclusion` under `in_progress`; restart re-pick = boot reconcile has no open-PR heal + `StartupRecover` strips the label blind; hold binding = `is:pr #N` search, mention-only; body cut = claim text left in `Location`, 200-byte literal cut). Open founder items: `executor.timeout.complex` 60→90m · branch/stash purge · TASK-596 P1 · the infra contractor PR#577 + infra#23 · PR#5950's two reviewer notes · release notices since v2.298.0 · nine verified NEW-VALIDs in the bench report.
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
- **Positioning + roadmap of record (2026-10-21/22)**: [`system/project/project_pilot_cloud_positioning.md`](tasks/archive/PILOT-CLOUD-TASKS-MOVED.md) (decisions §1–11) · [`system/project/project_pilot_cloud_roadmap_tickets.md`](tasks/archive/PILOT-CLOUD-TASKS-MOVED.md) (71 tickets per repo, filed status) · [`tasks/TASK-584-pilot-cloud-execution-roadmap.md`](tasks/archive/PILOT-CLOUD-TASKS-MOVED.md) (phases A–H, triggers, owners) · wave docs [TASK-582](tasks/archive/PILOT-CLOUD-TASKS-MOVED.md) (site) · [TASK-583](tasks/archive/PILOT-CLOUD-TASKS-MOVED.md) (design + proof)
- **Program doc**: [`tasks/TASK-405-pilot-saas-platform.md`](tasks/archive/PILOT-CLOUD-TASKS-MOVED.md)
- **Design**: [`system/saas-architecture.md`](system/saas-architecture.md) · [`saas-kanban-sync-design.md`](system/saas-kanban-sync-design.md) · [`saas-fleet-design.md`](system/saas-fleet-design.md) · [`saas-asset-research.md`](system/saas-asset-research.md)
- **New repos** (created 2026-07-14, in `~/.pilot/config.yaml`): `qf-studio/pilot-console` (Go control plane) · `pilot-console-ui` (Vue3/Vite/Bun SPA) · ~~`pilot-cloud-infra` (Go CDK)~~ **archived 2026-09-23** (estate deleted; fleet infra = the infra contractor's `aws-infrastructure-pilot`) — each has its own `CLAUDE.md`
- **Latest handoff marker**: `.agent/.context-markers/2026-11-08_eve-v2.315.2-live-task649-gitgraph-dispatched-hook-fix-6218-6172-trimmed.md` (local, untracked; previous: `2026-11-07_pm-v2.315.1-task643-a1c1d1-shipped-l4-merged-b1-rearmed.md`, `2026-11-06_am-task508-done-l11-shipped-enforce-day1-jira-off.md`, `2026-11-05_v2.313.1-billing-fallback-enabled-prepr-enforce-live-6098-flake.md`)
- **Systemic**: TASK-407 atomic dispatch-admission claim — **proven + archived 2026-07-30** ([`tasks/archive/`](tasks/archive/TASK-407-dispatch-admission-claim.md); #4265 closed, `duplicate-pr` green since 07-24). TASK-406 shipped → archived.
- **Dispatch SOP**: [`sops/development/dispatch-task-legs-to-pilot.md`](sops/development/dispatch-task-legs-to-pilot.md) — ADOPTED 2026-11-03: legs are `### LEG` blocks filed by `scripts/nav-dispatch-legs.py` (path + probe pre-flight, `no-decompose` then `pilot`); the nav-pilot whole-doc flow is not used here
- **Ops SOP**: [`sops/operations/safe-daemon-restart.md`](sops/operations/safe-daemon-restart.md) — restart is the operator's action; never relaunch the `--dashboard` daemon from an assistant shell (no single-instance lock yet)
- **Quality SOP**: [`sops/quality/real-stack-verify-gates-ui-merges.md`](tasks/archive/PILOT-CLOUD-TASKS-MOVED.md) — ADOPTED 2026-07-30: UI-surface merges aren't DONE until operator-verified on the live local stack (daemon gates are fixture-only; 5 drift defects in one night prove it)
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
_Pruned 11-09 after v2.316.0: TASK-649 (git graph per project) and TASK-643 (CI-fix chain) shipped and archived; TASK-620 archived, the reviewer row keeps TASK-612. Pruned 11-08: TASK-645, TASK-639, TASK-637, TASK-641 archived. Older prune notes: `git log -p .agent/DEVELOPMENT-README.md`._

| Priority | Topic | Why |
|----------|-------|-----|
| **P0** | **Agent isolation — agent sessions under a separate Unix user, read-only GitHub token per run, secrets from SSM** ([TASK-651](tasks/TASK-651-agent-uid-isolation.md)) | 📝 **11-12: design accepted** ([design](system/agent-isolation-design.md)). Box facts: the daemon user has passwordless root, seven literal secrets in the config, a personal GitHub token with write rights in every agent environment. Plan: operator steps O1–O4 (remove root sudo, GitHub App, secrets to SSM, agent user) · code legs U0–U5 behind `executor.claude_code.run_as_user`, off by default · switch on a sandbox project first. **11-12: O1 done on the box (no root for the daemon user). U0 #6320 → PR#6325 merged; container review found three major defects for a real host → #6328 (O4 waits for it). 11-13 pm: F1 #6344 → PR#6349 merged after a run-it review with real sudo; the option must stay empty: kill under a sudo terminal and API backends → F2 #6354 dispatched. B1 #6345 second run started (defect list in the issue). U2–U5 dispatch-ready; order F2 → U2 → U5.** Review of PR#6323 showed that the secret gate trusts refs the session can write; U3 closes that class when the option is set. Waiting for the founder: create the GitHub App (O2), new secret values in SSM (O3). |
| **P0** | **Secret exposure hardening — value-based redaction, leak detection, adapter canary tests, read guard, push gate, GitHub alert watcher, agent isolation** ([TASK-650](tasks/TASK-650-secret-exposure-hardening.md)) | ✅ **L1–L6 released in v2.318.0 (11-10); follow-ups #6272, #6276, #6279, #6281 released in v2.319.0 (11-11).** Read guard precision is **not planned** (founder decision 11-11, mem-305): PR#6285 reverted, PR#6299 closed after a NO-GO black-box review; the substring matcher stays. Queue: #6284 (push gate follow-ups, fresh run from main) · #6310 (lint that fails the local gate). Incidents: Telegram token in `daemon.log` and on public GitHub since 04-07 (alert #2, resolved as revoked; token rotated 11-09); agent `cat` of the box config in GH-4669 kept in a recording (masked). **11-13: L8 #6343 → PR#6348 merged after a run-it review; three notes → L9 #6353 dispatched (hooks path constant, co-author strip, NUL byte). The root fix is TASK-651 U3.** **Open: founder rotation of the Anthropic key + Slack bot/app tokens · paid secret scanning for five private repos · H1 → TASK-651.** SOP `sops/operations/secret-leak-response.md`, memories mem-300 to mem-305. |
| **—** | **Pilot Cloud docs (console, UI, site, fleet, tenants, Paddle, SaaS strategy)** | 📦 **Moved 2026-11-08 to the private repo `qf-studio/pilot-console` → `.agent/`** (TASK-646). Console sessions start from that repo. The `system/saas-*.md` files here are pointer stubs; `tasks/archive/PILOT-CLOUD-TASKS-MOVED.md` lists the moved task IDs. |
| **P2** | **Dashboard banner — model line and a plain billing word, both inside the banner** ([TASK-652](tasks/TASK-652-dashboard-banner-model-line-billing-word.md)) | ✅ **11-13: D1 #6350 → PR#6352 merged (first Opus 5.5 run); waits for a release.** Two-line banner: model text on line 2 left (two models no longer fit on line 1 at 69 columns), "subscription" or "api tokens" on line 2 right in place of "billing MODE"; the `b` key hides both. |
| **P2** | **One price table — the same rates for the cost estimate, the dashboard card and the replay recorder** ([TASK-653](tasks/TASK-653-one-price-table.md)) | 🚀 **11-13: P1 #6355 dispatched.** Leaf package `internal/pricing`; 5.x models at all three call sites; Sonnet 5.5 cache read $0.10; 1-hour cache write. Needed for the Opus 5.5 test on `complex` tickets (box config since 11-13). |
| **P1** | **Move Pilot Cloud docs out of this public repo into `pilot-console/.agent`** ([TASK-646](tasks/TASK-646-move-pilot-cloud-docs-to-private-repo.md)) | 📋 **11-08 planned, founder round open (D1–D5).** Scan: 16 instance IDs, the AWS account ID + IAM user name (no MFA — Phase 0 operator fix first), ~20 SSM names, the fleet design and ~40 console task docs in the public tree; 0 secret values. Move SaaS system docs, console tasks + archive, console memories, 6 tracked October markers, SOPs; stubs + one MOVED pointer stay; denylist gains the account ID. Nav session executes (cross-repo `git mv`), not Pilot. |
| **P1** | **Research page `subagent` + `context` chat events — the daemon reports sub-agent rows with durations and the turn's Navigator read counts (pilot#5810; unblocks the console Research page ui#244)** ([TASK-647](tasks/TASK-647-research-page-subagent-context-chat-events.md)) | ✅ **11-09: released in v2.316.0 (box 09:47Z).** Open: live check on the console Research page of a tenant on v2.316.0 (tree rows + run line), then archive. |
| **P1** | **Legacy repo bootstrap — real Navigator context on first attach, persisted, priced** ([TASK-549](tasks/TASK-549-legacy-repo-navigator-bootstrap.md)) | 🚀 10-14: #5679 (bootstrap session, trigger default manual) + #5680 (copy-back) dispatched. Verified: scaffold's injected context sections are placeholders nothing fills; no copy-back from worktrees; chat turns are cold research sessions. Next: cold-start bench (Navigator job), then D1–D3 (auto trigger, console snapshot, 10-ticket price). |
| **P1** | **Reviewer program — post-merge audit calibration** ([TASK-612](tasks/TASK-612-post-merge-reviewer-calibration.md) · [TASK-620](tasks/archive/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md) archived) | ✅ **11-09: TASK-620 complete — L6 sampling released in v2.316.0, `sample_rate: 5` live on the box.** Follow-up [#6238](https://github.com/qf-studio/pilot/issues/6238). Open: TASK-612 scored run. |
| **P1** | **Bootstrap unit economics — deep-research cost per repo size, cap, degrade path** ([TASK-581](tasks/TASK-581-bootstrap-unit-economics.md)) | 🚀 10-21: harness dispatched → [#5788](https://github.com/qf-studio/pilot/issues/5788) (runner over 4 public repos S/M/L/XL × 3 stages, prompt files, report tool, fixture tests; no live spend in the executor). Live 30-session matrix = operator step on the box (≤ $250 guard). Provisional cap $25 soft / $40 hard per repo; degrade = scope by churn → sample → min 5 tickets. D1–D5 open for the founder. Found: `modelPricing` prices Opus 5.5/Sonnet 5.5 ~25–50% high (unfiled). |
| P2 | **Console Docs page — live verification** ([TASK-466](tasks/archive/PILOT-CLOUD-TASKS-MOVED.md)) | Page renders through the edge (09-22 founder login) but the console DB has no tenant yet → the real `.agent`-tree proof is TASK-499 L3's acceptance (first fleet tenant). infra#51/TASK-498 closed superseded. |
| P2 | Delivery-evidence audit — false-success class ([TASK-460](tasks/TASK-460-delivery-evidence-false-success.md)) | **11-08 founder call: diff-coverage stays report-only.** 🔎 **11-06 research read**: guard-vs-shape table in the doc; top fix already in sdk v0.38.6, v0.38.7 pin filed [#6158](https://github.com/qf-studio/pilot/issues/6158); next-leg candidate = diff-coverage hold under enforce (founder call). Split from TASK-459 by founder scope call 08-08: green CI is not proof the requested change shipped (`mem-151`: scaffold-only PR merged green, parent auto-closed, zero requirements delivered). Planned, NOT dispatched; TASK-459 Phase 4's inventory hook feeds it the success-side site rows. Candidate legs: diff-surface check · ACs fail-when-unwired · epic-collapse guard. |
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
