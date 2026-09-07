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

**Format:** `<!-- autopilot-meta branch:pilot/GH-N pr:M iteration:K [source:S] [sha:FULLSHA] -->`

`source:S` and `sha:FULLSHA` are optional — only appended when `PRState.IssueNumber`/`PRState.HeadSHA` are populated.

**Generated at:** both `CreateFailureIssue` (CI failure cascade) and `CreateReviewIssue` (review feedback cascade) in `internal/autopilot/feedback_loop.go` build the field list via the shared `autopilotMetaFields` helper (`feedback_loop.go:609`), so both flavors always carry identical fields.

**Parsed by regex:**
- `var iterationRe = regexp.MustCompile(\`<!-- autopilot-meta.*?iteration:(\d+).*?-->\`)` at `internal/autopilot/controller.go:28`.
- `sha:` (GH-5348) is parsed separately by `parseAutopilotSHA` in `cmd/pilot/handlers.go`, feeding `Task.FixFromSHA`. It carries the **full** original PR head commit SHA — not the 7-char prefix already shown in the human-readable "Context" section of the issue body — so an autopilot-fix task can recreate its worktree from the exact original commit (`GitOperations.ResolveFixContinuationBaseRef`) if the original branch was deleted (e.g. on PR close). Without it, a deleted fix branch was silently rebuilt from `main`, discarding the original diff (pilot-console #275 incident).

**Note:** The regex matches Marker 1's format too (because `.*?` is permissive), but Marker 1 has no `iteration:` field, so `iterationRe.FindSubmatch` returns no match. Mixing them up in mental models is easy.

## Common confusion

`handleMergeConflict` (`controller.go:1688-1729`) does NOT create new issues. It only closes the PR and removes `pilot-in-progress`. What looks like "auto-re-file after conflict" is actually:

1. `handleMergeConflict` closes PR + strips `pilot-in-progress`
2. Poller re-picks the original issue
3. Runner classifies it as epic AGAIN
4. Decomposer fires AGAIN, generating Marker 1 sub-issue

The marker's appearance after a conflict-close is therefore a SECOND decomposition, not a re-file. See `pattern_decomposer_label_evaporation.md` for why this cascades indefinitely.
