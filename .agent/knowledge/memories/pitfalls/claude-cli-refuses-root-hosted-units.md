---
name: claude-cli-refuses-root-hosted-units
description: The claude CLI refuses --dangerously-skip-permissions when running as root, so a systemd unit with no User= silently cannot execute anything; the same default also puts the ledger at /.pilot, off the data volume
type: pitfall
---

# claude CLI refuses root — hosted units need a real user

**What happened (2026-07-24):** the hosted canary tenant's systemd unit ran
as **root** (no `User=`). Two failures, one root cause:

1. `claude --dangerously-skip-permissions` **refuses to run as root** — the
   executor could never start a session, so every dispatch died.
2. `HOME=/root` (or unset) put the ledger at **`/.pilot`** — on the root
   volume, not the provisioned data volume. State written where nothing
   backs it up and disk pressure is real.

Hot-fix applied live on the instance: `useradd pilot` + a systemd drop-in
setting `User=`/`HOME=`, state relocated to
`/var/lib/pilot/home/.pilot`, and `ExecStartPre=+` (the `+` prefix keeps the
secrets-fetch step privileged while the main process drops down).
Durable fix dispatched as **pilot-console#53**.

## Mechanism

- Running as root is the *default* for a naively-written unit — you have to
  opt in to a user. Nothing in the unit fails at install time; it fails at
  first execution, inside the executor, as an opaque session error.
- Every path Pilot derives from `HOME` (ledger, `.claude` credentials,
  caches) silently relocates with the user. Getting `User=` right and `HOME=`
  wrong yields a working daemon with an invisible, unbacked-up ledger.
- `ExecStartPre` inherits `User=` unless prefixed with `+`. Dropping to
  `pilot` breaks a secrets fetch that needs instance-role/IMDS access, so the
  naive fix trades one failure for another.

## How to avoid

1. Any hosted Pilot unit: `User=pilot`, explicit `HOME=` on the data volume,
   `ExecStartPre=+` for privileged pre-steps. Never rely on defaults.
2. Verify state location after any user change — confirm the **open DB file
   descriptor**, not the config (the TASK-409 split-brain lesson: config and
   the actually-open DB can disagree).
3. "Executor can't start a session" on a fresh host: check the effective UID
   before debugging Claude/API/network. Root is the first suspect.

Related: [[absolute-state-paths-bypass-cutover-shim]] (state landing off the
intended volume), [[ready-gate-couples-credential-validity]] and
[[oauth-ssm-params-rot-live-credentials-source-of-truth]] (same-day hosted
cascade).
