---
name: bot answers/issues hit the WRONG repo — intake follows adapters.github.repo (hardwired), Q&A follows the active project, and the active project resets on every daemon restart
description: Two independent "wrong repo" traps in the conversational bot. (1) Issue intake (Responder.DraftIssue → comms.IssueCreator) resolves owner/repo from a SINGLE hardwired NewIssueCreator entry built from cfg.Adapters.GitHub.Repo — it ignores the active/default project. resolveRepo falls to repos[0] for any unmatched projectPath, so intake ALWAYS targets adapters.github.repo, and /switch does NOT change it. (2) Q&A retrieval (Responder.Answer) uses getActiveProjectPath(contextID); the per-context active project is in-memory and RESETS to default_project on every daemon restart. So after a restart the bot answers about default_project, and intake files on adapters.github.repo, regardless of what the user /switch'd. Observed 2026-06-26: Q&A answered studio-sdk instead of pilot; intake filed feat(gateway)/feat(api) issues on studio-sdk repeatedly. Fix for a demo: point default_project AND adapters.github.repo at the same repo (+ project_board.enabled:false for label polling). Proper fix: wire NewIssueCreator with an entry per cfg.Projects so intake follows the active project, and persist the active project across restarts.
type: pitfall
---
The bot can answer about / file issues on the **wrong repository**, via two unrelated
mechanisms — both surfaced live during the 2026-06-26 demo bring-up.

**Trap 1 — issue intake ignores the active project.**
`cmd/pilot/main.go` builds the `comms.IssueCreator` with a **single** entry:
```go
github.NewIssueCreator(client, AllowAllIssueRepos(),
    github.IssueCreatorEntry{ProjectPath: cfg.Adapters.GitHub.ProjectPath,
        Owner: ..., Repo: ...})   // all from cfg.Adapters.GitHub.Repo
```
`issueCreator.resolveRepo(projectPath)` matches `projectPath` against entries, else falls
to `repos[0]`. With one entry, **every** intake → `adapters.github.repo`, no matter the
active project. So `/switch pilot` set the Q&A project but intake still filed on
studio-sdk. Symptom: "create an issue to add a /ping endpoint" → issue on the github
adapter's repo, not the repo you were talking about.

**Trap 2 — the active project resets on restart.**
`comms.Handler.activeProject` is an in-memory `map[contextID]string`, set by `/switch`
and lost on restart. `getActiveProjectPath` falls back to `default_project`. So after any
daemon restart, Q&A answers about `default_project` until you `/switch` again — and the
bot will confidently answer about a repo that doesn't contain what you asked (e.g.
"how does intent classification work?" → studio-sdk has no intent classifier → retrieval
misses → slow executor fallback → wrong/empty answer).

**Demo/config fix (fast, reversible):** point all three at the same repo:
```yaml
default_project: pilot                       # Q&A / retrieval target
adapters.github.repo: qf-studio/pilot        # intake target + poll source
adapters.github.project_path: …/pilot
adapters.github.project_board.enabled: false # use pilot-label polling, not a board
```
Then the full loop closes on one repo: chat fast, Q&A grounded, intake → `pilot` issue →
daemon label-polls it → **executes → PR** (validated: #3705 → PR #3706 merged).
Re-verify after **every** restart (Trap 2).

**Proper fix (code):**
- Wire `NewIssueCreator` with one `IssueCreatorEntry` per `cfg.Projects` (path → owner/repo)
  so `resolveRepo(activeProjectPath)` returns the active project's repo.
- Persist the active project per context (store + reload on startup) so it survives restarts.
- Consider surfacing the active project in the dashboard TUI (data is in `comms.Handler`
  `activeProject` + `GetActiveProject`) so the operator can see which repo the bot will use.

**Board-source caveat:** if the target repo is board-driven (`project_board.source_enabled:
true`), a `pilot`-labeled issue that isn't ON the board is ignored — intake doesn't add
issues to the board, so "talk → ticket → PR" won't close there. Use label polling for the
demo repo, or wire intake → board.

Relates to [[bug_classifier_effort_param_and_taxonomy]] (mem-041) — both found during the
same live bring-up.
