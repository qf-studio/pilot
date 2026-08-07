---
name: ssm-runs-as-root-cli-reads-empty-ledger
description: AWS SSM commands execute as root on the box — pilot CLI verbs run bare read root's empty ~/.pilot and report a fresh/empty ledger; always sudo -iu ec2-user
type: pitfall
---

# SSM runs as root — bare pilot CLI verbs read an empty ledger

**What happened (2026-08-06/07 outage sessions):** diagnostic commands sent
via `aws ssm send-command` (and shells from `ssm start-session`) execute as
**root**. The pilot daemon, its config, and the SQLite ledger all live under
**ec2-user** (`~ec2-user/.pilot/`). A bare `pilot <verb>` under SSM
therefore resolves state against root's home — an empty `~/.pilot` — and
reports a fresh/empty ledger, zero executions, no queue.

**Why it bites:** the output is syntactically valid and internally
consistent — it looks exactly like a wiped or split-brain ledger (see
[[absolute-state-paths-bypass-cutover-shim]] and
`learning_stale_ledger_misdiagnosis`), inviting a catastrophic misdiagnosis
during an incident, precisely when SSM one-shots are the tool in hand. An
"empty ledger" observation from SSM is evidence about *which user ran the
command*, not about daemon state.

**How to avoid:**
1. Prefix every CLI verb in SSM commands: `sudo -iu ec2-user pilot ...`
   (`-i` so ec2-user's HOME and env resolve).
2. Prefer `~/bin/pilot-board` from the laptop for status — it already
   handles the user boundary.
3. Treat any surprising "empty state" result from an SSM command as
   suspect until `whoami`/`echo $HOME` from the same channel confirms the
   user context.
