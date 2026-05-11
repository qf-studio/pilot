# TASK-40: DECLINED-with-Reason Structured Output (P3 of Sonnet success-rate plan)

**Status**: 🚧 In Progress
**Created**: 2026-05-07
**Assignee**: Pilot

---

## Context

14 of 25 Sonnet 4.6 failures (56%) are "Claude completed but made no code changes after retry". Today's retry prompt at `internal/executor/runner.go:2176-2188` says "implement or don't bother" with no path for Sonnet to signal "I genuinely cannot, here's why". So the 14 failures collapse together: half are correct judgements (issue is unactionable) and half are model giving up too early.

Goal: rewrite the retry prompt to require structured output. After retry, Sonnet must either commit code OR end its message with `DECLINED: <one-line reason>`. The DECLINED case becomes a structured human-handoff (label issue `pilot-needs-clarification`, post comment with reason, exit with new status `declined`) — not a failure. Convert ~half of the 14 no-changes "failures" into a deliberate human-in-the-loop signal and cleanly distinguish model error from genuine ambiguity.

This is P3 of `~/.claude/plans/use-nav-research-prepare-delegated-dragon.md`.
P1 (TASK-38, GH-2735) shipped as v2.130.0.
P2 (TASK-39, GH-2743) shipped as v2.130.1.

## Success Criteria

- [ ] Retry prompt at `runner.go:2176-2188` rewritten to instruct Sonnet that on retry, it must either commit code or end with `DECLINED: <reason>`
- [ ] Helper `parseDeclinedReason(text string) (string, bool)` parses the DECLINED line out of the final assistant message
- [ ] When DECLINED is present and no commits exist after retry: execution exits with status `declined`, the GitHub issue gets the `pilot-needs-clarification` label and a comment containing the reason
- [ ] When DECLINED is absent and no commits exist: existing failure behaviour preserved
- [ ] GH poller skips re-dispatching issues with `pilot-needs-clarification` label until label removed
- [ ] Dashboard counts include a `declined` bucket alongside `completed`/`failed`
- [ ] All existing tests pass; new tests cover parse, declined-path, label flow
- [ ] `make test`, `make lint`, `make build` pass

---

## Implementation Plan

### Phase 1: Parse helper

In `internal/executor/runner.go`, add near the top (next to `IsPermanentFailure`):

```go
// declinedRegex matches the structured DECLINED line at the end of an assistant
// message. The reason is captured group 1, trimmed of whitespace.
var declinedRegex = regexp.MustCompile(`(?m)^DECLINED:\s*(.+?)\s*$`)

// parseDeclinedReason returns the reason and true if the assistant text ends
// with a DECLINED line. Falls back to scanning the last 8 non-empty lines so a
// terminal whitespace blob doesn't hide the marker.
func parseDeclinedReason(text string) (string, bool) {
    if text == "" {
        return "", false
    }
    // ... parse and return
}
```

Tests in `runner_test.go`: empty, no marker, marker mid-text, marker at end, multiline reason (only first line captured), trailing whitespace, multiple DECLINED lines (last one wins).

### Phase 2: Rewrite retry prompt

`runner.go:2176-2188`. Replace the current `retryPrompt` with one that explicitly offers two outcomes. Keep it short:

```
## Retry: No Changes Detected

Your previous run completed but made no code changes. This task requires
either an actual implementation or a structured decline.

**Original Task:** %s

You MUST do exactly ONE of the following:

1. Read the task carefully, implement the required changes, and create at
   least one commit. Do NOT just analyze or plan — actually write and
   commit code.

2. If the task cannot be implemented as stated (ambiguous scope, missing
   info, conflicting acceptance criteria, blocked by external dependency),
   end your final message with a single line:

       DECLINED: <one-line reason>

   Examples:
       DECLINED: issue body does not specify which file to modify
       DECLINED: acceptance criteria conflict — needs human clarification
       DECLINED: requires API credentials not available in env

Do NOT use DECLINED to avoid hard work. Use it only when implementation
is genuinely blocked.

%s
```

### Phase 3: Branch on parse after retry

`runner.go:2228-2253` — when `commitCount == 0` after retry, parse the assistant text first:

