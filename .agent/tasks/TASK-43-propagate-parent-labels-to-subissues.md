# TASK-43: Propagate Parent Labels to Sub-Issues in CreateSubIssues

**Status**: 🚧 Planned
**Created**: 2026-05-07
**Assignee**: Pilot (via GH issue)

---

## Context

**Problem**: When the epic decomposer creates sub-issues, both creation paths
hardcode the label set to `[]string{"pilot"}`:

- `internal/executor/epic.go:883` (`createSubIssuesViaAdapter`):
  `r.subIssueCreator.CreateIssue(ctx, parentID, title, body, []string{"pilot"})`
- `internal/executor/epic.go:1003` (`createSubIssuesViaGitHub`):
  `args := []string{"issue", "create", "--title", title, "--body", body, "--label", "pilot"}`

This means **no parent label propagates to children**: not `no-decompose`, not
`area:*`, not `priority:*`, none. The `no-decompose` label only blocks
decomposition at the runner's opt-out check (`runner.go:1214-1216`) for the
*current* task. As soon as a child sub-issue is created without the label and
the poller picks it up, the runner classifies it as a fresh epic and
decomposes again.

**Empirical evidence (2026-05-07):** the cascade GH-2753 → GH-2754 → GH-2777
re-decomposed at every level *because* `no-decompose` evaporated on each
sub-issue creation. Even Fix #2 (TASK-42, prose-hint heuristic, shipped
v2.131.1) only protects originally-filed issues — it does not survive
sub-issue creation either, since `task.Body` is the *subtask description*, not
the parent body.

**Goal**: Propagate the parent's propagatable labels into every sub-issue
creation call, so opt-outs and area/priority routing survive decomposition.

**Success Criteria**:
- [ ] `filterPropagatableLabels([]string) []string` helper exists, exposed for tests
- [ ] Allow-listed propagation: `no-decompose`, `area:*`, `priority:*` (extensible)
- [ ] Block-listed (never propagate): `pilot-done`, `pilot-failed`, `pilot-in-progress`, `pilot-superseded`, `pilot-needs-clarification`, `pilot`
- [ ] Both creation paths (`createSubIssuesViaAdapter`, `createSubIssuesViaGitHub`) use the helper
- [ ] Sub-issues are created with `pilot` + filtered parent labels (deduplicated)
- [ ] Existing tests still pass; new tests cover the helper + both paths
- [ ] `make test`, `make lint`, `make build` all pass

---

## Implementation Plan

### Phase 1: Add `filterPropagatableLabels` helper

**File**: `internal/executor/epic.go` (new helper near top of file or in a new
small section, ~25 lines)

```go
// propagatableLabelAllowlist defines exact labels that survive parent→child
// propagation across decomposition. Anything not in this set or matching one
// of the prefixes (area:, priority:) is dropped.
var propagatableLabelAllowlist = map[string]struct{}{
    "no-decompose": {},
    "no-plan":      {},
}

var propagatableLabelPrefixes = []string{"area:", "priority:", "scope:"}

// alwaysBlockedLabels are pilot lifecycle markers that must never propagate.
var alwaysBlockedLabels = map[string]struct{}{
    "pilot":                       {},
    "pilot-done":                  {},
    "pilot-failed":                {},
    "pilot-in-progress":           {},
    "pilot-superseded":            {},
    "pilot-needs-clarification":   {},
}

// filterPropagatableLabels returns the subset of parent labels that should be
// inherited by sub-issues during epic decomposition.
func filterPropagatableLabels(parentLabels []string) []string {
    out := make([]string, 0, len(parentLabels))
    for _, raw := range parentLabels {
        l := strings.ToLower(strings.TrimSpace(raw))
        if l == "" {
            continue
        }
        if _, blocked := alwaysBlockedLabels[l]; blocked {
            continue
        }
        if _, ok := propagatableLabelAllowlist[l]; ok {
            out = append(out, l)
            continue
        }
        for _, p := range propagatableLabelPrefixes {
            if strings.HasPrefix(l, p) {
                out = append(out, l)
                break
            }
        }
    }
    return out
}
```

### Phase 2: Wire helper into `createSubIssuesViaGitHub`

**File**: `internal/executor/epic.go:998-1004`

```go
// before
args := []string{
    "issue", "create",
    "--title", title,
    "--body", body,
    "--label", "pilot",
}

// after
labels := append([]string{"pilot"}, filterPropagatableLabels(plan.ParentTask.Labels)...)
args := []string{
    "issue", "create",
    "--title", title,
    "--body", body,
}
for _, l := range labels {
    args = append(args, "--label", l)
}
```

