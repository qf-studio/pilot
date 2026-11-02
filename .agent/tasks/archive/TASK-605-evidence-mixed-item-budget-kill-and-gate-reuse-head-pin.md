# TASK-605: executor — evidence never shows a budget-killed command as evidence; gate reuse skipped when HEAD moved

**Status**: ✅ Merged 2026-10-27 12:36Z — #5890 → PR#5895. **Post-merge review not posted.**
**Spec refs**: the review comment on the PR (APPROVE-WITH-NOTES), issue body as filed (Context / Scope / Acceptance / Out of scope; one targeted `-run` evidence bullet, SOP Rule 5d).
**Origin**: 10-27 review batch of the four unreviewed 10-26 merges (PR#5870/#5871/#5872/#5875) — own read + independent second read + `go test -race` + mutation pins.

## Why
- Mixed item: `allErrored` false once any command finished → budget-killed command's partial output under `## Evidence`, no marker; sibling's "Not verified" inside the fence.
- `newEvidenceGateOutputs` checks Passed/ExitCode/Command only; self-review can commit after the gates → reused `make test` output for a tree that is not the pushed SHA.

## Legs — pilot (daemon)

### LEG 1 — repo: qf-studio/pilot — issue pilot#5890
Body as filed. Path pre-flight passed on every backticked path.

## Refs
- https://github.com/qf-studio/pilot/issues/5890
