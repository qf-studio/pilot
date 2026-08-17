# TASK-481: Three independent defects surfaced by the lkshrk PR review

**Status**: 📋 Planned — NOT dispatched. Research complete 2026-08-17. Three independent legs, dispatchable separately.
**Created**: 2026-08-17
**Origin**: review of external contributor PRs #4896 / #4899 / #4903 (lkshrk) — each PR fixes part of a defect and leaves an adjacent part unfixed. Leg C turned out substantially larger than the PR's own note suggested.

---

## Leg A — Linear `GetLabelByName` fails for a UUID-configured `team_id`

**Defect.** `GetLabelByName` filters `team: { key: { eq: $teamId } }` unconditionally (`internal/adapters/linear/client.go:416`). Linear's `key` is the short slug (`"ROU"`), not the UUID. Config documents `team_id` as accepting either form (`internal/config/config.go:61,96` — "Team ID or name"). With a UUID configured, lookup never matches, `GetOrCreateLabel` falls through to `CreateLabel` on every poller startup, and Linear rejects the duplicate name.

**Reproduction path** (confirmed): `internal/adapters/linear/poller.go:187,195,200,205` — `cacheLabelIDs` passes `p.config.TeamID` straight into both `GetLabelByName` and `GetOrCreateLabel`. (Contrast `CreateIssue`, which resolves through `parent.Team.Key` — always a key, never hits this.)

**Relationship to PR #4896**: that PR fixes only the *write* half (`ResolveTeamUUID` before `issueLabelCreate.teamId`) and adds the `looksLikeUUID`/`ResolveTeamUUID` helpers this leg reuses, plus a workspace-label fallback at `client.go:435-442`. The read filter is untouched — this is the unfixed half of issue #4884.

**Fix.** Branch the GraphQL filter on `looksLikeUUID(teamID)`: UUID ⇒ `team: { id: { eq: $teamId } }`, else keep `team: { key: { eq: $teamId } }`. Two named queries sharing the response struct is cleaner than string-templating one query. Reuse #4896's helpers as-is. The `len(nodes)==0` → `getWorkspaceLabelByName` fallback stays as #4896 leaves it.

**Files**: `internal/adapters/linear/client.go` (`GetLabelByName`, 412-442) · `internal/adapters/linear/client_test.go`.

