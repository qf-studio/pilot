---
status: dispatched
priority: P3
created: 2026-05-19
execution: dispatched
github_issue: 3017
labels: [cli, ux, onboarding, github]
---

# TASK-282: Interactive `pilot project add` wizard with `gh` CLI integration

**Status**: ✅ Completed (PR #3612 merged 2026-06-18)
**Priority**: P3 — quality-of-life
**Created**: 2026-05-19
**GitHub Issue**: https://github.com/qf-studio/pilot/issues/3017

---

## Context

**Problem**:
`pilot project add` (`cmd/pilot/project.go:94`) is purely flag-driven. With no flags it errors on missing `--name`. The only "intelligence" is git-remote auto-detection (owner/repo, default branch, `.agent/` presence). It never:
- Prompts the user
- Reads `gh auth` state
- Lists the user's GitHub repos
- Seeds `adapters.github.token` from `gh auth token`

The `gh` CLI is only consulted in `internal/health/health.go:594` as a fallback diagnostic when `github.token` is missing from config. The GitHub adapter (`internal/adapters/github/`) is PAT-based against `cfg.Adapters.GitHub.Token`.

Result: a new user wanting to wire Pilot to GitHub has to:
1. Know to run `pilot project add` with the right flags (or rely on git-remote auto-detect)
2. Separately hand-edit `~/.pilot/config.yaml` OR run `pilot setup` to enter a token
3. Discover those two steps are independent

**Goal**:
Single command — `pilot project add` with no flags — that walks a `gh`-authenticated user from zero to a runnable project entry in `~/.pilot/config.yaml`.

**Success Criteria**:
- [ ] `pilot project add` (no flags) enters wizard mode
- [ ] Wizard detects `gh auth status`; if OK and no token in config, offers to seed `adapters.github.token` from `gh auth token`
- [ ] Wizard presents a repo picker from `gh repo list --limit 50 --json nameWithOwner,description,isPrivate`
- [ ] Wizard prefills `name` from repo name, `path` from CWD (with override)
- [ ] Wizard offers to set as default project
- [ ] If `gh` is missing or unauthenticated, wizard falls back gracefully to the current flag-driven flow (`--name` required)
- [ ] Existing flag-driven invocation continues to work unchanged (no breaking change)

---

## Proposed UX

```
$ pilot project add

✓ Detected gh auth (user: alex-petrov, scopes: repo, workflow)
✓ No GitHub token configured in ~/.pilot/config.yaml

? Use your gh CLI token for Pilot? (Y/n)  ▸ Y
✓ Wrote adapters.github.token

? Pick a repo:
  ▸ qf-studio/pilot           (private) — AI that ships your tickets
    qf-studio/navigator       (public)  — Plan-execute pipeline
    alex-petrov/dotfiles      (public)
    alex-petrov/scratch       (private)
  ↓ 46 more — type to filter

? Project name:  [pilot]                 ▸ ⏎
? Local path:    [/Users/.../pilot]      ▸ ⏎
? Set as default project? (Y/n)          ▸ Y

✓ Project added: pilot
   Path:      /Users/alex/Projects/startups/pilot
   GitHub:    qf-studio/pilot
   Branch:    main (detected)
   Navigator: enabled
   Default:   yes

→ Start working:  pilot start --project pilot --github
```

Fallback when `gh` missing:
```
$ pilot project add
gh CLI not found or unauthenticated. Falling back to flag mode.
Use: pilot project add --name <name> [--github owner/repo] [--path <dir>]
Hint: install gh (https://cli.github.com) for the interactive picker.
```

---

## Implementation Plan

### Phase 1: Wizard scaffolding
**Goal**: Make `pilot project add` with no args invoke wizard; preserve flag mode.

**Tasks**:
- [ ] In `newProjectAddCmd` (`cmd/pilot/project.go:94`), detect "no flags + interactive TTY" → call new `runProjectAddWizard()`
- [ ] Move flag-driven logic out of `RunE` into `runProjectAddFlags()` for clean separation
- [ ] New file `cmd/pilot/project_wizard.go` for the interactive flow

**Files**:
- `cmd/pilot/project.go` — branch in RunE, extract flag logic
- `cmd/pilot/project_wizard.go` (new) — wizard implementation

### Phase 2: `gh` integration
**Goal**: Detect gh, seed token, list repos.

**Tasks**:
- [ ] Helper `ghAuthStatus()` — runs `gh auth status --json` (or parses text fallback)
- [ ] Helper `ghAuthToken()` — runs `gh auth token`
- [ ] Helper `ghRepoList(limit int)` — runs `gh repo list --limit N --json nameWithOwner,description,isPrivate,defaultBranchRef`
- [ ] Reuse picker UI from existing wizards if possible (`cmd/pilot/onboard_project.go`, `cmd/pilot/interactive.go`)

**Files**:
- `cmd/pilot/project_wizard.go` — gh helpers + picker
- Possibly `internal/github/ghcli.go` (new) — if helpers should be shared with health/diagnostic code

### Phase 3: Polish & fallback
**Goal**: Graceful degradation, dry-run, tests.

**Tasks**:
- [ ] Fallback path when gh missing/unauthenticated
- [ ] `--no-wizard` flag for CI/scripting (force flag-driven mode)
- [ ] Confirm-before-write step (show config diff)
- [ ] Tests: wizard with mocked gh, fallback, duplicate-name, duplicate-path

**Files**:
- `cmd/pilot/project_wizard.go`
- `cmd/pilot/project_wizard_test.go` (new)

---

## Technical Decisions

| Decision | Options | Tentative | Reasoning |
|----------|---------|-----------|-----------|
| Picker UI library | `huh`, `promptui`, custom bubbletea | **bubbletea** (already a dep) | `huh` is NOT in go.mod (verified 2026-06-17); avoid adding a new dep — reuse the existing bubbletea-based picker from `cmd/pilot/onboard_project.go` / `interactive.go`. Adding `huh` is acceptable only if explicitly approved. |
| gh invocation | shell out to `gh` CLI, use go-github with token | shell out | Avoids dep; gh CLI is the auth source-of-truth |
| Token persistence | write to config, env var | write to `adapters.github.token` in config | Matches existing setup wizard behavior |
| Fallback when no gh | Error, fall back to flag mode | Fall back with hint | Zero-friction degradation |
| Share with `onboard`? | Duplicate logic, extract shared helper | Extract shared helper | DRY; both wizards need same gh detection |

---

## Files likely touched

- `cmd/pilot/project.go` — branch in `newProjectAddCmd` RunE; extract `runProjectAddFlags`
- `cmd/pilot/project_wizard.go` (new) — wizard implementation
- `cmd/pilot/project_wizard_test.go` (new) — tests with mocked gh
- `cmd/pilot/onboard_project.go` — optionally call shared helpers
- `internal/github/ghcli.go` (maybe new) — shared gh-CLI helpers if reused by health

---

## Out of Scope

- GitHub App auth path (PAT only for v1)
- Replacing the flag-driven path (both must coexist)
- Multi-repo bulk-add (`pilot project add --all-gh`) — could be Phase 4
- Removing the gh-auth fallback in `internal/health/health.go:594`

---

## Dependencies

**Requires**:
- `gh` CLI installed on user's machine (runtime requirement, not build)
- Existing config schema (no migration needed)

**Blocks**:
- None — pure UX improvement

---

## Verify (when executed)

```bash
make build
./bin/pilot project add                          # wizard mode
./bin/pilot project add --name x --github a/b   # flag mode still works
./bin/pilot project add --no-wizard              # explicit flag mode
go test ./cmd/pilot/... -run TestProjectAddWizard
```

---

## Done (when executed)

- [ ] `pilot project add` with no flags enters wizard on a TTY with gh auth
- [ ] Wizard writes valid `ProjectConfig` to `~/.pilot/config.yaml`
- [ ] Token seeding works and is opt-in
- [ ] Fallback path works on machine without `gh`
- [ ] All existing `project add` tests still pass
- [ ] New wizard tests pass with mocked gh

---

## Notes

- Discovered while a user asked "how do I link `pilot project add` to gh?" — current answer is "you don't, you hand-edit config or run `pilot setup` separately". That's a clear UX gap.
- Related existing wizards: `pilot setup` (`cmd/pilot/setup.go:26`), `pilot onboard` (`cmd/pilot/onboard.go:56`). Neither uses gh. Could extend onboarding too in a follow-up.
- `gh` is consulted only at `internal/health/health.go:594` today.
- Do NOT create a GitHub issue with the `pilot` label for this — user explicitly wants backlog-only.

---

**Last Updated**: 2026-06-17 (dispatched to Pilot via #3017)
