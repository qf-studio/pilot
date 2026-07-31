# feat(secrets): env-gated local secrets driver — credential/connection writes 500 on the local stack (no AWS), blocking SOP real-stack verify of the S3 credentials leg

**Created**: 2026-07-30 · **Status**: ✅ Delivered (console PR#84 merged) + **verified 2026-07-31**: real-stack PUT reaches Anthropic live-validation in 370ms (no IMDS timeout); driver e2e suite 9/9 green vs real pg incl. persists-across-reopen. Happy-path final leg needs a real key (operator) · **Last Updated**: 2026-07-31
**Repo**: `qf-studio/pilot-console` · Drift-defect **#7** (environment-class: local stack cannot exercise the write path at all)

## Problem

`internal/secrets/writer.go` writes tenant secrets exclusively via AWS SSM
`PutParameter` (SecureString under `/tenants/{org}/{key}`). On the local
docker stack (GH-77) there are no AWS credentials, so the SDK default chain
falls through to EC2 IMDS (`169.254.169.254`), times out after ~6–10s, and
**every** `PUT /api/v1/credentials/{key}` and `PUT
/api/v1/connections/{tracker}` returns 500:

```
orgs: write secret failed: … SSM: PutParameter … no EC2 IMDS role found …
dial tcp 169.254.169.254:80: i/o timeout
```

`docker-compose.yml` (~lines 137–151) documents this as out-of-scope for
GH-77. That call is now stale: the adopted SOP
(`sops/quality/real-stack-verify-gates-ui-merges.md`, pilot repo) gates UI
merges on operator verification against the live local stack, and the S3
credentials/connections leg is currently **unverifiable there by
construction**. It already cost one operator confusion loop today (key
"saved", then gone — see companion ui issue, drift-defect #6).

## Fix (mandated shape)

1. Introduce a driver seam at the existing `secrets` package boundary:
   `PILOT_CONSOLE_SECRETS_DRIVER=ssm|postgres`, **default `ssm`** — prod
   posture byte-identical when the var is unset.
2. `postgres` driver: a dedicated table (e.g. `local_secrets(org_id, key,
   value, last4, updated_at)`) implementing the same writer interface the
   org/proxy registration paths construct (`internal/secrets.NewWriter` call
   sites in `main.go`). Plaintext-at-rest is acceptable and should be
   explicitly labeled local-dev-only in the table comment and package doc.
3. Presence semantics of `GET /api/v1/credentials` (configured/last4,
   write-only reads — GH-81 contract) must hold identically under either
   driver.
4. `docker-compose.yml`: set `PILOT_CONSOLE_SECRETS_DRIVER: postgres` for the
   console service and rewrite the stale "will fail without real AWS access"
   comment block.

## Acceptance criteria

- Local stack: `PUT /api/v1/credentials/anthropic` → 2xx; subsequent `GET
  /api/v1/credentials` shows `configured: true` + correct last4; state
  survives a console container restart (persisted in pg, not memory).
- Same for `PUT /api/v1/connections/{tracker}` secret material.
- Unset/`ssm` driver: existing behavior and tests unchanged (zero prod drift).
- Unit tests for the postgres driver; writer-seam test proving driver
  selection by env.

## Non-goals

- Local fleet provisioning still out of scope (GH-77 non-goals stand).
- No change to how hosted instances read their secrets at boot.
- No encryption scheme for the local table — it is dev-only by contract.

## Refs

- Evidence: console request log 2026-07-30 18:28Z (two 500s, IMDS timeout) —
  GH-79's request log located this in minutes, as designed.
- Stale limitation comment: `docker-compose.yml` ~137–151 (GH-77).
- Companion (independent):
  [pilot-console-ui#36](https://github.com/qf-studio/pilot-console-ui/issues/36)
  credential/connection save failures are silent (drift-defect #6).
- Pilot issue: https://github.com/qf-studio/pilot-console/issues/83
