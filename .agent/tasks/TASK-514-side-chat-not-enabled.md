# TASK-514: Console side chat shows "Chat isn't enabled for this org" on every tenant

**Status:** ✅ 3 of 4 legs MERGED + REVIEWED 2026-10-09 (console PR#385 13:07Z, ui PR#222 13:08Z, console PR#386 autopilot ~13:30Z; all APPROVE-w-notes, every mutation pin run by the reviewer and proven real — Pilot listed them "Not verified"). Leg 4 pilot#5629 → PR#5630 reviewed APPROVE-w-notes (CI pending at review time; autopilot merges on green). **Premise correction:** the example yaml already had the `adapters.chat` stanza (line ~250); the issue text was wrong (truncated grep in research). PR still adds a real load-test guard. **Console deployed 13:13Z (750c5f1 = #385+#386, prod-0.1.23); UI deployed 13:11Z (#222). Sweep is 60 s, so tenant daemons were bumped + restarted ~13:14Z. Awaiting founder's browser confirmation.**
**Created:** 2026-10-09
**Last Updated:** 2026-10-09
**Trigger:** founder screenshot 2026-10-09: banner "Chat isn't enabled for this org. Reach out to your admin." in the prod console side chat.

## Root cause (verified on origin/main of all three repos)

1. pilot-console `internal/fleet/configrender.go` `RenderTenantConfig` writes `adapters:` with github / jira / linear only. No `adapters.chat.enabled: true`. `git grep -i chat origin/main -- internal/fleet` is empty.
2. Daemon default is `web.DefaultConfig()` → `Enabled: false` (pilot `internal/config/config.go:162`, `internal/adapters/web/config.go:27`). With chat disabled the gateway never registers the chat routes (`internal/gateway/server.go:277-282`) → 404.
3. Console `internal/chatapi/handlers.go` `mapGatewayError` turns a daemon 404 into 409 `chat_not_enabled`; UI `ChatWindow.vue:204` renders the banner.
4. Chat was only ever proven (TASK-464 / TASK-469, 2026-08-11) against a hand-written config. Tenant configs come from the renderer, so no tenant ever had it.

**Rollout constraint:** the repo-sync sweep (`internal/fleet/reposync.go`) re-renders only when Repos/Jira/Linear params differ. A renderer-only fix reaches NEW instances only. Fix adds `ChatEnabled` to `ConfigParams` and the sweep sets it true when false → one new generation + push + restart per existing instance (push is restart-to-apply, no hot reload). **Every tenant daemon restarts once after the console deploy.**

## Latent bugs found alongside (filed)

- UI `src/stores/chat.ts` cursor only moves up (`Math.max`); daemon buffer is in-memory, seq restarts at 0 on daemon restart / instance replacement / 1 h expiry → polls empty forever until hard reload. `latestSeq` exists for exactly this (GH-4843) but the store never uses it to reset.
- Errors invisible: console maps every non-404 to 502 with no log of status/cause; UI `poll()` swallows plain `ApiError`; failed send restores the draft silently.
- `internal/chat/client.go` interpolates the conversation id unescaped.
- ~~Daemon `configs/pilot.example.yaml` never documented `adapters.chat`~~ — WRONG, the stanza existed at line ~250; research grep was truncated by `head`. Leg 4 became a comment clarification + load test.

## Dispatched legs

| Leg | Issue | Repo | Scope |
|---|---|---|---|
| 1 (the fix) | [console#383](https://github.com/qf-studio/pilot-console/issues/383) | pilot-console | `ChatEnabled` param, renderer stanza, provisioner + consolectl set true, repo-sync sweep bump, tests |
| 2 | [console#384](https://github.com/qf-studio/pilot-console/issues/384) | pilot-console | log status+cause in `mapGatewayError`, `url.PathEscape` conversation id |
| 3 | [ui#221](https://github.com/qf-studio/pilot-console-ui/issues/221) | pilot-console-ui | cursor reset when `cursorSeq > latestSeq`, inline send error |
| 4 | [pilot#5629](https://github.com/qf-studio/pilot/issues/5629) | pilot | document `adapters.chat` in the example yaml |

## Verify after Leg 1 deploys to prod

1. Console log: `reconcile` Info line with reason `chat_enabled` per instance; then a config push + daemon restart per instance.
2. Tenant daemon log: "Chat API initialized for gateway mode" (pilot `internal/pilot/pilot.go:848`). If instead "Chat API enabled in config but no runner was constructed" (`cmd/pilot/main.go:1175`) → gateway-mode runner wiring gap, separate issue.
3. Browser: banner gone; send a message; a reply renders within ~3 s polls; network tab shows `GET /api/v1/chat/conversations/<id>/events` 200 with growing `latestSeq`.
4. Restart one tenant daemon, send again without reload → reply still renders (Leg 3).

## Not in scope / known gaps

- Orgs with zero connections are skipped by the sweep (`!repoFound && !jiraFound && !linearFound → continue`), so they get chat only on their first connection. Acceptable: no repos → chat can't dispatch tasks anyway.
- `ownSentSeqs` dedup in the store is dead code (accept-time seq is the previous event, not the user's message). Not filed; cosmetic.
- Local dev: console chat proxy needs private IP + SSM, does not work from a laptop (learning `console-ssm-paths-work-locally-proxy-does-not`).
- Local checkouts of console (−7) and ui (−9) are behind origin/main; all research done against origin/main.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot-console/issues/383 (leg 1, the fix)
- Pilot issue: https://github.com/qf-studio/pilot-console/issues/384 (leg 2)
- Pilot issue: https://github.com/qf-studio/pilot-console-ui/issues/221 (leg 3)
- Pilot issue: https://github.com/qf-studio/pilot/issues/5629 (leg 4)
- Research: navigator-research agent 2026-10-09 (7-section map, in-session).
- Prior: TASK-464 (console proxy), TASK-469 (gateway wiring + latestSeq), gh-4835 (daemon contract).
- Node 25 caveat for local UI specs: `NODE_OPTIONS=--no-experimental-webstorage`.
