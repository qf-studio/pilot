# TASK-496: Resume pilot-console execution — S5 legs after the Nelya hand-over

**Created**: 2026-09-10 · **Status**: 🚀 B7 DECIDED + DISPATCHED 2026-09-10 (option A) — console **#282** filed (`pilot bug no-decompose`), PR#222 closed unmerged, #215 closed as superseded. Usage rollup dispatched 2026-09-10 → console **#283** → PR#285 REQUEST-CHANGES (migration 0018 collision with #284 + cumulative-counter summing) → revision **#286** in flight. B7: **PR#284 MERGED 10:12Z + reviewed APPROVE-w-notes** → follow-ups #287, #288, pilot#5426. Gate `idle_sleep_enabled` is OFF by default; keep it off until every tenant carries the dashboard-scope flag (re-bootstrap/AMI roll).

## Where console execution stopped (GitHub is authoritative)

- Last shipped: TASK-495 ECS/consolectl legs console#259–#264, #270–#274 (09-06/07); console#280 (refile of the unpickable #275) 09-07; **pilot-console `prod-0.1.0` released 09-08 10:06Z** (first ECS image, no tag ruleset yet).
- **Only open console item: #215 / draft PR#222 (B7 sleep redo).** `mergeable=CONFLICTING`, 11 files, conflicts in `cmd/consolectl/run.go`, `internal/fleet/reconciler.go`, `internal/fleet/store.go`, `main.go` (exactly the files the ECS legs rewrote after the branch was cut). Labels `pilot-needs-human` + `needs-manual-rebase`. Box autopilot shows it as `222 failed pending`. The 08-26 review verdict was REQUEST-CHANGES: D1–D4 fixed, but (a) the exec reader polls project-scoped `/api/v1/queue` while the tenant unit omits `--dashboard-scope` → blind to every repo but #0; (b) no kill switch for the two readers. Draft was the merge block; then a restart re-armed #215 and Pilot began re-executing over the reviewed branch; human unlabeled to stop it; five 405 "still a draft" merge attempts → needs-human.
- pilot-console-ui: no open issues/PRs. TASK-478 build+verify complete.

## B7 sleep path — DECIDED 2026-09-10: option A (founder: "go recommended, close")

Filed console **#282** from [`drafts/console-b7-idle-sleep-refile.md`](drafts/console-b7-idle-sleep-refile.md): #215's design + the 08-26 blockers (fleet-wide execution scope via the dashboard-scope flag rendered into the tenant unit; `idle_sleep_enabled` gate default off + `idle_window` field) + N2 (terminal-status allowlist, unknown = active) · N3 (one 10 s per-instance deadline over token+describe+GET, 5-min sampling throttle) · N4 (in-flight branch deletes idleSince) + housekeeping (second board pool, label index, test margins). Review PR#\<n\> when it lands against every acceptance box; the dashboard-scope flag is CLI-only on the pilot side, so already-bootstrapped tenants need a re-bootstrap or AMI roll.

Options as evaluated:

| Option | Cost | Notes |
|---|---|---|
| **A. Close PR#222 + #215, file a fresh issue on a new branch (recommended)** | one issue body | Branch is 11 files stale against main; the two review items are unimplemented anyway. New issue = #215's design + the `--dashboard-scope` fix + reader kill switch. No autopilot-meta footer (nothing worth continuing). Clears the box's `222 failed pending` residue. |
| B. Rebase PR#222 by hand in a worktree, then file a revision issue with footer + `autopilot-fix` | ~1 h human + one issue | Preserves review history on the PR; still needs (a)+(b) implemented. |
| C. Defer B7 entirely | none | B7 is cost control, not the S5 hard gate. Reasonable if SaaS tenants stay at 1–3 for weeks. |

## Ready dispatch (no founder / Nelya dependency)

1. **pilot-console · usage rollup (fleet design item 16) + GET /api/v1/usage → FILED as console #283 (2026-09-10)** — body and gate-checked at [`drafts/console-usage-rollup-issue.md`](drafts/console-usage-rollup-issue.md). Research facts behind it: instance-proxy recipe `internal/proxy/proxy.go` (console); daemon `/api/v1/metrics` JSON has `totalCostUSD` (pilot `internal/gateway/dashboard.go:75-102`), so no blocker; latest console migration 0017, RLS down-test pins absolute versions so 0018 is safe; **no advisory lock / leader election exists in the console** — the reconciler is single-replica only because it runs in `consolectl`; the draft makes the poller follow that and makes an advisory lock in-scope if a shared loop is chosen. UI leg (pilot-console-ui usage page) is a 5-line follow-on blocked on the API issue.

## NOT ready (corrections to the 09-10 research brief)

- **infra#36 (isolation harness cleanup) is not Pilot-executable.** Declined twice by the executor's model (`stop_reason: refusal`, `category: cyber`) even with zero probe content (08-26). The package is off-limits to the executor; needs a human author or a different repo layout. infra#35 same, unlabeled. Memory: `model-refusal-looks-like-exit-status-1`.
- Billing lifecycle (suspend on past_due) — code is ready but wired to the Stripe portal → founder payment-processor decision.
- Egress allowlist proxy, pricing from COGS, S6 cutover — founder decisions.
- EBS restore drill — operator work, runbook exists in pilot-cloud-infra.

## Founder decisions outstanding (gate console work)

- B7 path (above) — 2026-09-10
- Domain: `pilot.build` $720/yr vs `pilot.engineering` $83 — since 08-26
- infra#35 authorized framing / human author for the isolation harness — since 08-26
- Egress-allowlist scope — since 08-26
- Payment processor / Stripe inputs (gates billing lifecycle + CON-5 copy) — since 07-13
- Branch protection on `qf-studio/pilot` main — since 08-03, still none
- pilot-console tag ruleset (none; `prod-0.1.0` is out) — since 09-08

## Nelya items outstanding (TASK-495)

- Silent since 09-07. Open: GHCR pull method (PAT vs Actions), deploy `prod-0.1.0`, SES identity, NLB ALPN, `TRUSTED_PROXY_CIDRS` confirm, CloudFormation templates share, compression/CSP + #447.
- `auth-service-findings-2026-09-06.md` (Slack file) not triaged into issues.

## Refs

- Roadmap: [`system/saas-roadmap.md`](../system/saas-roadmap.md) S5 row · fleet design item 16: [`system/saas-fleet-design.md`](../system/saas-fleet-design.md)
- TASK-405 (program), TASK-495 (Nelya hand-over), TASK-478 (rail, complete)
- console#215 · console PR#222 · infra#35 · infra#36
