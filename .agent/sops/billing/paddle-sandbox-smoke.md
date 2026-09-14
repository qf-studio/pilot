# SOP: Paddle sandbox smoke test (TASK-497 L6)

Proves the whole billing path against Paddle's **sandbox** before anything is
enabled in live: checkout -> webhook -> `billing_status` -> enforcement sweep
(dry run) -> recovery -> cancel. Roughly 45 min once the founder gate in step 3
is cleared.

**Never run any part of this against the live account.** Live has no
notification destination yet, and recreating one rotates its secret (see
TASK-497 L7 guardrails).

## What it exercises

| Leg | Surface | Merged in |
|---|---|---|
| L1 | customer + transaction creation, `/billing/checkout-session` | console PR#298/#301 |
| L2 | `Paddle-Signature` verify, `billing_events` ledger, ordering guard, state table | console PR#302/#304 |
| L3 | checkout route, Subscribe / Manage / Update card, activation polling | ui PR#157/#161 |
| L4 | `/billing/portal-session` | console PR#307 |
| L5 | `consolectl billing set-status`/`resync`; enforcement sweep + suspended lifecycle | console PR#309/#312 |

## Prerequisites (all verified present 2026-09-14)

- Docker + Compose v2, `openssl`, `ngrok` (authed), `bun`, `go`.
- Sibling checkout of `qf-studio/auth-service` next to `pilot-console`.
- Sandbox catalog ids and the public client token: see
  `system/references/reference_paddle_account.md`.
- **Founder-held secret**: the sandbox API key (`pdl_sdbx_...`). Never in the
  repo, never in chat.

## Step 1 — local stack

```bash
cd ~/Projects/startups/pilot-console
make local-up      # postgres + redis + auth-service + console, all 4 healthy
make local-seed    # demo@pilot.local / PilotDemoPass2026!, demo org
curl -s localhost:8090/health
```

Billing routes 404 at this point: that is correct, `CHECKOUT_ENABLED` defaults
off and the routes are not registered at all.

## Step 2 — public tunnel

Paddle must reach the webhook endpoint, so the console needs a public URL.

```bash
ngrok http 8090 --log=stdout > /tmp/ngrok-l6.log 2>&1 &
curl -s http://127.0.0.1:4040/api/tunnels | python3 -c "import sys,json;print(json.load(sys.stdin)['tunnels'][0]['public_url'])"
```

Verified: a POST through the tunnel reaches the Go mux directly, with no ngrok
browser interstitial in the way. The free-tier URL **changes on every restart**
— if the tunnel dies mid-smoke, *update* the destination's URL in the Paddle
dashboard rather than creating a second one.

## Step 3 — credentials and the notification destination  ← FOUNDER GATE

Config lives outside both repos, at `~/.config/pilot-local/`:
`billing-override.yml` (compose override, no secrets) and
`paddle-sandbox.env` (mode 600; non-secret values pre-filled — environment,
price id, client token, `PAST_DUE_GRACE=1m`, `ENFORCE_ENABLED=0`).

1. **Founder** pastes the sandbox API key into `PILOT_CONSOLE_BILLING_PADDLE_API_KEY=`
   in `paddle-sandbox.env`. This is the only step nobody else can do.
2. Create the notification destination against the tunnel URL. Either the
   `paddle-sandbox` MCP, or the REST API with that key:

   ```
   POST https://sandbox-api.paddle.com/notification-settings
   { "description": "pilot-console local smoke",
     "destination": "<tunnel>/api/v1/billing/webhook",
     "type": "url", "api_version": 1, "active": true,
     "subscribed_events": ["subscription.created","subscription.activated",
       "subscription.updated","subscription.past_due","subscription.paused",
       "subscription.resumed","subscription.trialing","subscription.canceled",
       "transaction.completed","transaction.payment_failed"] }
   ```

   The response carries `endpoint_secret_key` (`pdl_ntfset_...`) **once**. Put it
   in `PILOT_CONSOLE_BILLING_PADDLE_WEBHOOK_SECRET=`.
3. Restart the console with billing on:

   ```bash
   docker compose -f docker-compose.yml -f ~/.config/pilot-local/billing-override.yml up -d console
   curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8090/api/v1/billing/webhook -d '{}'   # 403, not 404
   ```

   **400**, not 403, is the success signal here: the route registered and the
   signature check ran (`webhook signature verification failed`,
   `header_present: false`). A 404 means the flag did not take.

## Step 4 — checkout (happy path)

**Tunnel the UI, not the console.** Vite proxies `/api` to the console, so one
tunnel gives both an https origin for the overlay and a public webhook path:

```bash
cd ~/Projects/startups/pilot-console-ui && VITE_API_MODE=http bun run dev
ngrok http 5173 --host-header=localhost:5173      # host rewrite, else Vite 6 blocks the host
```

Point the notification destination at `<tunnel>/api/v1/billing/webhook` and
**update** it if the tunnel URL changes; never create a second one.

Two things that do not work, both learned the hard way on 2026-09-14:

- **Plain http on localhost is not enough.** Paddle's overlay needs an https
  parent origin; on http it loads Paddle.js and then reports a CSP violation to
  Paddle's own Sentry instead of rendering. Use the tunnel origin.
