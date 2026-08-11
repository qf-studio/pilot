# fix(autopilot): owner-death recovery — a designated fix issue that dies must re-arm the source or escalate

**Status**: ✅ Merged 2026-08-11 → PR#4849 (15:50Z). Post-merge review agent ran at ~18:30Z — check PR#4849 comments for the verdict (incl. interaction risk with then-unmerged PR#4846).
**Created**: 2026-08-11
**Assignee**: Pilot

## Context

After PR#4840 (GH-4826), a CI-failure close designates exactly one recovery owner — usually the spawned fix issue. Post-merge review found there is **no handling for the owner dying**: the GH-4459 comment (internal/autopilot/controller.go:2673) claims the fix issue "cleared preflight admission," but `CreateFailureIssue` (internal/autopilot/feedback_loop.go:221-325) only creates the issue — the preflight judge (`reject_vague`, internal/executor/intent_judge.go) runs much later. A designated fix issue rejected at preflight leaves **zero owners**: the PR is closed, retry is suppressed by the designation, nothing ships. In the original GH-4820 incident it was the (then-buggy) second arm that actually delivered the fix — with exclusivity now enforced, that accidental safety net is gone.

Adjacent hole: the dedup path returns a previously recorded fix issue with **no open-state check** (feedback_loop.go:255-263; state_store.go:1191) — a closed/dead fix issue can be re-designated owner.

## Implementation

1. **Detect owner death.** A designated fix issue that (a) is declined by the preflight judge, or (b) is closed without a merged PR, is a dead owner. Detection may hook the decline path (the judge already records `declined-preflight` executions) and/or a periodic scan of designated-owner issues' open/merged state — pick the seam that needs no new poller if one exists.
2. **React**: on owner death, either re-arm the source (clear the designation so the normal retry ladder applies, respecting retry generations) or escalate `needs-human` when retries are exhausted — never silently strand. Log + alert through the existing alerts engine.
3. **Dedup open-state check**: before returning a deduped fix issue as owner (feedback_loop.go:255-263), verify it is still open; if closed-unmerged, treat as dead and fall through to creation or escalation.
4. **Tests**: table-driven owner-death matrix — preflight-declined / closed-unmerged / closed-merged (not death) / dedup-returns-closed — asserting exactly one live owner or an escalation in every row.

Out of scope: persistence of the designation itself (separate issue, dispatched alongside); changes to the preflight judge's decision logic.

## Acceptance

- A preflight-declined designated fix issue results in the source re-armed or `needs-human` — reproduced in a test modeled on the GH-4820 shape.
- Dedup can no longer designate a closed-unmerged fix issue.
- No new poller/goroutine unless demonstrably unavoidable; alerts fire on every owner-death event.

## Refs

- Review verdict: https://github.com/qf-studio/pilot/pull/4840#issuecomment-5253781955 (D2)
- Prior work: PR#4840 (GH-4826), incident GH-4820
