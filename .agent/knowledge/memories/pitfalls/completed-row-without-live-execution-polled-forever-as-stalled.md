# Pitfall: An open pilot-labeled issue whose execution row is completed (no live run) is polled forever as 'stalled: awaiting re-arm evidence'

## Summary
When an execution row is completed with a pr_url but the issue stays open (e.g. the merged-branch short-circuit), the daemon's startup cleaner strips pilot-in-progress as stuck ('startup recover: removing stuck pilot-in-progress label'), the SDK poller then admits the issue every cycle ('processed but status labels removed, allowing retry'), and terminalCompletionChecker.HasCompletedExecutionReason falls through its three re-arm probes to recordClaimLostDrop + 'stalled: awaiting re-arm evidence' / 'repick-backoff cooldown' — every 5.5 min, no cap, no terminal transition. The reason string is wrong for a genuinely completed row; the comment at that branch says completed rows never reach it.

## Context
2026-10-01 #5551: completed 08:24:13 via short-circuit; v2.276.12 restart 09:15:57 stripped the label at 09:17:13; skip lines every ~5.5 min from 09:17 through at least 11:33Z. Box daemon.log + GitHub events.

## Details
cmd/pilot/main.go HasCompletedExecutionReason: HasTerminalCompletion true (pr_url != ''), canceled/superseded/needs_human probes all false, fallthrough arms the backoff. The stale-label cleaner (adapters/github/cleanup.go) only checks for an active execution, not for an open issue with a completed row. Related: terminal-status-without-rearm-probe-kills-operator-recovery, stalled-rearm-sweep-ignores-body-edit-loops-forever.

## Recommended Approach
In HasCompletedExecutionReason return a distinct 'completed execution exists' reason for genuine completed rows and do not feed recordClaimLostDrop; optionally have the startup cleaner close-or-comment an open issue whose newest row is completed with a pr_url that is already merged. Operator workaround: close the issue by hand; the row stays completed and the loop stops.

## Related
- `cmd/pilot/main.go`
- `internal/adapters/github/cleanup.go`
- `internal/memory/store.go`

---
**Captured**: 2026-10-01
**Confidence**: 85%
**Concepts**: poller, repick-backoff, rearm, terminal-status, github-cleanup
