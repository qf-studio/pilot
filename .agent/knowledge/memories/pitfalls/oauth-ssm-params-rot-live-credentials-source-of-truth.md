---
name: oauth-ssm-params-rot-live-credentials-source-of-truth
description: A CLAUDE_CODE_OAUTH_TOKEN copied into SSM is a snapshot that expires into silent 401 loops — the live ~/.claude/.credentials.json on a working machine is the only source of truth; and a funded-looking ANTHROPIC_API_KEY can still have zero credits
type: pitfall
---

# OAuth params in SSM rot — live credentials are the source of truth

**What happened (2026-07-24):** the hosted canary tenant could not execute.
Two independent credential faults, neither visible as "bad credential":

- `/pilot/ANTHROPIC_API_KEY` — present, well-formed, **zero credits**.
- `/pilot/CLAUDE_CODE_OAUTH_TOKEN` — present, well-formed, **stale**;
  produced 401 loops.

Resolution: take the box's *live* `~/.claude/.credentials.json` and ship it
to the instance via S3 with SSE-KMS, then delete the object. Not a fix — a
transfusion.

## Mechanism

- OAuth credentials **refresh**. The value in `~/.claude/.credentials.json`
  on a running machine rotates; the copy pasted into SSM does not. The SSM
  parameter is a photograph of a credential that has since moved on.
- Failure presents as 401 *loops*, not a startup error — the process is
  healthy, retrying, and burning quota against a dead token.
- A present-and-parseable API key tells you nothing about funding. Zero
  credits looks identical to a valid key until a request is billed.
- Console's `secrets.validKeys` **does not include**
  `CLAUDE_CODE_OAUTH_TOKEN`, so the supported path can't seed it — it had to
  go in via a raw SSM put, outside the control plane's own model of secrets.

## How to avoid

1. Treat SSM-stored OAuth tokens as expiring caches, never as durable
   config. If a hosted unit authenticates via OAuth, it needs a *refresh
   path*, not a copied parameter.
2. When a hosted agent 401s, compare against the live
   `~/.claude/.credentials.json` on a machine that currently works before
   suspecting IAM, KMS, or networking.
3. Check credit balance separately from key validity — "the key is right"
   and "the key can pay" are different assertions.
4. Add `CLAUDE_CODE_OAUTH_TOKEN` to console `secrets.validKeys` so seeding
   stops requiring an out-of-band SSM put.

**Founder decisions open (2026-07-24):** fund the API key vs. adopt
OAuth-for-dogfood as the supported hosted path. Two subscription consumers
now share one founder account (box + canary instance).

Related: [[ready-gate-couples-credential-validity]] (how this presents — as a
lifecycle failure, not an auth failure), [[claude-cli-refuses-root-hosted-units]]
(same-day hosted cascade).
