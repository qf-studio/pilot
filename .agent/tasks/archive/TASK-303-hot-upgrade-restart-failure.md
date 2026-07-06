> **SALVAGED 2026-07-06** from `backup/local-main-2026-05-27` (never landed on main; status frozen as of 2026-05-26 Wave-5 planning).

# TASK-303 — Hot-upgrade "u" key produces silent failure on macOS

**Status:** open
**Created:** 2026-05-26
**Type:** bug + UX
**Priority:** high (every release rollout is affected)
**Affects:** macOS users running `pilot start` with dashboard mode
**Related:** [[bug_hot_upgrade_silent_codesign]], [[bug_hot_upgrade_restarting_ui_trap]]

---

## Symptom

User presses `u` in the dashboard to apply a newly-detected release. The TUI shows:

1. `Installing v2.x.x... [████████] 100%` (UpgradeStateInProgress)
2. Then "restart" wording appears somewhere
3. But the running process keeps reporting the **previous** version
4. Only a manual `Ctrl+C` + `pilot start` picks up the new binary

The new binary **is on disk** (manual relaunch loads it cleanly) — the in-place hot restart is what fails.

---

## Root cause analysis

Full chain traced end-to-end in research session (2026-05-26). Three defects compound:

### Defect 1 — `PrepareForExecution` error is swallowed

`internal/upgrade/upgrade.go:405-407`:
```go
// Prepare binary for execution (removes quarantine, signs on macOS)
// Errors are non-fatal - binary may still work
_ = PrepareForExecution(u.binaryPath)
```

On macOS, `PrepareForExecution` runs `codesign -s -` (ad-hoc sign) and returns its error (`internal/upgrade/codesign_darwin.go:16`). When that error is discarded, the freshly-installed binary may be unsigned. Gatekeeper then blocks `syscall.Exec` from loading it.

### Defect 2 — `UpgradeStateInProgress` never displays `upgradeMessage`

`internal/dashboard/tui.go:2744-2747` renders `"Installing %s... %s %d%%"` and ignores `m.upgradeMessage`. The hot-upgrade flow updates `upgradeMessage` to `"Restarting..."` at 100% (`internal/upgrade/hot.go:139`), but the user never sees that text.

### Defect 3 — `UpgradeStateComplete` text claims a restart that didn't happen

`internal/dashboard/tui.go:2749-2751` renders `"Now running %s - Restarting..."`. On Unix this state should be unreachable (successful `syscall.Exec` would have replaced the process). When the state IS reached, the message lies — the user sees "Now running v2.x.x" but the process is still old. Whether the state is reachable on Unix today is itself a question (see "Verification" below).

---

## Goals

1. **Surface the real failure** — codesign errors must propagate so the user / logs see them
2. **Fix the UI** — the dashboard must accurately reflect whether exec succeeded, failed, or was skipped
3. **Pre-flight the new binary** — verify it can actually run before committing to `syscall.Exec`
4. **No regression in Windows path** — Windows correctly returns nil + manual-restart message; preserve that

---

## Acceptance criteria

- [ ] `PrepareForExecution` returns its error and the caller treats codesign failure as fatal on Darwin
- [ ] When the upgrade fails for ANY reason, the TUI shows a clearly-labeled failure pane with the error message — not a "Restarting..." or "Installing... 100%" frozen state
- [ ] A new pre-exec smoke test (`exec.Command(binaryPath, "--version").Run()` or equivalent) runs immediately before `syscall.Exec` and fails the upgrade cleanly if the new binary cannot start
- [ ] On macOS, attempting to hot-upgrade an unsigned binary produces a specific error string mentioning Gatekeeper / codesign — not a generic "restart failed"
- [ ] On Windows, the user still sees "Upgrade installed — restart manually" (no behavioral regression)
- [ ] Unit tests cover: codesign error propagation, smoke-test failure, TUI state transitions for all four `UpgradeState*` values
- [ ] Manual smoke test on macOS: deliberately corrupt a release artifact's signature, press `u`, observe a clear failure message

---

## Out of scope (split into follow-ups if needed)

- Telemetry / metrics for upgrade success rate (separate task)
- Removing the dependency on the system `codesign` binary entirely (would require Go-native Mach-O signing — large effort)
- Notarization workflow for releases (operational, not code)

---

## Implementation plan

### Phase 1 — Surface the codesign error (1-2h, smallest blast radius)

**Files:**
- `internal/upgrade/upgrade.go:407` — replace `_ =` with explicit error handling
- `internal/upgrade/codesign_darwin.go` — return a wrapped error that mentions the binary path
- `internal/upgrade/codesign_other.go` — keep no-op stub returning nil (verify file exists)

**Changes:**
```go
// upgrade.go:407 (after install)
if err := PrepareForExecution(u.binaryPath); err != nil {
    return fmt.Errorf("post-install preparation failed (binary may be blocked by Gatekeeper): %w", err)
}
```

**Tests:**
- `internal/upgrade/upgrade_test.go` — add test that injects a failing `PrepareForExecution` (via interface or build-tag injection) and asserts the upgrade returns the wrapped error

