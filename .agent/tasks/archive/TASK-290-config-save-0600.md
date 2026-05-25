# TASK-290: `config.Save()` writes 0600 (not 0644)

**Wave:** 1 (XS) · **Parallel-safe with TASK-289, TASK-291** · **Audit ref:** §2 Action #8, §3.7 P1

---

## Problem

`internal/config/config.go:496` writes `~/.pilot/config.yaml` with `0644` (world-readable). The file contains:
- GitHub PAT
- Linear API key
- Slack bot token
- Anthropic API key (if configured)

Any local user on a shared workstation can read these post-`pilot init`, post-wizard, or post-`pilot config set`. `cmd/pilot/backend.go:327` already writes 0600 for one specific path — generalize the pattern in `Save()`.

## Approach

### Step 1 — Tighten Save permissions (XS, ~10 min)

- `internal/config/config.go:496`: change `os.WriteFile(path, data, 0644)` → `os.WriteFile(path, data, 0600)`
- Same function: chmod parent dir to `0700` if it's `~/.pilot/` (skip if path is custom)

### Step 2 — Test permissions (XS, ~15 min)

- New test `TestSave_PermissionsAre0600` in `internal/config/config_test.go`:
  - Save to `t.TempDir()/test.yaml`
  - `os.Stat` the file; assert `Mode().Perm() == 0600`
  - Assert parent dir `Mode().Perm() == 0700` (if applicable)

### Step 3 — Sweep adjacent writers (XS, ~10 min)

- Search for other `os.WriteFile.*config|os.WriteFile.*\.yaml` patterns in `cmd/pilot/` and `internal/config/`
- Confirm they all use 0600; tighten any stragglers

## Files to modify

- `internal/config/config.go`
- `internal/config/config_test.go`
- Possibly other writers found in Step 3

## Test Strategy

- Unit: permission assertion as described
- Manual: `pilot config set foo bar && ls -la ~/.pilot/config.yaml` shows `-rw-------`

## Effort

XS (~35 min total). One PR.

## Out of Scope

- Encryption at rest (would be a much larger effort; environment-keyring integration)
- Migration of existing `0644` files on disk (covered by next save; document in PR description)
