# Approval Architecture Roadmap — per-project routing/gating + review-gate path

**Created**: 2026-08-06 (investigation: 6 research agents + 1 design agent; plan approved by founder).
**Owner intent**: personal projects route approval asks to Telegram, work projects to Slack; per-project `require_approval`; eventually console/dashboard approvals (S4 exit already requires a dashboard-only approval week). Review-gate policy call stays open until the per-project flip (B5) is evaluable.

## Context (measured 2026-08-06)

- 35/35 recent PRs (pilot + console) merged with **zero reviews**; median open→merge 8.7 min (pilot). Post-merge spot-checks are the only review; the 16:00 train deploys unreviewed code same-day.
- `require_approval=false` is the founder decision of 2026-07-20 (`approvals-off-stage-auto-merge.md`). Approval asks still fire on Slack via the escalate-only merge gates (size-floor >500 net production lines, scope-drift — hardcoded, intentional defence-in-depth, `controller.go:2103-2161`).
- Docs-only pushes paid the full 224-287s pre-push gate and lost a ~16-20% race against the daemon's merge cadence (~12-29 main-advances/day, median gap ~21 min).
- Full findings + file:line grounding: session research 2026-08-06; leg specs in `.agent/tasks/TASK-453…456` (archive after merge).

## Legs

| Leg | Issue | Scope | State |
|---|---|---|---|
| **A** — gate fast path | [pilot#4771](https://github.com/qf-studio/pilot/issues/4771) | pre-push hook reads stdin refs, union diff; docs-only → check-secrets + check-graph only (~6s); SOP `.agent/sops/quality/pre-push-gate.md` | dispatched |
| **B1** — channel-routing fidelity | [pilot#4772](https://github.com/qf-studio/pilot/issues/4772) | rehydrate + expiry-sweep scoped by `preferred_channel`; Slack first-send destination parity; `github-review`→`github` alias. **Hard prerequisite of B3** (dual-channel unsafe without it) | dispatched |
| **B2** — project identity on approvals | [pilot#4773](https://github.com/qf-studio/pilot/issues/4773) | `Request.Project` + `approval_pending.project` + GET surface emits it; fixes scoped-mode row-dropping; unblocks console#109 attribution (coordination note posted on console#109) | dispatched |
| **B3** — ProjectApprovalOverride | [pilot#4774](https://github.com/qf-studio/pilot/issues/4774) | per-project `require_approval` + `approval_source` via the `ProjectCIChecksOverride` copy-pattern; wired at all 3 construction sites incl. gateway mode; health checks | dispatched |
| **B4** — Manager persistence + `console` channel | not yet filed | `InsertPendingApproval` moves from handlers into `Manager` (pre-dispatch — also closes the send-before-persist crash window); explicit `approval_source: console` = persist-and-wait, decisions via HTTP only; Manager-level orphan sweep. **Do NOT change `handler == nil` semantics** (it parks-then-rejects on timeout; harness relies on the early-return path). | gated on [pilot#4757](https://github.com/qf-studio/pilot/issues/4757) merging (atomic already-decided guard); off B5's critical path |
| **B5** — the flip (config only) | operator step | per-project `approval:` blocks — personal → telegram, work → slack; per-project `require_approval` as desired | after B3 deploys + one restart cycle |

Ordering: `A` independent · `B1 → B3 → B5` · `B2` early (console#109 consumes `project` additively) · `B4` after #4757, independent of the flip.

## B5 flip procedure (when the time comes)

1. Prereqs: B1 + B3 merged AND deployed (restart — config is static at boot; `Reload()` has zero callers).
2. Add per-project `approval:` blocks to the box config (operator consent + restart per `pilot-aws` skill rules).
3. **Held-PRs pitfall** (`require-approval-flip-doesnt-release-held-prs.md`): PRs already parked in `StageAwaitApproval` never re-read the flag — sweep/decide them at flip time, don't misread them as a bug.
4. **Two-knob deadlock** (`health.go:923-942`): `require_approval=true` needs `approval.enabled` + `approval.pre_merge.enabled` or every PR parks forever. The B3 health checks cover the per-project variants — run `pilot doctor` after the config change.
5. Watch the first escalation-gate ask on each project land in the right channel.

## Review-gate policy (still open, deliberately)

The selective option is nearly free once this track lands: a sensitive-path predicate is a 4th `escalateReason` in the existing chain, and per-project `require_approval` (B3) gives per-project strictness. Full-flip evaluation belongs after console#109's Needs-You lane is live (dashboard approvals with readable context — the 07-13 lesson: approval without review context is theater). Until then, post-merge review remains the operating mode **by open call, not decision**.

## Post-merge review follow-ups (filed 2026-08-06 evening)

- [pilot#4777](https://github.com/qf-studio/pilot/issues/4777) — PR#4767's atomic guard is dead code in production wiring: Controller state writer swallows typed errors, PRState application unguarded. **Blocks trusting HTTP double-decide semantics** (console decision proxy relies on the 409).
- [pilot#4778](https://github.com/qf-studio/pilot/issues/4778) — in-tree static-token clients (merge-waiter/cleaner) + webhook-mode startup token validation.
- [sdk#107](https://github.com/qf-studio/studio-sdk/issues/107) — SDK TokenFunc API. **App-cutover status corrected**: prereqs are NOT done — polling-mode SDK pollers (`poller_github.go:228,478`) + PR-review handler (`main.go:2328`) hold static boot tokens; under App auth they 401 ~1h after start. Cutover blockers now: sdk#107 → pilot wiring leg (author after sdk release) → operator App provisioning → `GH_TOKEN` box-env check (pilot#4753's precedence finding, merged).
- Recurring lesson (2× this week — PR#4752 auth test, PR#4767 composed test): **tests asserting configurations production never wires**. Candidate memory/pattern entry + review-checklist item.

## Backlog (filed nowhere yet — pull from here)

- ~~Infra-CI failure classification~~ → **DISPATCHED 2026-08-06**: [pilot#4779](https://github.com/qf-studio/pilot/issues/4779) (TASK-457, structural classification + never-close-on-zero-evidence invariant) and [pilot#4780](https://github.com/qf-studio/pilot/issues/4780) (TASK-458, platform-outage breaker: cross-PR correlation → suppress destructive actions + pause admission → self-resume). Root cause found: TASK-418's infra classifier already existed but matches **four hardcoded log substrings** (`failure_class.go:71-74`) — the outage's prose matched none, so it fell through to the fail-safe `FailureClassCode` and ran the destructive path. Also found: `cancelled`/`timed_out` conclusions trigger `CIFailure` but are excluded from every evidence-gathering filter → zero evidence → defaults to code failure (outage amplifier).

- Sensitive-path escalation predicate (auth/credentials/approval/gateway-mutating paths → escalate), per-project pattern list on the B3 override struct.
- Per-project *destinations* (distinct chat_id / Slack channel per project) — needs a real `Destination` field on `Request` + persistence; the `Approvers[0]` hack is a tarpit.
- Webhook-mode Telegram approval-handler registration (`internal/pilot/pilot.go` registers Slack only; B3's health check makes the gap loud).
- Gateway-mode construction path skips the CIChecks overlay (`cmd/pilot/main.go:835` asymmetry; B3 fixes approval only).
- Branch protection on `qf-studio/pilot` main: still NONE (founder-trio item). Its stated de-urgency precondition (gh-guard shim, PR#4704) has SHIPPED — item needs re-triage: enable as defense-in-depth or explicitly defer with updated reasoning.
