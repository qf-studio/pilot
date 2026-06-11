# TASK-318: SDK M1 — extract Plane adapter into studio-sdk (reference extraction)

**Status:** drafted (fire to `qf-studio/studio-sdk` as a `pilot`-labeled issue once GH-1 lands a clean PR)
**Repo (target of work / PR):** `qf-studio/studio-sdk`
**Source repo (read-only):** `qf-studio/pilot`
**Milestone:** SDK extraction M1 (first of the low-coupling adapters)

## Why Plane first

Lowest-risk reference extraction: pure stdlib (no third-party deps → no `go get`),
smallest of the low-coupling trio (~2.6k LOC incl tests), and only three
pilot-internal couplings (`logging`, `text`, `testutil`). It establishes the
pattern that M2–M6 follow, so **correctness of the seam matters more than speed**.

## Cross-repo source access (important)

The executor works in the **studio-sdk** worktree and does NOT have the pilot
source locally. To get it, clone pilot read-only into a temp dir using the
configured token, then port from there:

```bash
gh repo clone qf-studio/pilot /tmp/pilot-src -- --depth 1
# source files: /tmp/pilot-src/internal/adapters/plane/*.go
```

Do **not** add `/tmp/pilot-src` to the studio-sdk repo. Read, port, discard.

## Source files to port

From `/tmp/pilot-src/internal/adapters/plane/`:
`client.go`, `notifier.go`, `poller.go`, `sanitize.go`, `types.go`, `webhook.go`
and the tests `client_test.go`, `notifier_test.go`, `webhook_test.go`.

Target: **`sdk/integrations/plane/`** in studio-sdk (package `plane`).

## Import / type mapping

| Pilot source | studio-sdk replacement |
|---|---|
| `internal/text`.`SanitizeUntrusted` / `SanitizeUntrustedString` | `github.com/qf-studio/studio-sdk/sdk/util/text` (identical API — drop-in) |
| `internal/logging`.`WithComponent("plane")` (package-level) | injected logger (see refactor below) |
| `internal/testutil`.`FakePlaneAPIKey` | `github.com/qf-studio/studio-sdk/sdk/testutil` (create — see below) |
| local `IssueResult` | `sdk/core`.`IssueResult` |
| local `ProcessedStore` | `sdk/core`.`ProcessedStore` |
| local PR callback `func(prNumber int, prURL, issueID, headSHA, branchName string)` | `sdk/core`.`PRCreatedEvent` (set `IssueNodeID=""`) |

## Required refactors (more than import-swaps)

1. **Logger injection.** Replace every `logging.WithComponent("plane")` /
   `logging.WithComponent("plane-poller")` call with an injected logger.
   - The poller already has `WithPollerLogger(*slog.Logger)` — keep it; default to
     `slog.Default()` when unset.
   - Give `Notifier` a logger field too (constructor param or option), defaulting
     to `slog.Default()`. `*slog.Logger` satisfies `sdk/log.Logger`, so either type
     works; prefer `*slog.Logger` to match the existing poller option.
   - No package-level logging calls may remain.

2. **`sdk/testutil`.** Create `sdk/testutil/tokens.go` (package `testutil`) with
   obviously-fake constants — start with `FakePlaneAPIKey = "test-plane-api-key"`.
   (Future adapters add their own constants here.) Follow the project rule:
   **no realistic-looking tokens** (push-protection). Mirror pilot's
   `internal/testutil/tokens.go` style.

3. **Adopt `sdk/core` contract.** The ported package must:
   - Implement `sdk/core.Adapter` (`Name() string` → `"plane"`) and, where the
     original supports it, `sdk/core.Pollable` / `sdk/core.WebhookCapable`.
   - Use `sdk/core.IssueResult` and `sdk/core.ProcessedStore` instead of local copies.
   - Normalize the PR callback: replace `WithOnPRCreated(func(...5 args...))` with
     `OnPRCreated func(sdk/core.PRCreatedEvent)` in `PollerDeps`, or keep
     `WithOnPRCreated(func(sdk/core.PRCreatedEvent))` as an option — emit a
     `PRCreatedEvent{PRNumber, PRURL, IssueID, HeadSHA, BranchName}` (no node ID).
   - Issue handling: map the plane-native `*WorkItem` to `sdk/core.IssueEvent`
     (`IssueID`, `Title`←Name, `Body`←Description, `Labels`, `ProjectID`) at the
     boundary and invoke `sdk/core.IssueHandler` if a host handler is supplied.
     If a use needs plane-specific fields a normalized `IssueEvent` can't carry,
     keep a plane-typed option **in addition** and note the gap in a code comment —
     do not silently drop data.

4. **Zero pilot dependency.** After porting, `grep -r "qf-studio/pilot"
   sdk/integrations/plane` must return nothing. The SDK module must not import
   the pilot module at all.

## Acceptance criteria

- [ ] `sdk/integrations/plane/` compiles; `go build ./...` green in studio-sdk.
- [ ] Ported tests pass: `go test ./sdk/integrations/plane/...` (with `-race`).
- [ ] `go vet ./...` clean.
- [ ] `grep -r "qf-studio/pilot" sdk/` returns nothing (zero pilot deps).
- [ ] No new third-party module deps (plane is pure stdlib).
- [ ] No package-level logging; logger is injected, defaults to `slog.Default()`.
- [ ] `sdk/testutil/tokens.go` exists with `FakePlaneAPIKey` (obviously-fake value).
- [ ] PR targets `main`; description lists the import/type mappings applied.

## Verification

```bash
go build ./... && go vet ./... && go test -race ./sdk/integrations/plane/...
grep -rn "qf-studio/pilot" sdk/ ; echo "exit=$?  (want: no matches)"
```

## Notes / scope guard

- This is **bench-/SDK-only** structural work — do not wire Plane back into Pilot
  in this PR (that's a later cutover milestone). Pilot keeps its own
  `internal/adapters/plane` untouched.
- Keep the public API close to the original where `sdk/core` doesn't dictate a
  change, so M2–M6 can mechanically follow this template.
- If `sdk/core` is missing a type the port genuinely needs, add it to `sdk/core`
  (don't fork a local copy) and note it in the PR.
