# Pitfall: A guard copied from the periodic cleanup into StartupRecover (TASK-624 D2, PR#59

## Summary
A guard copied from the periodic cleanup into StartupRecover (TASK-624 D2, PR#5988) kept pilot-in-progress whenever the latest execution row had ANY terminal status, including stalled. dispatcher.Start stalls every crash orphan before StartupRecover runs, and the SDK poller skips issues labelled pilot-in-progress, so a run killed by a restart with no PR was never re-offered; before the change the boot strip was the only release. Fixed in TASK-625 (PR#5993) by keying the guard on Store.HasTerminalCompletion (done: completed with a deliverable, clean no_op, canceled, superseded, needs_human). Rule: 'terminal' means finished; 'done' means never re-dispatch; a label guard that stops re-offers must use done.

## Context
Discovered during development.

## Details
A guard copied from the periodic cleanup into StartupRecover (TASK-624 D2, PR#5988) kept pilot-in-progress whenever the latest execution row had ANY terminal status, including stalled. dispatcher.Start stalls every crash orphan before StartupRecover runs, and the SDK poller skips issues labelled pilot-in-progress, so a run killed by a restart with no PR was never re-offered; before the change the boot strip was the only release. Fixed in TASK-625 (PR#5993) by keying the guard on Store.HasTerminalCompletion (done: completed with a deliverable, clean no_op, canceled, superseded, needs_human). Rule: 'terminal' means finished; 'done' means never re-dispatch; a label guard that stops re-offers must use done.

## Recommended Approach
Apply this knowledge when working on related topics.

## Related
- None documented

---
**Captured**: 2026-11-01
**Confidence**: 100%
**Concepts**: restart, labels, sdk-poller, recovery, stalled, github-adapter
