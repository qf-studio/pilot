# TASK-34: Initialize Releaser from `resolvedRelease()` (Gap 1)

**Status**: 🚧 In Progress
**Created**: 2026-05-06
**Assignee**: Pilot

---

## Context

**Problem**:
At `internal/autopilot/controller.go:197-199`, the `Releaser` is constructed from the global `cfg.Release` only:

```go
if cfg.Release != nil && cfg.Release.Enabled {
    c.releaser = NewReleaser(ghClient, owner, repo, cfg.Release)
}
```

But at runtime, `resolvedRelease()` (line 1547-1551) prefers `env.Release` over global. If a user ever sets release config ONLY under `environments.<env>.release` and removes the global `release:` block, `shouldTriggerRelease()` returns `true` (resolves env config) but `c.releaser == nil` (initialized from absent global). `handleReleasing` then returns early at line 1524 ("releaser not configured, skipping release") and the release never fires — silently.

Today, `~/.pilot/config.yaml` has both global and env-scoped release blocks with identical content, so the latent bug is masked.

**Goal**:
Initialize `c.releaser` from `resolvedRelease()` so the construction path matches the runtime decision path.

**Success Criteria**:
- [ ] `c.releaser` is non-nil when EITHER global `cfg.Release` or `env.Release` is enabled
- [ ] If both are set, the env-scoped one wins (matches `resolvedRelease()` semantics)
- [ ] No regression in existing tests
- [ ] New test case covering the env-only-release scenario

---

## Implementation Plan

### Phase 1: Refactor releaser initialization
**Tasks**:
- [ ] At `controller.go:197`, replace the direct `cfg.Release` check with a call that mirrors `resolvedRelease()`. Construction is in `NewController`, before the Controller struct is populated, so a small inline helper or duplicating the resolve logic is fine.
- [ ] Ensure resolution uses `cfg.ResolvedEnv()` to get the env block, falls back to global.

**Files**:
- `internal/autopilot/controller.go` — modify NewController

### Phase 2: Test
**Tasks**:
- [ ] Add table-driven test in `controller_test.go` covering: global only, env only, both set, neither set.

**Files**:
- `internal/autopilot/controller_test.go`

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Resolution helper | Inline duplicate / extract package func / call cfg method | Extract or reuse cfg-level helper if one exists | DRY; resolvedRelease is currently a Controller method using c.config — refactor to a Config method or a free function so it works pre-Controller |

---

## Verify

```bash
make test ./internal/autopilot/...
make lint
```

---

## Done

- [ ] `Releaser` initialized from resolved (env || global) release config
- [ ] Tests cover all 4 combinations
- [ ] No regression
EOF