### Phase 3: Wire helper into `createSubIssuesViaAdapter`

**File**: `internal/executor/epic.go:883`

```go
// before
identifier, url, err := r.subIssueCreator.CreateIssue(ctx, parentID, title, body, []string{"pilot"})

// after
labels := append([]string{"pilot"}, filterPropagatableLabels(plan.ParentTask.Labels)...)
identifier, url, err := r.subIssueCreator.CreateIssue(ctx, parentID, title, body, labels)
```

### Phase 4: Tests

**File**: `internal/executor/epic_test.go`

Table-driven tests for `filterPropagatableLabels`:
- `["pilot", "no-decompose"]` → `["no-decompose"]`
- `["pilot", "no-decompose", "area:executor"]` → `["no-decompose", "area:executor"]`
- `["pilot", "pilot-done", "pilot-failed"]` → `[]`
- `["", "  ", "no-decompose"]` → `["no-decompose"]` (trims/empties skipped)
- `["No-Decompose"]` → `["no-decompose"]` (case insensitive)
- `["priority:p1", "scope:executor", "random-label"]` → `["priority:p1", "scope:executor"]`
- `[]` → `[]`

Integration assertions in existing or new test:
- `TestCreateSubIssuesViaGitHub_PropagatesNoDecomposeLabel` — given parent
  with `no-decompose`, the `gh issue create` invocation includes
  `--label no-decompose` (verify via mock exec or stub).
- `TestCreateSubIssuesViaAdapter_PropagatesParentLabels` — given parent with
  `area:foo`, verify the mock `subIssueCreator.CreateIssue` receives
  `[]string{"pilot", "area:foo"}`.

### Phase 5: Manual end-to-end check (post-merge)

1. File a test issue with `pilot` + `no-decompose` and a body that genuinely
   warrants decomposition.
2. Confirm the daemon does NOT decompose it (existing `no-decompose` already
   blocks at the parent level).
3. Manually remove `no-decompose`, leave `pilot` — daemon decomposes.
4. Inspect created sub-issues: each should have only `pilot` (since the parent
   no longer had `no-decompose` at decomposition time). This is the negative
   control.
5. Re-add `no-decompose` to the parent, force re-dispatch, repeat: each
   sub-issue should now carry `no-decompose`.

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|---|---|---|---|
| Allow vs block list | Pure allow / pure block / hybrid | Hybrid | Allow-list keeps lifecycle labels safe; prefix matchers (area:, priority:, scope:) extend without churn |
| Where helper lives | `epic.go` vs `decompose.go` vs new file | `epic.go` | Used only by `createSubIssues*`; co-located with callers |
| Lowercase normalization | Preserve case / normalize | Normalize | GitHub labels are case-insensitive at lookup; avoids `No-Decompose` vs `no-decompose` drift |
| Pass labels through `EpicPlan` | New field vs use existing `ParentTask.Labels` | Use `ParentTask.Labels` | Already populated from the original task's `Labels` in the planner |

---

## Dependencies

**Requires**:
- TASK-42 / GH-2783 (decomposer prose hints) — shipped v2.131.1. Complementary,
  not blocking.

**Blocks**:
- TASK-44 (multi-level grandparent close in `maybeCloseParentIssue`) — separate issue.
- TASK-45 (iteration counter on `inherited-spec` sub-issues) — separate issue.

---

## Verify

```bash
# Helper unit tests
go test ./internal/executor/... -run "TestFilterPropagatableLabels" -v

# Integration tests for both creation paths
go test ./internal/executor/... -run "TestCreateSubIssues" -v

# Full executor test suite (regression)
go test ./internal/executor/... -v

# Lint and build
make lint
make build
```

---

## Done

- [ ] `filterPropagatableLabels` exists in `internal/executor/epic.go`
- [ ] `createSubIssuesViaGitHub` uses it (`epic.go:998-1004`)
- [ ] `createSubIssuesViaAdapter` uses it (`epic.go:883`)
- [ ] Helper tests pass (8+ cases)
- [ ] Integration tests pass (both paths)
- [ ] PR merged, v2.131.x or v2.132.x tagged
- [ ] Manual e2e validation: `no-decompose` survives decomposition

---

## Notes

- Today's cascade chain GH-2753 → GH-2754 → GH-2777 is the canonical
  recurrence pattern this fix prevents.
- After this lands, the `inherited-spec` body marker becomes meaningful for
  iteration-counter work (TASK-45) — labels alone don't tell us the depth of
  re-decomposition.
- The autopilot `iterationRe` at `controller.go:28` is unrelated; it tracks
  feedback-loop iteration on PRs, not decomposition depth.

---

**Last Updated**: 2026-05-07
