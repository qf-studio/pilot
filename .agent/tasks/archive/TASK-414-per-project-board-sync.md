# feat(github): per-project Projects V2 board config — unbind board sync/source from the default repo

**Created**: 2026-07-20 · **Status**: ✅ SHIPPED 2026-07-20 (PR #4473 merged, deployed; follow-ups #4474 #4475 #4478 all shipped same day)

## Problem

Pilot supports exactly ONE Projects V2 board, hard-bound to the default repo
(`adapters.github.repo`). `cmd/pilot/poller_github.go:303` gates all board
wiring behind `target.isDefault` ("Per-project board sync is a 4d.6+ question;
project repos get no board wiring"). The autopilot side (`cmd/pilot/main.go`
~663–681 and ~1620–1628) builds a single `ProjectBoardSync` from the global
adapter config the same way.

Real-world breakage this caused (2026-07-20 investigation):

- Board qf-studio/#1 "Studio SDK" moved cards while `studio-sdk` was the
  default repo (laptop era), then froze when the default flipped to
  `qf-studio/pilot` — item "M7 cutover" stuck in Todo since.
- Board qf-studio/#2 "Pointer" is configured globally (`project_number: 2`)
  but the only board-capable poller is the pilot repo, whose issues aren't on
  that board → every sync attempt no-ops or targets the wrong board.
- Two products with two boards cannot both work. Ever.

## Solution

Per-project `project_board` config, with the existing adapter-global block
kept as the default-repo fallback (zero migration break).

```yaml
adapters:
  github:
    repo: qf-studio/pilot
    project_board: { ... }          # fallback — applies to default repo only, as today
projects:
  studio-sdk:
    github:
      owner: qf-studio
      repo: studio-sdk
      project_board:                # NEW — overrides/adds per project
        enabled: true
        project_number: 1
        status_field: Status
        statuses: { in_progress: In Progress, review: In Review, done: Done, failed: Blocked }
  pointer:
    github:
      owner: qf-studio
      repo: pointer
      project_board:
        enabled: true
        project_number: 2
        source_enabled: true        # board-driven: Todo column = queue
        source_status: Todo
        statuses: { in_progress: In Progress, review: In Review, done: Done, failed: Blocked }
```

## Implementation

1. **Config** — `internal/config/config.go`: add
   `ProjectBoard *github.ProjectBoardConfig \`yaml:"project_board,omitempty"\``
   to `ProjectGitHubConfig` (reuses the existing struct from
   `internal/adapters/github/types.go`; no new types).
2. **Poller wiring** — `cmd/pilot/poller_github.go` (~line 303): replace the
   `if target.isDefault` gate with per-target resolution: use the target
   project's `GitHub.ProjectBoard` if set; else if `target.isDefault` fall
   back to `ghCfg.ProjectBoard`; else no board. Reuse
   `toSDKProjectBoardConfig` (`cmd/pilot/github_sdk_bridge.go`) instead of the
   inline literal.
3. **Autopilot wiring** — `cmd/pilot/main.go`: both board-sync construction
   sites (~663–681 gateway path, ~1620–1628 autopilot path) currently build
   one `ProjectBoardSync` from `cfg.Adapters.GitHub.ProjectBoard`. Extend to
   resolve per project repo with the same precedence (project override →
   global fallback for the default repo), so PR-open/merge/fail transitions
   move cards on the correct board per repo.
4. The `board_wired` log field (`poller_github.go:482`) must reflect the
   per-target result — it is the operator's verification signal.

## Tests (table-driven, per Go standards)

- Config: YAML with per-project `project_board` parses; absent → nil.
- Resolution precedence: project-level set / unset × default / non-default
  target → expected board config (4 cases minimum).
- Poller wiring: non-default target with project board config gets
  `sdkCfg.ProjectBoard != nil`; non-default without stays nil; default keeps
  global fallback.

## Acceptance criteria

- A non-default project with `project_board` gets board sync AND (when
  `source_enabled`) board sourcing.
- Default repo behavior with only the global block is byte-identical to today.
- Projects without any board config get none (no GraphQL calls).
- `make test && make lint` green.

## Non-goals

- No board auto-add (sync still only updates cards already on the board).
- No config changes for pointer-resources (operator wires configs post-merge).
- No changes to studio-sdk (the SDK already supports arbitrary boards per
  `ProjectBoardConfig`; this is host-side wiring only).

## Refs

- Dispatched: https://github.com/qf-studio/pilot/issues/4472

- Evidence thread: boards qf-studio/#1 (frozen mid-M7) and #2 (never moved).
- Related: token scope fix 2026-07-20 (gh CLI OAuth token lacked `project`
  scope; fixed via `gh auth refresh` on the box + wrapper reads
  `$(gh auth token)`).
