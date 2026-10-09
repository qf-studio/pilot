# SOP — Paddle live secrets: source of truth, SSM handoff, rotation

**Scope**: the three live Paddle values the console-api task definition reads (TASK-497 D6). Created 2026-10-09 when the values were first minted.

## Source of truth
Doppler project `pilot-console`, config `prd` (Production only — never Dev/Staging):

| Doppler name | Paddle object | Prefix | Consumer env (console-api) | SSM parameter (Nelya's template) |
|---|---|---|---|---|
| `PADDLE_API_KEY` | API key `pilot-console-prod` (rotatable, 24 scopes, **expires 2027-01-07**) | `pdl_live_` | `PILOT_CONSOLE_BILLING_PADDLE_API_KEY` | `/pilot-fleet/console/BILLING_PADDLE_API_KEY` (SecureString) |
| `PADDLE_WEBHOOK_SECRET` | notification destination `ntfset_01m4gbtxeevnar9mma451sbh1w` | `pdl_ntfset_` | `PILOT_CONSOLE_BILLING_PADDLE_WEBHOOK_SECRET` | `/pilot-fleet/console/BILLING_PADDLE_WEBHOOK_SECRET` (SecureString) |
| `PADDLE_CLIENT_TOKEN` | client-side token `ctkn_01m4g87ga97b6stpk4hxp5zg41` (`pilot-console-ui`) | `live_` | `PILOT_CONSOLE_BILLING_PADDLE_CLIENT_TOKEN` | `/pilot-fleet/console/BILLING_PADDLE_CLIENT_TOKEN` (SecureString; public-safe value, kept uniform) |

Non-secret env set by the template: `PILOT_CONSOLE_BILLING_CHECKOUT_ENABLED` (param `BillingEnabled`, default false), `PILOT_CONSOLE_BILLING_PADDLE_ENVIRONMENT=live`, `PILOT_CONSOLE_BILLING_PRICE_ID=pri_01m4g870rh98m207cjwhjn8taq` (param `BillingPriceId`).

Values never enter chat, Slack, tickets or git. Only prefixes and the first/last characters are ever printed.

## Pre-flight: prove the set loads before any deploy
Run the console's own loader against the Doppler values (what the box will do at boot). Expected: `config.Load OK … L7 CHECK PASSED`, price `49900 USD month 1`. Proven 2026-10-09.

```sh
# in a detached worktree of pilot-console with a throwaway cmd/l7check (see marker 2026-10-09 for the source)
doppler run -p pilot-console -c prd -- sh -c '
  export PILOT_CONSOLE_BILLING_CHECKOUT_ENABLED=true PILOT_CONSOLE_BILLING_PADDLE_ENVIRONMENT=live \
         PILOT_CONSOLE_BILLING_PADDLE_API_KEY="$PADDLE_API_KEY" \
         PILOT_CONSOLE_BILLING_PADDLE_CLIENT_TOKEN="$PADDLE_CLIENT_TOKEN" \
         PILOT_CONSOLE_BILLING_PADDLE_WEBHOOK_SECRET="$PADDLE_WEBHOOK_SECRET" \
         PILOT_CONSOLE_BILLING_PRICE_ID=pri_01m4g870rh98m207cjwhjn8taq
  go run ./cmd/l7check'
```

## SSM handoff (once Nelya grants `ssm:PutParameter` on `/pilot-fleet/console/BILLING_PADDLE_*`)
Doppler → SSM without the value touching a terminal:

```sh
doppler run -p pilot-console -c prd -- sh -c '
for pair in PADDLE_API_KEY:BILLING_PADDLE_API_KEY PADDLE_WEBHOOK_SECRET:BILLING_PADDLE_WEBHOOK_SECRET PADDLE_CLIENT_TOKEN:BILLING_PADDLE_CLIENT_TOKEN; do
  src=${pair%%:*}; dst=${pair##*:}
  eval val=\$$src
  aws ssm put-parameter --profile quantflow --region eu-central-1 \
    --name "/pilot-fleet/console/$dst" --type SecureString --overwrite \
    --key-id "<pilot-fleet console family KMS key id — ask Nelya>" \
    --value "$val" --output text --query Version
done'
```
Verify with `aws ssm get-parameters-by-path --path /pilot-fleet/console/ --query "Parameters[].Name"` (names only). Then Nelya sets `BillingPriceId`, flips `BillingEnabled`, redeploys. Verification sequence is in the 09-30 #infrastructure thread (`1790762207.809729`): unsigned POST → 400, `GET /api/v1/billing/config` → `environment: production`.

## Rotation
- **API key** (90-day expiry → **rotate before 2027-01-07**; Paddle emits `api_key.expiring` ~7 days before — the destination above does not subscribe to it, so set a calendar reminder for 2026-12-20). Dashboard: Developer tools → Authentication → key `pilot-console-prod` → Rotate (key is marked rotatable; old value stays valid for the grace window Paddle shows). New value → Doppler `prd` `PADDLE_API_KEY` → SSM put (above) → Nelya redeploys console-api. Confirm with the pre-flight run.
- **Webhook secret**: never delete/recreate the destination (that rotates the secret with no overlap and drops the logs). If a rotation is forced, the binary accepts `old,new` comma-separated in `PILOT_CONSOLE_BILLING_PADDLE_WEBHOOK_SECRET` for the overlap, then drop the old one.
- **Client token**: create a new token (API or dashboard), swap Doppler + SSM, redeploy, then revoke the old token. Public-safe, so no urgency on exposure, but keep the uniform path.
- **Compromise**: revoke in the dashboard first, then rotate as above; the console refuses to start if a prefix no longer matches the environment, so a bad paste fails loudly at boot.

## Related
- `.agent/system/references/reference_paddle_account.md` (ids, scopes, status)
- `.agent/tasks/TASK-497-paddle-billing-integration.md` (D6 env table, L7)
