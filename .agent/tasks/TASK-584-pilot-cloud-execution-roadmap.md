# TASK-584: Pilot Cloud — complete execution roadmap (phases A–H)

**Status**: 🚀 APPROVED 2026-10-22 (plan mode). Phase A partly done in the 10-22 session (memories mem-261..264, marker); Phase A items 1–2 (docs-copy issue on docs/content, modelPricing bug in internal/executor/runner.go) are the **first actions of the next session**; item 5 (O1 note) done. Waves 0–1 dispatched via TASK-582/583; waves 2–5 get TASK-585..588 when their triggers fire.
**Created**: 2026-10-22
**Assignee**: Navigator (task docs, dispatch, reviews) · Pilot (all P/U/C/S legs) · founder (F1–F4, O3, D1–D7) · Nelya (I1–I4)

---

## Context

The session produced the decisions (`.agent/system/project/project_pilot_cloud_positioning.md` §1–11),
a 71-ticket roadmap (`.agent/system/project/project_pilot_cloud_roadmap_tickets.md`), and dispatched
wave 0 (site, 5 issues) and the first batch of wave 1 (13 issues) via TASK-582/583. This plan covers
**everything that is still open**: the four quick close-out items, the eight held wave-1 legs, waves
2–5 (33 tickets) as task docs and issues, the founder/Nelya/Figma inputs, and the stale-doc cleanup.
Outcome: every roadmap row is either merged, filed with a `Blocked by` ref, or assigned to a named
human with a trigger.

Mechanics reused throughout (no new tooling):
- Task docs with `### LEG <ID> — repo: <owner/repo> — title: <t>` sections → filed by
  `scripts/nav-dispatch-legs.py` (path pre-flight against `origin/main`, `#{{ID}}` refs, writes `## Refs`).
- Issue bodies: Context / Scope / Acceptance (every command item ends "paste the output into the PR
  body") / Out of scope; backticks only for paths that exist on `main`; cross-repo deps are **held**, not
  expressed as refs (the dependency gate is same-repo numeric only).
- Reviews: pre-merge review as a comment, revision issue (never a draft flip), post-merge review verdict.
- Memory: `graph_manager.py --action add-memory` (+ fix the file title), never the legacy auto-memory dir.

## Phase A — close-out of the session (today, ~1 h, this session)

1. ✅ 10-22 17:50Z → pilot#5820 (S9). **File the docs-site copy issue** on `qf-studio/pilot` (`docs/content/`): why-pilot "Not a
   researcher" → "Ask about your code. Turn the answer into a ticket."; `index.mdx:73` secrets claim →
   OSS-only wording + "a box only you use" for Cloud; "Pilot Console" → "Pilot Cloud"; add the
   §3 proof line. Add row **S9** to the roadmap file (repo: pilot/docs).
2. ✅ 10-22 17:50Z → pilot#5821 (P15; also Sonnet 5 and Haiku 5.5 wrong, cache-read per model). **File the `modelPricing` bug** on `qf-studio/pilot` (found by the TASK-581 session: Opus 5.5 /
   Sonnet 5.5 priced ~25–50% high in the executor's cost estimate; cite the file the session named in
   TASK-581 § Found). Label `pilot`. Add roadmap row **P15**.
3. **Graph memories** (4): decision `$300 ceiling, ledger evidence` · decision `no dollars on
   platform-key surfaces` · decision `research writes a spec for the executor; feedback loop is the moat`
   · pattern `LEG-section task docs + nav-dispatch-legs.py`. Update mem-251's summary to point at §4–§11.
4. **Context marker** `2026-10-22_pilot-cloud-roadmap-dispatched.md` via nav-marker; set `.active`
   (replaces the stale 10-21 marker). Commit + push (`docs:`), gate in background.
5. **O1 note**: add "free listing first; paid listing needs 100 installs" to roadmap row O1.

## Phase B — wave 1 completion (as merges land; days)

Trigger = upstream PR merged (check `gh pr list --state merged` per repo, or the daily brief).

| Held leg | File when | Repo | Body source |
|---|---|---|---|
| C1 run-story endpoint | pilot#5790 merged | pilot-console | §8.1; aggregate `run_story` from the instance read |
| C2 share token | C1 merged | pilot-console | §8.1 D3 |
| U6 Run Story card + ticket page | C1 merged | pilot-console-ui | §8.1 (4 states, Retry) |
| U7 share-link view | C2 merged | pilot-console-ui | §8.1 D3 |
| U8 Review needed page | console#422 merged | pilot-console-ui | §8.2 (3 types, 3 actions) |
| C6 brief store + Briefs API + SES email | pilot#5791 merged **and** I3 (SES) done | pilot-console | §8.7 |
| U11 Briefs page + bell | C6 merged | pilot-console-ui | §8.7 |
| U12 brief settings | C6 merged | pilot-console-ui | §8.7 D3 |

Append each as a `### LEG` to TASK-583 and dispatch with `--only`. Review every wave-1 PR pre-merge
(autopilot merges on green in minutes). Wave-1 exit: new dashboard + Research page live on the
canary tenant, Run Story on a Done card, Review needed list, pen-test harness report attached to
pilot#5793, P11 run on the canary (I2).

## Phase C — wave 2: research → tickets (TASK-585; file after TASK-518 leg 4 is merged)

