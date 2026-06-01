# TASK-346: ListIssues fetches only the first 30 issues — candidates silently truncated (C6)

## Context

`ListIssues` (`internal/adapters/github/client.go:392`) builds
`/repos/{o}/{r}/issues?` with only `state`/`sort`/`since` params — **no `per_page` or `page`** — then
does a single `doRequest`. GitHub defaults to 30 items/page (caps at 100). So the poller's candidate
universe in `checkForNewIssues` and `findOldestUnprocessedIssue` (label mode) is silently limited to the
first 30 issues. Because labels are filtered client-side AFTER fetching, even those 30 may include
non-pilot issues, further shrinking the effective pilot-labeled set. On a repo with >30 open issues,
older pilot-labeled issues beyond page 1 are never seen — never dispatched, and the "oldest first"
guarantee breaks. Contrast `ListPullRequests` (`client.go:550`) which paginates correctly with
`per_page` + `page`.

## Approach

Add `per_page=100` and loop over `page=1,2,...` until a short page is returned (mirror the existing
`ListPullRequests` pagination). At minimum widen the single page to 100 and log when a full page is
returned so truncation is observable.

> **CI gate (kickoff #4):** the fix lives in `client.go` (`ListIssues`), NOT `poller.go`. Do not add
> per-candidate API calls in the poller loop — `stress/TestMemory_ProcessedMapGrowth` busy-waits over
> 1000 fresh issues with a 30s ctx and will time out CI if the parallel poll path gets slower per issue.
> Pagination inside `ListIssues` is safe (one extra request per 100 issues, not per candidate).

## Acceptance

- [ ] `ListIssues` requests `per_page=100` and paginates over `page=N` until a short page returns.
- [ ] Test: a stubbed 2-page response (100 + 15) returns all 115 issues, in order.
- [ ] Existing label-filter behavior preserved (client-side filter still applied to the full set).
- [ ] `make test` green for `internal/adapters/github`; `make lint` clean.

## Refs

- Findings ledger: `.agent/tasks/TASK-322-security-audit-findings.md` (C6, medium)
- Kickoff: `.agent/tasks/TASK-342-wave3-kickoff.md` (gate #4 — stress-test starvation)
- File: `internal/adapters/github/client.go:392` (pattern at `:550` ListPullRequests)
