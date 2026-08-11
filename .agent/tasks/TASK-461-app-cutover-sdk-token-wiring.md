# TASK-461: GitHub App cutover — SDK-client token wiring (two legs + operator steps)

**Status**: ✅ **BOTH LEGS MERGED — Leg 2 merged 2026-08-10** ([#4824](https://github.com/qf-studio/pilot/issues/4824) → **PR#4827**, same-day). Leg 1 (SDK) merged 08-09 (sdk#109 → [sdk PR#110](https://github.com/qf-studio/studio-sdk/pull/110)). **Post-merge review of PR#4827 PENDING.** Remaining: operator steps below (box App provisioning · `GH_TOKEN` precedence check pilot#4753 · restart + first poll/autopilot action past the ~1h token boundary) → then archive.
**Created**: 2026-08-09
**Assignee**: Pilot (one leg at a time)

---

## Context

Under GitHub App auth, installation tokens expire hourly. The **in-tree** client family was already fixed across GH-4743 (proactively-refreshing `TokenSource`), GH-4747 (`githubTokenFunc`/`newGitHubClient` — per-request resolution, `cmd/pilot/main.go:225-267`), GH-4754 (401 → invalidate → single re-mint retry), GH-4778/PR#4789 (residual sites). The **studio-sdk** clients still freeze the boot token.

sdk#107 → PR#108 (merged 08-07, in v0.32.0) added the client-side API: `TokenFunc`, `NewClientWithTokenFunc(fn, opts...)`, `WithTokenInvalidate(fn)` — token resolved per request attempt inside the retry loop; 401 runs the invalidate hook once then retries once.

**Adapter gap found 2026-08-09** (this session's research, corrects the marker's "wiring leg is next" assumption): `Adapter.NewPoller` (`sdk/integrations/github/adapter.go:51-55`) constructs `NewClient(cfg.Token)` internally and feeds it to the Poller, its MergeWaiter (`poller.go:300`), and board sync/source (`adapter.go:146-152`). No injection seam existed → sdk#109.

### Static-token SDK-client sites in pilot (verified 2026-08-09 @ `c7f649c4`)

| Site | Holder | Lifetime |
|---|---|---|
| `main.go:2085` (polling) / `main.go:826` (gateway) `apGHClient` | **every autopilot controller** (closes/merges/labels) + release summary generator | daemon |
| `main.go:2541` | PR-review webhook handler (`NewWebhookHandler`) | daemon |
| `poller_github.go:478` `sdkClient` | rate-limit scheduler retry callback closure | daemon |
| `poller_github.go:368-378` `sdkCfg.Token` → adapter-internal client | SDK poller + merge-waiter + board sync/source | daemon |
| `handlers.go:761-762` `specClient` | per-event (re-resolves via `resolveGitHubToken` each event) | event — low risk; swap for uniformity |
| `poller_github.go:238` boot verify · `commands.go:2901` CLI | one-shot / short-lived process | fine as static |

`resolveGitHubToken` (`main.go:181`) already mints through the App `TokenSource` cache when `adapters.github.app` is configured, so per-request calls are cache hits, not fresh mints.

---

## Leg 1 — SDK adapter injection (sdk#109, dispatched)

`New(cfg, ...AdapterOption)` + `WithAdapterClient(*Client)`; nil-safe; MergeWaiter/board layer inherit automatically; rotation test pins the no-freeze property. See issue body.

## Leg 2 — pilot wiring (DISPATCHED 2026-08-10 as [#4824](https://github.com/qf-studio/pilot/issues/4824), `pilot` + `no-decompose`)

Issue body follows this draft (anchors refreshed @ `39881bba`):

1. **Bump studio-sdk** to a version containing #108 + #109 (pseudo-version pin is established practice — the current pin `v0.31.2-0.20260721...` is itself one).
2. **`newGitHubSDKClient(cfg *config.Config) *githubSDK.Client`** beside `newGitHubClient` (`main.go:265`): `githubSDK.NewClientWithTokenFunc(githubSDK.TokenFunc(githubTokenFunc(cfg)), githubSDK.WithTokenInvalidate(invalidateGitHubAppToken(cfg)))`. Note `invalidateGitHubAppToken` returns nil when App auth isn't configured — SDK `withAuthRetry` treats a nil hook as no-retry (safe), but pass the option conditionally to keep intent explicit.
3. **Swap daemon-lifetime sites**: `main.go:826`, `main.go:2085`, `main.go:2541`, `poller_github.go:478`, `handlers.go:762` → the shared helper.
4. **Poller fan-out**: construct one shared client in `githubPollerRegistration.CreateAndStart`, thread it through `startGithubSDKPollerForRepo` (replacing the `token string` param), inject via `githubSDK.New(sdkCfg, githubSDK.WithAdapterClient(client))`. One client across the fan-out matches today's one-token-shared design (rate-limit budget is per-credential).
5. **Boot fail-loud gate stays** (`poller_github.go:228-240`): keep the empty-resolve check, but run `verifySDKGithubToken` against the shared TokenFunc-backed client so the verified path is the production path.
6. **Comment parity**: update the "studio-sdk client … until later phases" comments at `main.go:824-826`/`2083-2085`.
7. **Tests**: rotation test at the pilot seam (token rotated between two requests is picked up — reuse sdk#107's test shape via `WithClientBaseURL` against a local test server); no-App-config path byte-identical.

## Operator steps (after Leg 2 deploys — box, per `pilot-aws` skill rules)

1. App provisioning: `adapters.github.app` (App ID, installation ID, private key) in box config.
2. Box-env `GH_TOKEN` check — pilot#4753 precedence finding: ensure env `GH_TOKEN` doesn't shadow the App path for `gh`-CLI/executor credentials.
3. Restart + watch: `token_source=app` in startup log; first poll AND first autopilot action past the ~1h token boundary.

---

## Out of Scope

- Executor `gh`/git credential plumbing (`internal/executor/gh_credentials.go`) — separate surface, already TokenSource-aware per GH-4743 wave.
- In-tree client sites — done (GH-4747/4754/4778).
- Approval-architecture legs (roadmap owns those).

## Refs

- sdk#107 → PR#108 (client TokenFunc, v0.32.0) · [sdk#109](https://github.com/qf-studio/studio-sdk/issues/109) (adapter injection, leg 1)
- Roadmap: `.agent/system/approval-architecture-roadmap.md` § post-merge follow-ups (sdk#107 bullet — superseded by this doc)
- In-tree precedent: GH-4743 · GH-4747 · GH-4754 · GH-4755/PR#4764 · GH-4778/PR#4789

**Last Updated**: 2026-08-09
