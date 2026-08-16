---
name: apply-script-silent-rollback-causes
description: config-push rollbacks are silent and causeless in the console — the real error lives only in the box's SSM invocation output. Two proven causes: root-run git vs pilot-owned clones (dubious ownership → "not in a git directory") and boot-time-stale /run/pilot/env (post-boot secret seed/rotation invisible until restart). Diagnose from SSM invocation output, never from the rollback event.
type: pitfall
---

# Config-push rollbacks are causeless in the console — forensics live in SSM invocation output

**What happened (2026-08-16, TASK-405 un-patched re-run):** gen-3 config
push rolled back on every retry with only `config.push_rolled_back` in the
journal. Three different-looking hypotheses (PAT scope, env staleness,
timing) were chased before pulling the box's actual SSM invocation output
(`aws ssm list-command-invocations --details`), which held the true errors:

1. `GITHUB_TOKEN: unbound variable` — `/run/pilot/env` is written by
   `fetch-secrets.sh` only at daemon start (`ExecStartPre`); a secret
   seeded/rotated after boot never reaches the apply script's clone step
   (its restart comes *after* the clone). Fixed: console#142 → PR#148.
2. `fatal: not in a git directory` — the apply script runs as **root** via
   SSM; its repo-local `git config` hits pilot-owned clones and git's
   dubious-ownership protection fails repo discovery with that misleading
   message. Made unconditional (= fatal on every apply) by the GH-132
   helper change. Fixed: console#146 → PR#149 (`runuser -u pilot`).

**Rules:**
- Diagnose rollbacks from the box's SSM invocation output, never from the
  console event alone (console#144 files the product fix: capture
  forensics into the journal before reap).
- Any script SSM runs as root that touches pilot-owned git repos must
  `runuser -u pilot --` for the git calls (or it dies with a message that
  points nowhere near ownership).
- `/run/pilot/env` is a boot-time snapshot — anything consuming secrets
  post-boot must refresh via `fetch-secrets.sh` first.

**Refs:** [[bake-workflow-tee-masked-failure-promoted-source-ami]] ·
console#142/#144/#146 · TASK-405
