# Decision: API keys for the hosted console live in Doppler (project `pilot-console`)

## Summary
Founder decision 2026-09-29: third-party API keys for the hosted console (Paddle sandbox and live first, SES/GitHub-App style secrets as they come up) live in **Doppler**, workplace `quantflow`, project `pilot-console`, configs `dev` / `dev_personal` / `stg` / `prd`. The CLI is installed and authorized on the laptop (`doppler v3.76.6`, `doppler me` = Aleks, type cli). At decision time the project held no secrets yet, only the three `DOPPLER_*` auto-vars.

## Context
Paddle L7 was blocked on a sandbox MCP key that had never been stored anywhere. Rather than paste it into the Claude Code plugin's user-config (a sensitive value in `~/.claude`, per-machine, invisible to CI), the founder created a Doppler project so every key has one home with per-environment configs.

## Rules
- Never paste a key into plugin user-config, `~/.pilot/config.yaml`, `.env` files, issue bodies, PR bodies or `.agent/`.
- Inject at run time: `doppler run --project pilot-console --config dev -- <command>`; read one value into a shell: `$(doppler secrets get NAME --plain --project pilot-console --config dev)`.
- Naming: `PADDLE_SANDBOX_API_KEY` (dev/stg), `PADDLE_API_KEY` (prd), `PADDLE_WEBHOOK_SECRET`, one screaming-snake name per key; sandbox and live never share a name.
- ECS/production consumption path (Doppler → SSM or ECS secrets) is Nelya's side; ask, do not build.

## Consequences
- Paddle L7 catalog setup runs the `paddle:catalog-setup` Node-SDK path under `doppler run`, not the MCP server, until the plugin can read its key from the environment.
- A session that needs a key runs `doppler secrets --only-names` first to confirm the name exists, then `doppler run`. Values are never echoed.

## Related
- TASK-497 (Paddle billing)
- TASK-405 (Pilot Cloud)

---
**Captured**: 2026-09-29
**Confidence**: 95%
**Concepts**: secrets, console, billing, process