- **`bun run dev:https` (Vite basic-ssl) produces an untrusted certificate.**
  Chrome shows an interstitial that browser automation cannot click through, so
  the Subscribe redirect dies there. Either click it through by hand once, or
  install `mkcert` (a system trust-store change — ask the operator first), or
  use the tunnel, which has a real certificate.

Sign in as the demo user, open Settings -> Billing, press Subscribe. Pay with
the sandbox test card `4242 4242 4242 4242`, any future expiry, any CVC.

Expect, in order: the Paddle overlay opens on our own route (not a redirect to
Paddle's domain) -> the page polls `GET /api/v1/billing/status` -> chip flips to
active. The chip is driven by the **webhook**, never by the browser event, so a
chip that never flips means the webhook did not arrive. Check the ledger:

```sql
SELECT event_id, event_type, occurred_at, org_id FROM billing_events ORDER BY occurred_at;
SELECT name, billing_status, billing_customer_id, billing_subscription_id,
       billing_status_event_at FROM organizations WHERE name = 'Pilot Demo Org';
```

## Step 5 — portal (L4)

Press Manage subscription, then Update payment method. Each press must mint a
**fresh** session: two presses produce two different URLs, and the response
carries `Cache-Control: no-store`. Both open in a new tab, never an iframe.

## Step 6 — past_due, grace, dry-run suspend (L5)

Use Simulations. **The notification destination must have `traffic_source` set
to `all`** or the simulation is rejected with "Notification setting cannot be
used for 'simulation' traffic" — a destination is created as `platform` only.
Patch it once:

```
PATCH https://sandbox-api.paddle.com/notification-settings/{id}   {"traffic_source":"all"}
```

Simulated events carry synthetic customer ids, so they resolve to no
organization: they are ledgered with a null `org_id` and acked 200, with one
Warn each. That is the designed behaviour and worth asserting — an
unresolvable event must be recorded, never dropped. To move a real org's
status, use `consolectl billing set-status` (step 8) or a real checkout.

Expect: `billing_status` -> `past_due`, `billing_status_event_at` set, the UI
shows "payment failed" plus the grace note sourced from `past_due_grace_hours`.
With `PAST_DUE_GRACE=1m`, within a minute the reconciler tick logs, at Warn:

```
reconcile: org past_due within grace window; not suspending yet
reconcile: billing enforcement disabled; would suspend instance ... action=suspend
```

**`ENFORCE_ENABLED=0` is deliberate.** The sweep decides and logs; it writes
nothing. Flipping it to `1` in this smoke is optional and only meaningful if the
local stack has an instance row, which it does not by default (fleet is off
locally). Proving the decision path via the Warn lines is the real objective.

## Step 7 — recovery, cancel, failure card

- Simulate `subscription.updated` with status `active` -> back to `active`; with
  enforcement on, the resume path would fire only for instances whose latest
  journal event is `billing_suspended`.
- Cancel via the simulator: `subscription.updated` carrying a scheduled change
  arrives first, then `subscription.canceled` at the effective date ->
  `inactive`.
- Retry checkout with the declining card `4000 0000 0000 0002` and confirm the
  UI error path is honest.

## Step 8 — operator tools (L5 part 2)

```bash
export DATABASE_URL=postgres://postgres:postgres@localhost:5432/console?sslmode=disable
go run ./cmd/consolectl billing set-status --org "$ORG" --status active --reason "smoke"
go run ./cmd/consolectl billing resync --org "$ORG"
```

Both must append to `billing_events` (event types `operator_override` and
`resync`), never write the column directly. `resync` needs the org to already
carry a `billing_subscription_id`, so run it after step 4.

## Teardown

```bash
pkill -f 'ngrok http'
cd ~/Projects/startups/pilot-console && make local-down   # or local-nuke to drop volumes
```

Leave the sandbox notification destination in place for the next run, but
remember its URL dies with the tunnel. Nothing in the live account is touched by
any step here.

## What the local stack cannot prove

The enforcement sweep runs on the fleet reconciler tick, and fleet is off in the
local stack, so steps 6 and 7 exercise the status transitions and the config
endpoint but **not** the sweep itself. The sweep's decision table, its
conditional writers and its tick wiring are covered by the unit and
Postgres-backed tests merged with it. Proving it live needs a staging
deployment with fleet enabled and at least one instance row.

## Known traps

- A chip that never flips is a webhook problem, not a UI problem. Check ngrok's
  inspector at `http://127.0.0.1:4040` first.
- `WEBHOOK_IP_ALLOWLIST` must stay **off**. The console resolves the client IP
  from `RemoteAddr` unless `TRUSTED_PROXY_CIDRS` is set, and through a tunnel
  every delivery arrives from the tunnel's address.
- Sandbox only emails the registered account address.
- The sandbox business name has a cosmetic typo, "Quntflow".
- ngrok's free tier shows a one-time interstitial to browsers. Click "Visit
  Site" once per session, or send an `ngrok-skip-browser-warning` header.
- The Vite dev server must be started as a durable background process. Started
  from a shell that then exits, it dies and the page reports
  "server connection lost" a few seconds after each load.

## Refs

- `tasks/TASK-497-paddle-billing-integration.md` (D1-D7, legs, founder inputs)
- `system/references/reference_paddle_account.md` (ids, tokens, what is unset)
- Console README sections "See it locally" and "Billing (Paddle)"
