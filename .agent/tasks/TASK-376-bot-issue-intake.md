# TASK-376: Bot Module Phase 4 — conversational issue intake

**Status**: 🚧 Dispatched, gated → [#3672](https://github.com/qf-studio/pilot/issues/3672) (`Blocked by: #3665`)
**Created**: 2026-06-26
**Assignee**: Pilot (queued)
**Parent plan**: `/Users/aleks.petrov/.claude/plans/there-is-a-problem-inherited-fiddle.md`
**Depends on**: TASK-374 (Responder).

---

## Context

No existing logic turns a freeform message into a structured issue. Add a
conversational intake that drafts `title`/`body`/`labels` and **creates the issue
directly + labels `pilot`** (locked decision — auto-executes). Guardrails are the
existing repo allowlist + conventional-commit title check in `CreatePilotIssue`
(`internal/adapters/github/issue_create.go:59,63`).

---

## Acceptance Criteria

- [ ] `internal/comms/issue_intake.go`: `IssueDraft{Title,Body,Labels}`, per-context intake state machine (`pendingIssues map[string]*IssueDraft` on `Handler`, mirroring `pendingTasks`), and the `IssueCreator` interface:
      `CreateIssue(ctx, projectPath string, d IssueDraft) (url string, err error)` (mirrors `MemberResolver` DI — keeps comms PM-agnostic, no executor→github cycle).
- [ ] `Responder.DraftIssue(ctx, history, msg) (IssueDraft, error)`: LLM drafts a **conventional-commit** title (`type(scope): desc`) + body; default label `pilot` when `bot.issue_intake.auto_label_pilot`.
- [ ] `internal/adapters/github/issue_creator.go`: concrete `comms.IssueCreator` resolving active project → `owner/repo` → `github.CreatePilotIssue(...)` with `pilot` label + `RepoAllowlist`.
- [ ] Triggers: new `intent.IntentIssueIntake` (NL: "create an issue to…", "file a ticket…") in `intent.go`/`classifier.go`, **and** explicit `/draft-issue <text>` command in `commands.go` (pattern: `handleNoPR`/`handleForcePR`, `:552-580`).
- [ ] `handleIssueIntake` dispatched in the `detectIntent` switch (`handler.go:213-233`); `cmd/pilot/main.go` wires the github-backed `IssueCreator` into `HandlerDeps`.

---

## Out of Scope
- Linear/Jira issue creation (GitHub only for v1).
- Human confirmation gate (decision = create directly + label pilot).

## Verify
```bash
go test ./internal/comms/... ./internal/adapters/github/...
go build ./... && make lint
```
Live: "create an issue to add rate limiting to the gateway" → bot drafts
`feat(gateway): …`, creates the GitHub issue labeled `pilot`, returns the URL;
confirm the Pilot daemon auto-picks it.

## Done
- [ ] NL + `/draft-issue` both produce a `pilot`-labeled GitHub issue with a valid conventional-commit title.
- [ ] `IssueCreator` mock test asserts `CreateIssue` called once with the `pilot` label.
- [ ] Tests + lint clean.

## Refs
- Parent plan; depends on TASK-374. Guardrails: `issue_create.go:59,63,75`.

**Last Updated**: 2026-06-26