```go
refusal := ""
if backendResult != nil {
    refusal = strings.TrimSpace(backendResult.LastAssistantText)
    if retryResult != nil && strings.TrimSpace(retryResult.LastAssistantText) != "" {
        refusal = strings.TrimSpace(retryResult.LastAssistantText)
    }
}

if reason, ok := parseDeclinedReason(refusal); ok {
    // Structured decline — not a failure.
    result.Success = false
    result.Declined = true        // NEW field on Result
    result.DeclinedReason = reason
    if backendResult != nil {
        backendResult.ErrorType = string(ErrorTypeDeclined) // NEW
    }
    log.Info("task declined by executor with reason",
        slog.String("task_id", task.ID),
        slog.String("reason", reason),
    )
    r.reportProgress(task.ID, "Declined", 100, "Declined: " + reason)
    // ... return early; let caller post comment + label
} else {
    // Existing failure behaviour preserved.
}
```

Add `Declined bool` and `DeclinedReason string` to whichever Result type runner returns (likely `Result` in the same file or `internal/executor/types.go`).

Add `ErrorTypeDeclined ErrorType = "declined"` near other ErrorType constants.

### Phase 4: GitHub label + comment on decline

The runner already has access to a GH client for PR creation. After a decline:
- Post a comment on the source issue: a markdown block with the reason, who declined (model name), and a hint that removing `pilot-needs-clarification` will allow re-dispatch
- Add label `pilot-needs-clarification` to the issue
- Remove `pilot-in-progress` if present

Locate the existing label-management helpers in `internal/adapters/github/`. If not present, add a small helper.

### Phase 5: Poller skip-on-label

`internal/adapters/github/poller.go:660,877,916` — already check existing labels for blocking states. Add `pilot-needs-clarification` to the blocking set so the poller doesn't re-dispatch a declined issue until a human removes the label.

Pattern to mirror: how `pilot-failed` is treated. Search `pilot-failed` to find the exact set.

### Phase 6: Dashboard `declined` count

`internal/dashboard/...` — find where `completed`/`failed` counters are computed and add a `declined` parallel counter sourced from `executions.status='declined'`.

If status is computed from a fixed list, extend the list. Schema check: `internal/memory/store.go` — confirm `executions.status` has no CHECK constraint blocking new values. If it does, add `declined` to the allow-list.

### Phase 7: Tests

- `runner_test.go`: `parseDeclinedReason` table-driven; `TestRunner_DECLINED_Path` (mock retry returns DECLINED, assert status=declined, label applied, no failure escalation); `TestRunner_NoChanges_NoDecline` (retry without DECLINED still fails as today)
- `poller_test.go`: assert `pilot-needs-clarification` skips dispatch
- Dashboard test (if any test infra) for the new counter

### Phase 8: Documentation

- Update `docs/content/features/...` with one paragraph describing the new behavior + label
- Mention the `pilot-needs-clarification` label in the user-facing runbook

---

## Out of Scope

- Pre-flight intent judge (P4)
- Effort routing (P5)
- Retry budget tuning beyond DECLINED path
- Slack/Telegram notification for declines (could be follow-up)
- Auto-clarification flow that asks the user follow-up questions (out of scope for v1)

---

## Verify

```bash
make test ./internal/executor/... ./internal/memory/... ./internal/adapters/github/...
make lint
make build
```

Post-merge production verification (after the next batch of issues):

```sql
-- Expect: a non-zero declined count if any issues were genuinely ambiguous
SELECT status, COUNT(*) FROM executions
WHERE model_name='claude-sonnet-4-6'
  AND created_at > '<merge-date>'
GROUP BY status;
```

Manual verification: file an intentionally-ambiguous Pilot issue (e.g., "improve the thing"). Confirm Sonnet returns DECLINED, the issue gets `pilot-needs-clarification`, and the poller skips it on the next tick.

---

## Done

- [ ] `parseDeclinedReason` implemented + tested
- [ ] Retry prompt rewritten with explicit DECLINED option
- [ ] Result type extended; ErrorTypeDeclined added
- [ ] Label + comment posted on decline
- [ ] Poller skips `pilot-needs-clarification`
- [ ] Dashboard shows declined count
- [ ] Docs updated
- [ ] PR opened, CI green, auto-merged, auto-released

---

## Notes

- ~150 LoC including tests, dashboard wiring, label flow.
- **Single Pilot issue with a single AC list** — to avoid epic decomposition. Per memory `bug_decomposer_no_meta_marker.md` and `bug_pilot_ghost_closes.md`, decomposed multi-PR work has higher failure surface.
- Per `pattern_burst_auto_release_starvation.md`: ship alone (no concurrent Pilot issues filed).
- Master plan: `~/.claude/plans/use-nav-research-prepare-delegated-dragon.md` (P3 of 5)

---

**Last Updated**: 2026-05-07