Legs (6): **P2** `Create tickets` drafts in SOP format + shippable check + epic split (pilot, L) →
**P3** reject-reason → learning (pilot, S) → **C5** draft store + approve → `pilot`-labeled issue
(console, after P2) → **C4** reject write-through (console, after P3) → **C10** verify the 50/day
research cap against shipped chat-turn metering (console, S) → **U10** `Create tickets` under the
report + drafts in the lane + board input → Research page (ui, after C5 and U9).
Exit: a research answer becomes an approved GitHub issue from the console with no hand edits.

## Phase D — wave 3: onboarding (TASK-586; file after F3 sets the bootstrap cap)

Legs (7): **P4** new-project interview (≤5 questions → scaffold + first tickets per the onboarding
SOP) · **P5** legacy scan (repo, README, docs/, issues, last 50 PRs; cap + degrade path; after P12
results) · **P6** external docs v1 (URL/upload → `.agent/system/`) · **C13** bootstrap trigger +
cap + status (after P5) · **U14** interview UI (after P4) · **U15** legacy path UI (after P5, P6) ·
**D6** Figma onboarding (founder). Exit: a new tenant reaches Queued tickets from signup on both
paths, bootstrap cost ≤ cap on the four TASK-581 fixture repos.

## Phase E — wave 4: trust close-out (TASK-587; file after wave 1 exit)

Legs (7): **P7** gateway-edge scrubber (`secretpatterns` + env-name rule) · **P8** tool-call line
events, 7-day retention · **C9** `/ws/dashboard` proxy (after P7, P8) · **U13** live log tail on
Running (after C9) · **C12** Postgres RLS · **I1** egress allowlist proxy (Nelya) · **S8** Security
page (site; after P11 report exists). Exit: live log on a Running card shows no secret in a
seeded-secret run; RLS on; Security page live with the pen-test date.

## Phase F — wave 5: post-launch (TASK-588; file after the first 5 design partners are live)

Legs (6, §9): **C11/U16** Viewer role + actor display · **P13** deploy templates VPS + AWS ·
**P6-v2** docs ingestion connectors (decide after 3 legacy tenants) · **P14** PM-only mode (after
TASK-460 gates) · **U18** docs edit · **U17** mobile re-land on the new design.

## Phase G — humans (parallel; triggers)

| Item | Owner | Trigger / due | Unblocks |
|---|---|---|---|
| F1 legal entity, address, email, law, email provider, no-analytics | founder | skipped today; next session | S1 legal pages → Paddle resubmit (F2) |
| F4 review Nelya's deploy PR | founder + Navigator | when the PR appears on pilotcloud-site | S2 first deploy → wave-0 copy live |
| F3 run TASK-581 matrix on the box, pick cap | founder (operator step, ≤ $250 guard) | after pilot#5788 merged | Phase D |
| O3 20-agency list + one-page offer | founder | after wave-0 deploy | design partners |
| D1–D5, D7 Figma pages | founder | before U6/U8/U11 are filed (give Pilot exact values) | wave-1 UI legs |
| I2 pen test on canary, I3 SES sender | Nelya | after pilot#5793 merged / anytime | wave-1 exit, C6 |
| D6 Figma onboarding | founder | before Phase D UI legs | U14/U15 |

## Phase H — docs hygiene (one chore, any time after Phase A)

Rewrite the contradicted lines rather than append: `saas-architecture.md` (Stripe → Paddle, BYOK-only
→ platform key + $300 ceiling, $500 → $499, "read-only cost display" → no-dollars rule),
`TASK-405` header (price, Paddle), `TASK-546` (tick invites/caps/chat-cap as shipped; mark D-items
closed per §4), `saas-roadmap.md` (add S5 entries = waves 1–4). One `docs:` commit; update
`DEVELOPMENT-README.md` § Current State to point at the positioning doc.

## Carried from TASK-496 (archived 2026-11-02)

Repo-settings decisions still open, verified 2026-11-02 via the GitHub API:

- ~~Branch protection on `qf-studio/pilot` main — none~~ **DONE 2026-11-02**: required checks `test` + `lint` (strict off), no review count, force-push and deletion blocked, admins not enforced (founder docs pushes keep working; the daemon only merges PRs on green). Applied via `gh api -X PUT repos/qf-studio/pilot/branches/main/protection`.
- **Tag ruleset on `qf-studio/pilot-console` — none**; tags reach `prod-0.1.49`. Decide: restrict `prod-*` creation to the release workflow identity.
- Nelya open items stay under TASK-495. infra#35/#36 need a human author (executor refuses the isolation-harness package).

## Verification (end-to-end, per phase)

- Phase A: `git log origin/main -1` shows the close-out commit; `gh issue list --repo qf-studio/pilot
  --label pilot` shows the two new issues; `graph_manager.py --action stats` memory count +4;
  `.agent/.context-markers/.active` names the new marker.
- Phases B–F: each wave's task doc `## Refs` lists every leg URL; `nav-dispatch-legs.py --dry-run`
  exits 0 before every filing; wave exit criteria above checked on the canary tenant (browser pane on
  the console, `~/bin/pilot-board` for the daemon, box DB via SSM for ledger claims — name the source).
- Phase G: TASK-515 § Refs records the facts and the deploy run URL; `curl -sI https://pilotcloud.dev/`
  returns 200 via CloudFront.
- Phase H: `grep -rn "Stripe\|\$500\|\$299" .agent/system/saas-architecture.md .agent/tasks/TASK-405*`
  returns no live-plan references.