### Phase 2 — Pre-exec smoke test (2-3h)

**Files:**
- `internal/upgrade/restart.go` — add a `verifyExecutable(binaryPath string) error` helper called before `syscall.Exec`

**Changes:**
```go
// restart.go:35 (before stdout/stderr sync)
if err := verifyExecutable(binaryPath); err != nil {
    return fmt.Errorf("new binary failed pre-exec verification: %w", err)
}

func verifyExecutable(binaryPath string) error {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    out, err := exec.CommandContext(ctx, binaryPath, "--version").CombinedOutput()
    if err != nil {
        return fmt.Errorf("--version failed (%w): %s", err, string(out))
    }
    return nil
}
```

**Tests:**
- `internal/upgrade/restart_test.go` — table tests for: binary missing, binary not executable, binary crashes on `--version`, binary succeeds

### Phase 3 — TUI failure rendering (2-3h)

**Files:**
- `internal/dashboard/tui.go:2735-2755` — rework the upgrade panel switch

**Changes:**

1. `UpgradeStateInProgress` should append `m.upgradeMessage` when non-empty:
```go
case UpgradeStateInProgress:
    bar := m.renderProgressBar(m.upgradeProgress, 30)
    content.WriteString(fmt.Sprintf("  Installing %s... %s %d%%", m.updateInfo.LatestVersion, bar, m.upgradeProgress))
    if m.upgradeMessage != "" {
        content.WriteString(fmt.Sprintf("\n  %s", m.upgradeMessage))
    }
```

2. `UpgradeStateComplete` should match the *actual* semantics — on success exec already replaced the process, so this state in practice means "install done, restart required" (matches Windows). Reword to:
```go
case UpgradeStateComplete:
    content.WriteString(fmt.Sprintf("  Upgrade to %s installed — restart Pilot manually to apply.", m.updateInfo.LatestVersion))
```

3. `UpgradeStateFailed` rendering already exists at 2753-2755; verify it visually dominates the panel and isn't easily missed. Consider a red/yellow style if `lipgloss` is used.

**Tests:**
- `internal/dashboard/tui_test.go` — render-snapshot tests for each upgrade state

### Phase 4 — Documentation + memory update (30 min)

- Update `.agent/knowledge/memories/patterns/pattern_hot_upgrade_bootstrap.md` if behavior changes
- Add to `.agent/knowledge/graph.json` (use `nav-graph` skill to add nodes for the two new pitfalls + this task)
- Verify CLAUDE.md mentions nothing stale about hot upgrade

---

## Verification steps (manual, post-implementation)

1. **Happy path:**
   - Build local binary, tag a fake release, run `pilot start`, press `u`
   - Expect: process replaces in-place, version banner updates to new tag

2. **Codesign failure repro (macOS):**
   - Stub `PrepareForExecution` to return an error (via test build tag)
   - Press `u` — expect TUI shows "post-install preparation failed (binary may be blocked by Gatekeeper): ..."

3. **Corrupted binary repro:**
   - Stub the download to write `#!/bin/false` instead of a valid binary
   - Press `u` — expect TUI shows "new binary failed pre-exec verification: ..."
   - Critically: old process must still be running with old version, user can dismiss and retry

4. **Windows regression check:**
   - On Windows VM, press `u`
   - Expect: "Upgrade to vX.Y.Z installed — restart Pilot manually to apply."

---

## Risk / blast radius

- **Low-to-medium.** Changes are localized to `internal/upgrade/*.go` and `internal/dashboard/tui.go`. No DB schema changes, no API changes. Worst case: a new bug in the upgrade path makes upgrades fail more loudly — but the current symptom is "fails silently with confusing UX," so loud failure is itself an improvement.
- The pre-exec smoke test adds ~50-200ms to upgrade time (one `--version` invocation). Acceptable.
- Tests must cover the build-tag matrix (`darwin`, `linux`, `windows`) for `codesign_*.go` and `restart*.go`.

---

## Effort estimate

- Phase 1: 1-2h
- Phase 2: 2-3h
- Phase 3: 2-3h
- Phase 4: 30 min
- **Total: ~6-9h**

Reasonable to hand off as a single Pilot issue.

---

## Handoff template (GitHub issue body)

```
Hot-upgrade ("u" key) silently fails on macOS — new binary lands on disk but `syscall.Exec` is blocked by Gatekeeper, and the TUI doesn't surface the real failure. Full RCA in .agent/tasks/TASK-303-hot-upgrade-restart-failure.md.

Fix in three phases (single PR):
1. Stop swallowing PrepareForExecution errors (internal/upgrade/upgrade.go:407)
2. Add pre-exec smoke test in RestartWithNewBinary (internal/upgrade/restart.go)
3. Make TUI upgrade panel render upgradeMessage in-progress and reword Complete state (internal/dashboard/tui.go:2735-2755)

Acceptance criteria + verification steps in the task file. Preserve Windows manual-restart behavior.
```
