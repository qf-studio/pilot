---
name: Self-test command must match the binary's real CLI contract
description: TASK-303's pre-exec smoke test invoked `pilot --version` (a flag pilot doesn't register) instead of the `version` subcommand; it exited non-zero and blocked EVERY hot upgrade. Arg-agnostic test fakes hid it. Guard with a fake that mimics the real CLI.
type: pitfall
---

## What

The pre-exec smoke test added in TASK-303 (`internal/upgrade/restart.go`) verified the new binary by running `binaryPath --version`. But pilot's root cobra command registers **no** `--version` flag — only a `version` subcommand. So:

- `pilot --version` → `Error: unknown flag: --version`, **exit 1**
- `pilot version` → `Pilot X.Y.Z`, **exit 0**

The smoke test therefore failed on **every** hot upgrade, aborting before `syscall.Exec`. Users saw the (correct, TASK-303) honest-failure panel `pre-exec smoke test failed: exit status 1` and fell back to manual reinstall. Fixed in GH-3222 / PR #3223 (v2.159.3): `"--version"` → `"version"`.

## Why the existing tests missed it

`TestRunSmokeTest` used **arg-agnostic** shell-script stand-ins (`#!/bin/sh\necho v1.0.0\nexit 0`). The fakes ignored their arguments, so they passed regardless of whether the smoke test sent `--version` or `version`. The test exercised the *timeout/exit-code plumbing* but never the *CLI contract*.

## How to apply

- **When a process self-invokes its own binary** (smoke tests, health checks, `--version` probes), the invocation MUST match what that binary actually supports. Don't assume `--version` works — most CLIs have it, cobra apps without `rootCmd.Version` set do not.
- **Test fakes must encode the contract under test.** If the thing you're verifying is "we call the right subcommand," the fake must accept the right form and reject the wrong one. The regression guard `TestRunSmokeTest_UsesVersionSubcommand` does exactly this: a fake that exits 0 on `version` and 1 on anything else. With the old `--version` code it fails; with the fix it passes.
- **General rule:** an arg-agnostic mock can only validate plumbing, never argument correctness. If argument/flag selection is the behavior being tested, the mock must branch on the argument.

## Cross-cutting note

This is the third defect in the same hot-upgrade subsystem this week, all from the same shipment:
- [[bug_hot_upgrade_silent_codesign]] — swallowed codesign error
- [[bug_hot_upgrade_restarting_ui_trap]] — misleading TUI render
- this one — wrong smoke-test CLI contract

The fix for the first two (honest failure + pre-exec smoke test) introduced this third one. **Lesson: a fix that adds a new self-verification step is itself code that needs its own contract test** — the verification can be wrong in a way that's worse than no verification (it blocked a path that previously sort-of worked).

Deployment was gated by [[pattern_hot_upgrade_bootstrap]] — the fix lives inside the path it repairs, so it required one manual reinstall (v2.159.3) before `u` could self-heal.

## Related

- [[bug_hot_upgrade_silent_codesign]]
- [[bug_hot_upgrade_restarting_ui_trap]]
- [[pattern_hot_upgrade_bootstrap]]
- Executor couldn't self-fix this one-liner (second-guessed the obvious-looking `--version`) → GH-3224
