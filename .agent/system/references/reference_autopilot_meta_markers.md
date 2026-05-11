---
name: autopilot-meta body marker locations
description: Code pointers for the two distinct autopilot-meta markers — decomposer's "parent + inherited-spec" vs feedback loop's "branch + pr + iteration". Easy to confuse.
type: reference
originSessionId: 89fe3897-6bc2-4725-a1f2-8635b79860b3
---
Two distinct `<!-- autopilot-meta -->` body markers exist with different formats and purposes. They are NOT interchangeable.

## Marker 1: Decomposer subtask marker

**Format:** `<!--autopilot-meta\nparent: GH-N\ninherited-spec: true\n-->`

**Generated at:**
- `internal/executor/epic.go:820` — adapter path (`createSubIssuesViaAdapter`)
- `internal/executor/epic.go:924` — GitHub CLI path (`createSubIssuesViaGitHub`)

**Purpose:** Marks sub-issues created during epic decomposition. `parent` field is `plan.ParentTask.ID` (e.g., `GH-2753`). `inherited-spec: true` means the body was inherited from the parent issue's spec.

**No iteration counter.** Children created by re-decomposition are indistinguishable from first-generation children.

## Marker 2: Feedback-loop iteration marker

**Format:** `<!-- autopilot-meta branch:pilot/GH-N pr:M iteration:K -->`

**Generated at:**
- `internal/autopilot/feedback_loop.go:215` (CI failure cascade)
- `internal/autopilot/feedback_loop.go:262` (review feedback cascade)

**Parsed by regex:** `var iterationRe = regexp.MustCompile(\`<!-- autopilot-meta.*?iteration:(\d+).*?-->\`)` at `internal/autopilot/controller.go:28`.

**Note:** The regex matches Marker 1's format too (because `.*?` is permissive), but Marker 1 has no `iteration:` field, so `iterationRe.FindSubmatch` returns no match. Mixing them up in mental models is easy.

## Common confusion

`handleMergeConflict` (`controller.go:1688-1729`) does NOT create new issues. It only closes the PR and removes `pilot-in-progress`. What looks like "auto-re-file after conflict" is actually:

1. `handleMergeConflict` closes PR + strips `pilot-in-progress`
2. Poller re-picks the original issue
3. Runner classifies it as epic AGAIN
4. Decomposer fires AGAIN, generating Marker 1 sub-issue

The marker's appearance after a conflict-close is therefore a SECOND decomposition, not a re-file. See `pattern_decomposer_label_evaporation.md` for why this cascades indefinitely.
