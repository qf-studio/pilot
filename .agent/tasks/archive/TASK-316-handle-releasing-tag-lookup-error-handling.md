---
name: task-316-handle-releasing-tag-lookup-error-handling
description: P1 — Defense-in-depth fix for handleReleasing GetTagForSHA error fallthrough that can cause infinite re-tag-attempt loops
metadata:
  type: task
  priority: P1
  area: autopilot
---

# TASK-316: `handleReleasing` fallthrough on `GetTagForSHA` error

**Status**: ✅ Shipped (commit `e77a94bf`) — both paths landed (GetTagForSHA transient-retry + duplicate-tag→success). Archived 2026-06-01. Verified live on `main` during TASK-309/#3188 triage; the B2 attempt-cap was deemed out of scope (TASK-316 stopped the freeze).
**Created**: 2026-05-27
**Priority**: P1 (defense-in-depth — the loop only triggers if TASK-314 isn't fixed, but the bug is real and worth hardening)
**Area**: `internal/autopilot/controller.go`

---

## Context

**Problem**:
`handleReleasing` (controller.go:1659–1671) calls `GetTagForSHA` to short-circuit re-releasing an already-tagged commit. On error, it logs a warning and **falls through** to `CreateTagForRepo`. If the tag actually exists (we just couldn't see it), the create call fails with "tag already exists" or similar, the function returns an error, and the PR remains in `activePRs` at `Stage=StageReleasing` — re-entered every poll cycle, retrying forever.

```go
existingTag, err := c.ghClient.GetTagForSHA(ctx, owner, repo, prState.HeadSHA)
if err != nil {
    c.log.Warn("failed to check existing tags", "error", err)
    // Continue anyway - worst case we get a duplicate tag error  ← incorrect comment
} else if existingTag != "" {
    c.removePR(prState.PRNumber)
    return nil
}
```

The "worst case duplicate tag error" comment is wrong: a duplicate-tag error from `CreateTagForRepo` propagates up as a returned `error`, which means `removePR` is never called and the PR sticks. This is the second of two compounding bugs that caused the 2026-05-27 stuck-`StageReleasing` dashboard symptom (TASK-314 is the primary; this is defense-in-depth).

**Goal**:
On `GetTagForSHA` error, retry next poll (return the error) rather than blindly continuing to `CreateTagForRepo`. Optionally: when `CreateTagForRepo` itself fails with a duplicate/conflict error, classify that as "already released" and `removePR`.

---

## Acceptance Criteria

- [ ] When `GetTagForSHA` returns an error, `handleReleasing` returns the error (retry next poll) instead of falling through to `CreateTagForRepo`
- [ ] When `CreateTagForRepo` returns a duplicate-tag / 422-conflict error, `handleReleasing` treats it as success (the tag exists), calls `removePR`, returns nil
- [ ] Unit tests for both paths: (a) `GetTagForSHA` errors → no tag-create attempted, returns error; (b) `CreateTagForRepo` returns duplicate-tag error → `removePR` called, returns nil
- [ ] Existing tests pass (especially the race-condition guard at lines 1655–1670 for rapid-fire merges)

---

## Implementation

### Path 1: Treat `GetTagForSHA` errors as transient

```go
existingTag, err := c.ghClient.GetTagForSHA(ctx, owner, repo, prState.HeadSHA)
if err != nil {
    return fmt.Errorf("failed to check existing tags for PR #%d: %w", prState.PRNumber, err)
}
if existingTag != "" {
    c.log.Info("commit already tagged, skipping release",
        "pr", prState.PRNumber,
        "sha", ShortSHA(prState.HeadSHA),
        "tag", existingTag,
    )
    c.removePR(prState.PRNumber)
    return nil
}
```

### Path 2: Classify duplicate-tag as success in `CreateTagForRepo` error handling

After `CreateTagForRepo` call (line 1709–1712), inspect the error. If it's a duplicate-tag / 422-conflict:

```go
tagName, err := c.releaser.CreateTagForRepo(ctx, owner, repo, prState, newVersion)
if err != nil {
    if isDuplicateTagError(err) {
        c.log.Info("tag already exists at HEAD SHA — treating as released",
            "pr", prState.PRNumber,
            "sha", ShortSHA(prState.HeadSHA),
        )
        c.removePR(prState.PRNumber)
        return nil
    }
    return fmt.Errorf("failed to create tag: %w", err)
}
```

`isDuplicateTagError` should detect both GitHub's 422 "Reference already exists" and any wrapper-level conflict signal. Keep the predicate narrow — don't swallow generic 422s.

### Files

- `internal/autopilot/controller.go:1659–1712` — change fallthrough behavior + add duplicate-tag classifier
- `internal/autopilot/release.go` (or wherever `CreateTagForRepo` lives) — may need to surface a typed error
- `internal/autopilot/controller_test.go` — add unit tests for both branches

---

## Out of Scope

- The scanner re-add loop itself — fixed by TASK-314
- Adding a global circuit breaker for "PR has been in StageReleasing > N minutes, force-remove" — interesting follow-up, separate issue
- Telegram/Slack notification changes — orthogonal

---

## Technical Decisions

| Decision | Options | Recommended | Reasoning |
|---|---|---|---|
| `GetTagForSHA` error policy | Fall through (today), retry (return err), assume-no-tag | Return err / retry | Existing tag is a release-critical invariant; better to retry than to attempt a duplicate create |
| Duplicate-tag classification | New typed error, string-match, HTTP status check | Typed error from release client | Avoids string-match brittleness; lets test cover the path cleanly |

---

## Verify

```bash
go test ./internal/autopilot/ -run TestHandleReleasing -v
```

Manual: inject a `GetTagForSHA` failure (e.g. GitHub 5xx) via a fake — verify PR stays at StageReleasing for ONE poll cycle (returning error) and is cleared on the next successful call, instead of looping on duplicate-tag forever.

---

## Done

- [ ] Both unit tests added and green
- [ ] `handleReleasing` no longer attempts `CreateTagForRepo` after a `GetTagForSHA` error
- [ ] Duplicate-tag from `CreateTagForRepo` is non-fatal and clears the PR

---

## Refs

- Sibling fix: TASK-314 (primary cause of the 2026-05-27 stuck-release dashboard)
- Code: `internal/autopilot/controller.go:1659–1712`
- Investigation transcript: this session (2026-05-27)

---

**Last Updated**: 2026-05-27
