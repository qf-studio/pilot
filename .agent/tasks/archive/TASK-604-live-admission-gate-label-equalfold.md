# TASK-604: github — live admission gate compares the trigger label case-insensitively

**Status**: ✅ Merged 2026-10-27 11:44Z — #5889 → PR#5893. **Post-merge review not posted.**
**Spec refs**: the review comment on the PR (APPROVE-WITH-NOTES), issue body as filed (Context / Scope / Acceptance / Out of scope; one targeted `-run` evidence bullet, SOP Rule 5d).
**Origin**: 10-27 review batch of the four unreviewed 10-26 merges (PR#5870/#5871/#5872/#5875) — own read + independent second read + `go test -race` + mutation pins.

## Why
- `githubEventHasLabel` uses `==`; SDK `HasLabel` and the webhook use `EqualFold`; GitHub is case-insensitive. `pilot_label: Pilot` vs repo `pilot` now never admits (skip `trigger_label_removed`, backoff re-armed each offer). Box unaffected (lower-case).

## Legs — pilot (daemon)

### LEG 1 — repo: qf-studio/pilot — issue pilot#5889
Body as filed. Path pre-flight passed on every backticked path.

## Refs
- https://github.com/qf-studio/pilot/issues/5889