**Test rows** (httptest mock, following #4896's `TestGetLabelByNameWorkspaceFallback`):

| Case | teamID | Expected filter sent | Expected |
|---|---|---|---|
| Key-configured, label exists | `"ROU"` | `team.key.eq` | returns ID, no fallback |
| UUID-configured, label exists | UUID | `team.id.eq` | returns ID, no fallback |
| UUID-configured, only workspace-scoped label | UUID | id-filter then workspace fallback | returns workspace label ID |
| Key-configured, missing everywhere | `"ROU"` | key-filter then fallback | `label %q not found in team %s` |
| `GetOrCreateLabel` idempotency, UUID team | UUID, label exists | id-filter | **`CreateLabel` never called** (the #4884 duplicate-create guard) |

**Dependency**: rebase onto #4896 (adjacent lines in the same function). If #4896 hasn't merged, implement both in one PR but keep the diffs logically separable.

---

## Leg B — auto_merger squash-prefix stripping misses compound/non-GH prefixes

**Defect.** `AutoMerger.MergePR` strips only a literal `fmt.Sprintf("GH-%d: ", prState.IssueNumber)` (`internal/autopilot/auto_merger.go:79-90`). For Linear compound (`LIN-ROU-586: `) or plain Jira-style (`APP-123: `) prefixes the `TrimPrefix` is a no-op, so the identifier leaks into the squash subject and defeats `parseBumpFromMessage()`'s conventional-commit detection — the exact motivation stated in the code's own comment at :80-81.

`internal/executor/title.go:25-26` already *claims* parity: "The downstream squash-merge path strips the same prefix (see internal/autopilot/auto_merger.go)". It doesn't.

**Fix.** Export `StripIssuePrefix(title string) string` from `internal/executor/title.go` (place it right after `issuePrefixRegex` at :27 so regex and accessor stay co-located), then call it from `auto_merger.go:82-85`, dropping the `IssueNumber > 0` guard — the regex only strips on an actual match, so it's safe unconditionally and now also covers adapters where `IssueNumber` is unset. No import cycle: `internal/executor` does not import `internal/autopilot` (verified).

**Files**: `internal/executor/title.go` · `internal/autopilot/auto_merger.go` (+import) · `internal/executor/title_test.go` · `internal/autopilot/auto_merger_test.go`.

**Test rows**:

| PR title | IssueNumber | Expected stripped title |
|---|---|---|
| `GH-4909: fix(autopilot): guard merge` | 4909 | `fix(autopilot): guard merge` |
| `APP-123: feat(auth): oauth flow` | 123 | `feat(auth): oauth flow` |
| `LIN-ROU-586: fix(core): widen window` | 0 | `fix(core): widen window` |
| `LIN-MY1-TEAM-42: fix(core): resolve` | 0 | `fix(core): resolve` |
| `fix(core): direct commit` | 0 | unchanged |
| `GH-123: ` | 123 | `""` — verify the `(#N)` suffix append doesn't panic |

**Dependency**: composes with #4899 (widens the shared regex; `StripIssuePrefix` inherits it for free). Either order — #4899 doesn't touch `auto_merger.go`, this doesn't touch `epic.go`.

---

## Leg C — signal blocks leak to Slack and Discord users today (four copies, three behaviors)

**Defect — larger than PR #4903's own note.** There are four independent copies of "strip internal signal markers," and slack/discord's are not merely stale: **their `FormatTaskResult` never calls them at all**. `CleanInternalSignals` is unreachable dead code in both adapters, so every `EXIT_SIGNAL` / `NAVIGATOR_STATUS` / fenced `pilot-signal` block in task output reaches Slack and Discord users unconditionally, today.

| Copy | Location | Handles | Fenced blocks | Bare `{"v":…}` | Called from `FormatTaskResult`? |
|---|---|---|---|---|---|
| comms (canonical) | `internal/comms/util.go:9-70` | full list + `NAVIGATOR_STATUS` block-skip | after #4903 ✓ | after #4903 ✓ | n/a (library) |
| telegram (dupe) | `adapters/telegram/formatter.go:330+`, called :133,:197,:243 | same list, independently duplicated constants :11-18 | after #4903 ✓ | after #4903 ✓ | yes |
| slack | `adapters/slack/formatter.go:302-318` | only 4 literal `[…]` markers via `ReplaceAll` | no | no | **no** — dead code, zero call sites |
| discord | `adapters/discord/formatter.go:135-143` | a *different* scheme: `<!-- INTERNAL: … -->`, no producer anywhere in the repo (vestigial) | no | no | **no** — only `handler_test.go:781` |

**Fix — consolidate, don't patch four copies.**

1. `internal/comms/util.go` is the home. All three adapters already import `internal/comms` for `Handler`/`IncomingMessage`/`Messenger` — zero new import edges, no cycle risk.
2. Delete `slack/formatter.go:302-318` and `discord/formatter.go:135-143` outright (unreachable ⇒ deletion is safe and removes the drift surface).
3. Delete telegram's unexported copy (constants :11-18, function :330+); its three call sites become `comms.CleanInternalSignals(...)`.
4. **Wire the call in** at `slack/formatter.go:53` (`FormatTaskResult`) and `discord/formatter.go:30`, before truncation/write. This is **new production behavior**, not a refactor — flag it explicitly in the PR description.

**Files**: `adapters/telegram/formatter.go` (+ its `formatter_test.go`) · `adapters/slack/formatter.go` (+import) · `adapters/discord/formatter.go` (+import) · `adapters/discord/handler_test.go:779-` (repoint or remove `TestCleanInternalSignals`).

**Test rows**:

| Adapter | Input to `FormatTaskResult` | Expected |
|---|---|---|
| slack | `"done\n[EXIT_SIGNAL]\nreal output"` | contains `real output`, not `[EXIT_SIGNAL]` |
| slack | fenced ```` ```pilot-signal\n{"v":2,…}\n``` ```` | neither `pilot-signal` nor the JSON payload |
| discord | `"done\nNAVIGATOR_STATUS\n━━━\nreal output"` | no `NAVIGATOR_STATUS` |
| discord | `"checked issue\n{\"v\":2,\"type\":\"status\"}\nnothing to do"` | bare JSON line stripped |
| telegram | existing `formatSuccessResult`/`formatFailureResult` tests | identical output post-redirect |
| build | old copies removed | `go build ./...` clean |

**Dependency**: **must land after #4903** (or rebased onto it) — #4903 is what makes `comms.CleanInternalSignals` the version worth centralizing on. Landing first would point everyone at the pre-#4903 implementation and require a second pass. Risk note: any existing slack/discord golden-output fixture containing signal-shaped substrings will need updating.

---

## Follow-up leads (not investigated, filed here so they aren't lost)

- `ListIssues` (`linear/client.go:271-297`) and `GetTeamDoneStateID` (`:587-635`) both filter `team: { key: { eq } }` — likely the same key-vs-UUID class as Leg A.
- `internal/briefs/formatter_slack.go` / `formatter_email.go` were not inspected — possibly a fifth signal-stripping copy.
- Discord's `<!-- INTERNAL: -->` marker has zero producers in the codebase; grep suggests vestigial, not exhaustively confirmed against SDK/executor prompt templates.
