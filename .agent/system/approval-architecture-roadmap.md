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
- ~~[sdk#107](https://github.com/qf-studio/studio-sdk/issues/107) — SDK TokenFunc API~~ → **merged (PR#108, in v0.32.0)**. App-cutover track now owned by `.agent/tasks/TASK-461-app-cutover-sdk-token-wiring.md` (2026-08-09): research found a second SDK gap — `Adapter.NewPoller` constructs its client internally from `cfg.Token`, no injection seam → [sdk#109](https://github.com/qf-studio/studio-sdk/issues/109) dispatched (adapter `WithAdapterClient`). Chain: sdk#109 → pilot wiring leg (drafted in TASK-461, HELD) → operator App provisioning → `GH_TOKEN` box-env check (pilot#4753 precedence finding). Site inventory re-verified: the drifted `main.go:2328` ref is actually `main.go:826/2085` (`apGHClient`, held by every autopilot controller) + `main.go:2541` (PR-review webhook client) + `poller_github.go:478` + the adapter-internal poller client.
- Recurring lesson (2× this week — PR#4752 auth test, PR#4767 composed test): **tests asserting configurations production never wires**. Candidate memory/pattern entry + review-checklist item.

## Outage recovery + hardening (2026-08-07)

GitHub Actions incident resolved 02:04Z 08-07 (~9.5h). Recovery executed: daemon restarted
10:43Z on v2.255.0, queue resumed, PR#4776 (gate fast path) merged 10:49Z, `pilot-needs-human`
cleared from GH-4771, `make install-hooks` run on the laptop (fast path now live locally).

Outage artifacts cleaned: fix-issues **#4766/#4769/#4775** closed (all auto-generated from
`Set up job` infra failures — GH-4756 burned three PR generations: #4765→#4768→#4770, each
closed on a false signal, each spawning a junk fix issue). GH-4756 re-dispatched fresh.

**New defect found during recovery — [pilot#4781](https://github.com/qf-studio/pilot/issues/4781)**:
CI aggregation counts **superseded check-runs**. Restoring PR#4770's branch produced a fresh
passing run, but autopilot aggregated it together with the outage-era failed run for the same
SHA (the "CI checks discovered" log line listed every check name twice), read failure, and
re-closed the PR + deleted the branch within 90 seconds of the reopen. **Any re-run-based
recovery is unreliable until this is fixed** — it is independent of the outage class and was
the reason the resurrection attempt failed. Note PR#4776's rerun DID work, because
`gh run rerun --failed` updates check-runs in the same workflow run rather than creating a
second one.

## Backlog (filed nowhere yet — pull from here)

- ~~Infra-CI failure classification~~ → **DISPATCHED 2026-08-06**: [pilot#4779](https://github.com/qf-studio/pilot/issues/4779) (TASK-457, structural classification + never-close-on-zero-evidence invariant) and [pilot#4780](https://github.com/qf-studio/pilot/issues/4780) (TASK-458, platform-outage breaker: cross-PR correlation → suppress destructive actions + pause admission → self-resume). Root cause found: TASK-418's infra classifier already existed but matches **four hardcoded log substrings** (`failure_class.go:71-74`) — the outage's prose matched none, so it fell through to the fail-safe `FailureClassCode` and ran the destructive path. Also found: `cancelled`/`timed_out` conclusions trigger `CIFailure` but are excluded from every evidence-gathering filter → zero evidence → defaults to code failure (outage amplifier).

- ~~Fix-issue evidence quality~~ → **FILED 2026-08-10**: [pilot#4825](https://github.com/qf-studio/pilot/issues/4825) (unlabeled — dispatch call pending). The auto-generated `CreateFailureIssue` body for #4820 truncated the CI logs *before* the actual errcheck line, which WAS present in the run log; preflight `reject_vague` was the backstop doing the primary's job.
- ~~Dual-armed recovery race~~ → **FILED 2026-08-10**: [pilot#4826](https://github.com/qf-studio/pilot/issues/4826) (unlabeled — dispatch call pending). After PR#4818's CI-failure close, both the fix-issue chain (#4820) and the source-issue retry chain armed simultaneously; GH-3806 `TerminalLabel` never designated an owner. Ask: exactly one recovery path owns the retry, structurally.
- Sensitive-path escalation predicate (auth/credentials/approval/gateway-mutating paths → escalate), per-project pattern list on the B3 override struct.
- Per-project *destinations* (distinct chat_id / Slack channel per project) — needs a real `Destination` field on `Request` + persistence; the `Approvers[0]` hack is a tarpit.
- Webhook-mode Telegram approval-handler registration (`internal/pilot/pilot.go` registers Slack only; B3's health check makes the gap loud).
- Gateway-mode construction path skips the CIChecks overlay (`cmd/pilot/main.go:835` asymmetry; B3 fixes approval only).
- Branch protection on `qf-studio/pilot` main: still NONE (founder-trio item). Its stated de-urgency precondition (gh-guard shim, PR#4704) has SHIPPED — item needs re-triage: enable as defense-in-depth or explicitly defer with updated reasoning.
