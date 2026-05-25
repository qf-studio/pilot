# TASK-291: `getMainBranchSHA` reads `ResolvedEnv().Branch`

**Wave:** 1 (XS) · **Parallel-safe with TASK-289, TASK-290** · **Audit ref:** §2 Action #9, §3.7 P1

---

## Problem

`internal/autopilot/controller.go:1588` (`getMainBranchSHA`) hardcodes `GetBranch(ctx, owner, repo, "main")`. Repos defaulting to `develop`, `master`, or `trunk` silently fail post-merge CI monitoring; releases may fire before main-branch CI completes. The branch is already resolvable from `c.config.ResolvedEnv().Branch`, `ProjectConfig.BranchFrom`, or `DefaultBranch` — but the helper ignores all three.

## Approach

### Step 1 — Read from resolved env (XS, ~15 min)

- `internal/autopilot/controller.go:1588`: replace `"main"` literal with `c.config.ResolvedEnv().Branch`
- Fallback chain if empty: `ProjectConfig.BranchFrom` → `DefaultBranch` → `"main"` (literal as last resort, with WARN log)

### Step 2 — Table-driven test (XS, ~30 min)

- `internal/autopilot/controller_test.go`: add `TestGetMainBranchSHA_RespectsResolvedEnv` covering:
  - `Branch: "main"` → calls `GetBranch(..., "main")`
  - `Branch: "develop"` → calls `GetBranch(..., "develop")`
  - `Branch: "master"` → calls `GetBranch(..., "master")`
  - `Branch: ""` → falls through to `BranchFrom`, then `DefaultBranch`, then `"main"` + WARN
- Mock the GitHub client to record which branch was requested

### Step 3 — Manual smoke (~10 min)

- On a develop-default repo, trigger merge; observe post-merge CI fires correctly

## Files to modify

- `internal/autopilot/controller.go`
- `internal/autopilot/controller_test.go`

## Test Strategy

- Unit: table-driven test as above
- Manual: develop-default secondary repo

## Effort

XS (~55 min total). One PR.

## Out of Scope

- Releaser's similar frozen-config issue (§3.7 P1 — the `Releaser` struct freezes `release.config` at construction). That's a separate fix; defer to Wave 4+ (listed in audit Out-of-scope).
