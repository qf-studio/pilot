# SOP: `gh issue close/edit/comment` requires the bare issue number, never the prefixed task ID

**Source incident**: GH-4405 — 2026-07-17, canary epic GH-95 and pointer epic GH-8 on the AWS daemon (v2.241.1-3-g70b28845). Three call sites in `internal/executor/epic.go` (`handleSubIssueCoverageGap`'s label/comment, and `executeSubIssuesTracked`'s parent close) passed `Task.ID` ("GH-95") straight to `gh issue close/edit/comment`. Every call failed with `invalid issue format: "GH-95"` and silently degraded to a WARN log — parents never closed, never got `pilot-needs-clarification`, never got the coverage-gap comment.

## Rule

Never pass `Task.ID` (the human-readable, prefixed form like `"GH-95"`) as the positional issue argument to `gh issue close`, `gh issue edit`, or `gh issue comment`. Use `Task.GHIssueRef()` (`internal/executor/runner.go`) instead:

```go
if err := r.CloseIssueWithComment(ctx, projectPath, parent.GHIssueRef(), comment); err != nil { ... }
```

`GHIssueRef()` prefers `SourceIssueID` (already bare for GitHub-sourced tasks) and falls back to stripping the `"GH-"` prefix from `ID`.

## Why

`gh issue <subcommand> <id>` accepts a bare number (`95`), a `#`-prefixed number (`#95`), or a full URL — never Pilot's internal `"GH-95"` task-ID format. This is easy to miss because:

- `Task.ID` is the field used everywhere else in logs, ledger events, and human-readable comment text ("✅ Completed as part of GH-95") — those uses are correct and should **not** change.
- The failure is non-fatal by design (label/comment/close are all best-effort, WARN-and-continue), so it never surfaces as a build/test failure or a loud error — only as a WARN buried in daemon logs and epics that mysteriously never close.
- `gh issue create`, `gh issue list --search "... in:body"`, and `gh pr create/list` do **not** take an issue-number positional argument the same way — don't over-apply this rule to those call sites (see `queryRecentSubIssues`/`recoverExistingSubIssues` in `epic.go`, which correctly search body text for the prefixed `"Parent: GH-95"` string).

## Where this matters

Any new `gh issue close|edit|comment|view <id>` call site fed from a `*Task` or `plan.ParentTask`. Grep for `parent.ID` / `plan.ParentTask.ID` near `exec.Command(Context)?(ctx, "gh"` before adding a new call site — if the ID feeds a `gh` CLI positional argument, use `GHIssueRef()`; if it feeds a log field, ledger event, or human-readable comment/search-body text, keep `.ID`.

## Prevention

- `Task.GHIssueRef()` (runner.go, next to `LogExecutionID()`) is the single conversion point — use it, don't reimplement `strings.TrimPrefix(id, "GH-")` inline (see `postTitleRejectionEscalation` in `title_rejection.go` for the pre-existing correct pattern this SOP generalizes).
- Tests that assert on captured `gh` CLI invocation args (fake `gh` script + log file, see `gh4405_test.go`, `gh4300_test.go`, `gh3938_test.go`) should compare against `parent.GHIssueRef()`, not `parent.ID`.
