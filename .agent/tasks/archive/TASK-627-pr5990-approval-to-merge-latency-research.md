# TASK-627: PR#5990 approval→merge ≈2 h — research findings (no daemon defect on the merge path)

**Status**: ✅ DONE 2026-11-01 — P1 PR#6000 released as **v2.307.0** (cut by hand 16:45Z, box hot-upgraded 17:02:46Z) · S1 released studio-sdk v0.38.5 · box studio-sdk gate override live 16:01Z · research closed (merge path clean; 10-31 click time undecidable). Remaining: the first Slack click on 2.307.0 shows `action_ts` + `delivery_delay_ms`; first SDK run shows `project_override=true`.
**Origin**: 10-31 marker working note "approval → merge latency was ≈2 h on PR#5990 (approved ~17:30Z, `merging` at 19:27Z)"
**Data sources**: box `daemon.log` via SSM (named per rule 6 of `pilot-aws`), Slack DM `D09HGS3BR4J` via the Slack connector, repo code at `a3c773952`.

## Result in one line

The daemon took **2 min 0 s** from the Slack click to the merge. The 2 h 16 min sit **before** the click reached the daemon: either the founder clicked at 19:25Z (human latency, inside the observed distribution) or an earlier click was lost in Slack transit (the 10-16 PR#5737 class). The log cannot tell the two apart.

## Timeline (box daemon.log, UTC, 2026-10-31)

| Time | Event | Source line |
|---|---|---|
| 16:55:05 | PR#5990 created, registered `stage=pr_created env=stage` | autopilot |
| 17:07:22 | CI passed → `ci_passed` | autopilot |
| 17:08:24 | `merge gate escalated: requiring human approval` reason = pre-PR REQUEST-CHANGES (TASK-620 L5) → `awaiting_approval` | autopilot |
| 17:09:23 | `async approval request submitted` request_id `pr-5990-1793466563558177753` channel=slack destination=D09HGS3BR4J **timeout=24h** | approval |
| 17:28:21, 17:29:11 | `slack: envelope missing envelope_id, skipping` ×2 | slack.socket_mode |
| 17:29:21 | `slack: websocket read error … close 1006` → `reconnecting to Socket Mode delay=1s` (reconnect succeeded: no `failed to dial` / `failed to get new WSS URL`, next refresh exactly 5 h after the 21:13Z restart) | slack |
| 18:08:26 | `alert fired rule=autopilot_deadlock severity=critical` (1 h without a stage transition — this PR waiting on a human) | alerts |
| **19:25:27** | `Slack approval interaction received … action=approved user=hello` → `approval decision applied to PR state` (6 ms) | approval.slack / autopilot |
| 19:26:23 | `approval granted — advancing to merging stage` (next 60 s tick) | autopilot |
| 19:27:24 | `handleMerging: attempting merge attempt=1 method=squash`; `verifyCIBeforeMerge: CI passed` | auto-merger |
| 19:27:27 | `PR merged` | auto-merger |

Click → merged: **2 min 0 s** (two controller ticks at the 60 s cadence, `internal/autopilot/controller.go` tick loop ≈ 9980–10060: 10 s while any PR is `waiting_ci`/`pr_created`, else 60 s).

## What the Slack DM shows

One message in the window, ts `1793466563.669229` (= 17:09:23Z, the request card). The daemon **overwrote it in place** with ":white_check_mark: APPROVED … Decision by: hello" when the decision was applied. Slack history therefore carries no record of when the button was clicked, nor of a failed earlier click.

## Approval latency on the box since per-click logging (GH-5758, first line 2026-10-21)

22 requests with a received click. Request → click: 5 s, 6 s, 7 s, 9 s, 12 s, 20 s, 20 s, 25 s, 29 s, 1 m 08 s, 6 m, 8 m, 10 m, 10 m, 20 m, 26 m, 30 m, 48 m, 52 m, **2 h 16 m (PR#5990)**, 12 h (overnight), 14 h (overnight). Every received click was applied within 2 s. PR#5990 is the slowest same-day response but not a statistical outlier. 36 requests total since 10-01, 35 decisions, 0 expired, 1 relayed by hand (PR#5737, 10-16 — the one proven lost click).

## Code path verified (no defect found on hold → approved → merging)

1. Hold: `merge gate escalated` → `awaiting_approval`; request submitted with `approval.pre_merge.timeout` default 24 h (log `timeout=24h0m0s`).
2. Click ingress on the box is **Socket Mode only** (`Slack ingress socket_mode=true http_interactions_route=false`, boot line 11-01 13:20:42Z). Path: `internal/adapters/slack/socketmode.go:201` `runConnection` → `socket_mode.go:159` `handleRawMessage` (ack, emit) → `handler.go:252` `approvalHandler.HandleInteraction` → `internal/approval/slack.go:484` logs `Slack approval interaction received` **before any lookup** (GH-5758) → decision recorded → `Controller.SetApprovalDecision` applies to `PRState` (same ms).
3. Next tick: `approval granted — advancing to merging stage`; tick after: `handleMerging` → `verifyCIBeforeMerge` → merge.

The subagent's candidate causes (decision not applied to in-memory state, re-escalation discarding the approval, CI-verify breaker, draft/changes-requested holds) are all refuted by the log lines above.

## Two real but small defects found on the way

**D1 — the Socket Mode handler cannot see Slack's `disconnect` envelopes.** Slack's `hello` and `disconnect` messages carry **no `envelope_id`** (Slack docs, "Using Socket Mode"). `socket_mode.go:167` returns early on an empty `envelope_id` with the WARN `envelope missing envelope_id, skipping`, so the `disconnect` branch at `socket_mode.go:184` is unreachable (box count of `disconnect envelope received`: **0** since 10-01). The client never reconnects proactively; it waits for Slack's forced close ~10 s after `refresh_requested` and then reconnects 1 s later (`socketmode.go:31` `initialReconnectDelay`). Effect: every ~5 h (Slack `approximate_connection_time`) a blind window of ~11–12 s plus two WARN lines of noise. Slack says payloads "may be sent to any" open connection and recommends opening the replacement connection before the refresh. Pattern on the box: `warning` at T−60 s, `refresh_requested` at T−10 s, close at T — 4 cycles/day. The 17:28–17:29Z refresh on 10-31 sits ~1 min from the marker's "~17:30Z"; the 10-16 lost click (15:20–15:35Z) had **no** refresh in its window, so the refresh is not the mechanism for that case.

**D2 — the per-click log line omits the click time.** The block_actions payload carries `actions[].action_ts` (parsed into `ActionTS` at `internal/adapters/slack/webhook.go:31`/`:77`) but `HandleInteraction` (`handler.go:47`, `approval/slack.go:484`) never receives or logs it. Logging `action_ts` and `received − action_ts` would make a delayed or retried delivery visible, which is the one fact missing from both the 10-16 and 10-31 cases.

**Noise item** — `autopilot_deadlock` (`internal/alerts/types.go:483`, 1 h without a stage transition, severity critical) fires for a PR that waits on a human in `awaiting_approval`. It did not shorten the wait on 10-31 (fired 18:08Z, click 19:25Z).

## Open fact (founder only) — asked 2026-11-01, answer: cannot recall

The founder does not remember whether there was an earlier click. The 10-31 case is closed as **undecidable**; no change to the merge path. Leg A makes the next case decidable.


Did the founder click **once at 19:25Z (20:25 CET)**, or also earlier around 17:30Z (18:30 CET)? An earlier click that produced no `Slack approval interaction received` line = a lost click in Slack transit (second instance after PR#5737). A single click at 19:25Z = human latency, nothing to fix on the merge path.

## Proposed legs (not dispatched)

- **Leg A (diagnostic, small)**: thread `action_ts` through `ApprovalCallbackHandler.HandleInteraction` and log it with the delta on the GH-5758 line. Independent of the open fact; worth doing either way.
- **Leg B (hygiene, small)**: in `handleRawMessage`, dispatch on `type` before the `envelope_id` check: `hello` → Debug, `disconnect` → the existing disconnect branch (ack only when an `envelope_id` is present); open the replacement connection on `refresh_requested` instead of waiting for the 1006 close. Keep a test with a disconnect envelope that has no `envelope_id` (today's fixtures at `socket_mode_test.go:520` all include one, which is why the dead branch passed).
- **Leg C (optional)**: exclude `awaiting_approval` from `autopilot_deadlock`, or lower it to warning for that stage.

## Correction 2026-11-01 (dispatch pass)

The live Socket Mode client on the box is the SDK's `sdk/integrations/slack/socketmode.go` (studio-sdk v0.38.4, pinned in `go.mod`), started by `cmd/pilot/main.go` through `sdkSlack.New(...).NewChatBridge` in polling mode. The `slack: ` log prefix is the SDK's. This repo's `internal/adapters/slack/socket_mode.go` is the gateway-mode copy and is not what the box runs; it has the same dead branch. The SDK handler already treats `hello` at Debug (why no third WARN per cycle). `core.MessageEvent` has no event time, so the click time cannot be logged from this repo alone. Legs A+B therefore start in the SDK; the pilot leg follows the pin bump.

## Legs

### LEG S1 — repo: qf-studio/studio-sdk — title: fix(slack): Socket Mode handles Slack's envelope_id-less disconnect before the ack gate and reconnects at once; callback MessageEvent carries the click time as Timestamp

## Context

Pilot's founder box runs Slack approvals over Socket Mode through this SDK. Its daemon log shows the same four-line cycle every ~5 h (Slack's `approximate_connection_time`): two WARN `slack: envelope missing envelope_id, skipping` at T−60 s and T−10 s, then WARN `slack: websocket read error … close 1006`, then `slack: reconnecting to Socket Mode delay=1s`. The INFO `slack: disconnect received` has fired 0 times in a month of log. Cause: Slack's `disconnect` messages (reasons `warning`, `refresh_requested`, `link_disabled`) carry no `envelope_id` (Slack docs, "Using Socket Mode"), and `handleRawMessage` in `sdk/integrations/slack/socketmode.go` returns at the empty-`envelope_id` check before the `disconnect` branch. The client never reconnects on request; it waits ~10 s for Slack's forced close. Slack may route a payload to any open connection and recommends opening the replacement before the refresh.

Second gap: a Slack button click reaches the host as `core.MessageEvent{Action: "callback"}` (built in `sdk/integrations/slack/bridge.go`) with no event time. The interaction payload carries `actions[].action_ts` (already decoded into `InteractionActionDef.ActionTS` in `sdk/integrations/slack/types.go`, never read). Without it a host cannot tell a late click from a lost one (two such cases on the box, 2026-10-16 and 2026-10-31).

## Scope

1. In `handleRawMessage`: dispatch on `env.Type == "disconnect"` before the empty-`envelope_id` check. Log INFO `slack: disconnect received` with `reason` (keep the `server requested disconnect` fallback), emit `SocketEventDisconnect` and `Close()` as today, so `runConnection` returns and `listenLoop` reconnects after `initialReconnectDelay`. Acknowledge only when `envelope_id` is non-empty. Keep the `hello` Debug branch. The remaining empty-`envelope_id` WARN gains a `type` attribute.
2. In `sdk/core/chat.go`: add `Timestamp string` to `MessageEvent` — the platform's own time for the event as the platform reports it (Slack callback: `actions[0].action_ts`); empty when the platform reports none. In `bridge.go` set `Timestamp: action.ActionTS` on the callback event. Other `Action` kinds stay unchanged.
3. Tests, table-driven: (a) a SocketModeHandler test over an httptest WebSocket server that sends `{"type":"disconnect","reason":"refresh_requested"}` without an `envelope_id` and asserts one `SocketEventDisconnect` on the channel, no ack frame written, and the connection closed; (b) the same with `{"type":"disconnect","envelope_id":"env-9","reason":"link_disabled"}` asserting the ack frame is written; (c) a bridge callback test with `action_ts` asserting `ev.Timestamp`; (d) extend `TestMessageHandler_CallbackVariant` in `sdk/core/chat_test.go` with `Timestamp`.

## Acceptance

- Feeding the envelope_id-less `disconnect` yields a `SocketEventDisconnect` event and a closed connection; no ack frame is written.
- A callback whose payload has `actions[0].action_ts` = `1793466563.669229` produces a `MessageEvent` with `Timestamp` = `1793466563.669229`.
- `go test -race ./sdk/integrations/slack/ ./sdk/core/` passes; paste the output into the PR body.
- Mutation: move the empty-`envelope_id` check back above the `disconnect` branch -> test (a) fails.
- Mutation: drop `Timestamp: action.ActionTS` from the callback builder -> test (c) fails.
- CHANGELOG entry under the next patch version.

## Out of scope

Opening the replacement connection before the refresh (a second concurrent connection); the host-side log line (pilot follow-up after the pin bump); message-kind `Timestamp` for `Action: "message"`.

probe: present qf-studio/studio-sdk if env\.EnvelopeID == ""
probe: present qf-studio/studio-sdk ActionTS string
probe: present qf-studio/studio-sdk CallbackID: action\.ActionID
probe: absent qf-studio/studio-sdk Timestamp:\s+action\.ActionTS

### LEG P1 — repo: qf-studio/pilot — title: feat(approval): Slack click log line carries the click's action_ts and the delivery delay on both ingress paths; studio-sdk pin v0.38.4 → v0.38.5

## Context

A Slack approval click reaches `SlackHandler.HandleInteraction` in `internal/approval/slack.go` from two paths: Socket Mode (`internal/adapters/slack/handler.go` passes `ev.CallbackID`, `ev.Data`, `ev.Sender.UserID`, `ev.Sender.DisplayName`, `""`) and the HTTP interactions webhook (`NewSlackInteractionHandler` in `internal/pilot/pilot.go` passes an `InteractionAction` from `internal/adapters/slack/webhook.go`). The GH-5758 line `Slack approval interaction received` logs who clicked and when the daemon received it, but not when the user clicked. Two incidents on the founder box (2026-10-16 PR#5737, 2026-10-31 PR#5990) could not be classified as "late click" or "lost click" for that reason. studio-sdk v0.38.5 (released 2026-11-01) adds `core.MessageEvent.Timestamp`, set from the Slack payload's `actions[0].action_ts` on callbacks; the webhook path already decodes `action_ts` into `InteractionActionDef.ActionTS` but drops it when it builds `InteractionAction`.

## Scope

1. `go.mod`: bump `github.com/qf-studio/studio-sdk` to v0.38.5 (`go mod tidy`; `go.sum` follows).
2. `ApprovalCallbackHandler.HandleInteraction` (`internal/adapters/slack/handler.go`) gains a trailing `actionTS string` parameter. The Socket Mode call site passes `ev.Timestamp`. `InteractionAction` (`internal/adapters/slack/webhook.go`) gains `ActionTS string`, filled from the first action's `action_ts` where the handler builds it; `NewSlackInteractionHandler` passes `action.ActionTS`.
3. `SlackHandler.HandleInteraction` (`internal/approval/slack.go`): the GH-5758 log line gains `action_ts` (the raw string) and `delivery_delay_ms` (receipt time minus `action_ts` parsed as seconds with a fractional part, rounded to ms). When `action_ts` is empty or does not parse, log `action_ts` as given and omit `delivery_delay_ms`; never fail the click on a bad timestamp.
4. Update every implementer and caller of the interface (the `mockApprovalHandler` in `internal/adapters/slack/handler_test.go`, the call in `internal/autopilot/restart_approval_slack_integration_test.go`, and any other).

## Acceptance

- A Socket Mode callback event with `Timestamp` = `1793466563.669229` reaches the log line with `action_ts=1793466563.669229` and a non-negative `delivery_delay_ms`.
- An HTTP interaction payload whose `actions[0].action_ts` = `1793466563.669229` produces the same log attributes.
- A click with an empty `action_ts` logs `action_ts=""` and no `delivery_delay_ms`; a click with `action_ts` = `garbage` logs `action_ts=garbage`, no `delivery_delay_ms`, and is still handled.
- Tests added next to `TestSlackHandler_HandleInteraction_LogsReceivedBeforeLookup` in `internal/approval/slack_interaction_log_test.go` (table-driven over the three timestamp cases) and a webhook test asserting `InteractionAction.ActionTS` is filled from the payload.
- `go test -race ./internal/approval/ ./internal/adapters/slack/ ./internal/pilot/` passes; paste the output into the PR body.
- Mutation: drop the `action_ts` attribute from the log call -> the table test fails on every row.
- Mutation: stop filling `InteractionAction.ActionTS` in the webhook handler -> the webhook test fails.

## Out of scope

Any change to the approval decision path, the Slack card rendering, or the `autopilot_deadlock` alert rule.

probe: present qf-studio/pilot HandleInteraction\(ctx context.Context, actionID, value, userID, username, responseURL string\) bool
probe: present qf-studio/pilot studio-sdk v0\.38\.4
probe: absent qf-studio/pilot delivery_delay_ms
probe: present qf-studio/pilot ActionTS string `json:"action_ts"`

## Held leg P1 — superseded by LEG P1 above (filed 2026-11-01 after v0.38.5)

## S1 run and review (2026-11-01)

- sdk#152 → [PR#153](https://github.com/qf-studio/studio-sdk/pull/153), run 4m51s (15:26→15:31Z, Sonnet 5.5, complexity=complex). CI green 15:32Z. Pre-PR review (shadow) **REQUEST-CHANGES blocking=2 minor=2** → merge gate escalated → `awaiting_approval` 15:33Z → Slack card 15:34Z → founder approved 15:45:36Z.
- Review of the diff (Navigator): spec work correct — `disconnect` branch before the `envelope_id` check, ack only with an id, `MessageEvent.Timestamp` from `action_ts`, `api.golden` updated, `TestSocketModeHandler_Disconnect` (httptest WebSocket, two cases) + `TestBridgeCallbackTimestamp` + `TestMessageHandler_CallbackVariant` extended. **Side damage:** `Makefile` `test` target lost its recipe and `test-changed` is defined twice (first copy runs the suite twice); eight test files had U+200B literals rewritten to `\u200b` (behaviour-neutral, out of scope); CHANGELOG puts the contract change under `### Fixed`.
- **Root cause of the Makefile damage:** the box's global `quality.gates[test]` runs `make test-changed` (`required: true`), a pilot-repo target. The SDK project block has no `quality:` override, so attempt 1 failed in 28 s and the retry "fixed" the Makefile. **Every SDK (and any non-pilot) run hits this.** Operator item: per-project `quality:` override for studio-sdk (`make test`), or a `test-changed` target in the SDK Makefile.
- Founder approved the merge (click 15:45:36Z → merged 15:47:28Z, squash `bfe4021d3`; click→merge 1m52s). Post-merge review (two passes) **REQUEST-CHANGES defects=6** → auto-filed [sdk#154](https://github.com/qf-studio/studio-sdk/issues/154) (`autopilot-fix`) → ran 1m52s → [PR#155](https://github.com/qf-studio/studio-sdk/pull/155) restores the Makefile byte-for-byte (pre-PR APPROVE-WITH-NOTES) → auto-merged 15:55:42Z → post-merge APPROVE-WITH-NOTES, no issue. **S1 is on SDK main, unreleased.** GH-154's gate passed only because a Makefile-only diff runs no packages (GH-5926 scope); the next SDK run with Go changes fails attempt 1 again until the box gets a per-project override. **DONE 2026-11-01 16:00Z (founder consent):** `config.yaml` studio-sdk block gained a `quality:` override (build `make build` · test `make test` required · lint `make lint` optional; backup `config.yaml.bak-sdkgates-20261101`); daemon restarted 16:01:15Z (stop → 0 procs at t+5s → start → 1 proc, 2.306.0, queue 0). `project_override=true` on the next SDK run's `Resolved quality gate config` line is the proof it took. Same gap remains for every other Go project without `test-changed` (auth-service, Navigator, ai-coding-summit) — product-side fallback (absent make target → `make test`) is a candidate pilot leg, unfiled. P1 (pilot pin bump) waits for an SDK tag: `release.trigger: on_schedule 0 16 * * 1-5` → Mon 11-02 16:00 Berlin, or cut by hand.

## P1 run and review (2026-11-01)

- pilot#5999 → [PR#6000](https://github.com/qf-studio/pilot/pull/6000), run 8m29s (16:18→16:27Z, Sonnet 5.5, gates 3m48s, `project_override` n/a — pilot repo). Pre-PR review (shadow) **APPROVE-WITH-NOTES minor=2** → auto-merged 16:38:28Z (squash `1cb89a048`). PR-created → merged 11m32s.
- Review of the diff (Navigator) — **APPROVE-w-notes**: pin v0.38.5 (`go.mod`/`go.sum`); `ApprovalCallbackHandler.HandleInteraction` gains `actionTS`; Socket Mode passes `ev.Timestamp`; `InteractionAction.ActionTS` filled from `actionDef.ActionTS`, `NewSlackInteractionHandler` forwards it; `deliveryDelay(now, actionTS)` parses `seconds.micros`, rejects NaN/Inf/negative, `time.UnixMicro`; the GH-5758 line gains `action_ts` and `delivery_delay_ms` (omitted when unparseable), still emitted before the pending lookup. Tests: `TestSlackHandler_HandleInteraction_LogsActionTS` (valid/empty/garbage), `TestDeliveryDelay` (6 rows), webhook test asserts `ActionTS`, handler test asserts the value reaches the mock; every caller updated. Notes (no issue): (1) the HTTP forwarding line in `internal/pilot/pilot.go` has no test — dropping it passes every test; (2) `delivery_delay_ms` can go negative on clock skew (signed value is informative; the test regex would then fail only on a skewed host).
- Post-merge review 16:40:56Z: **APPROVE-WITH-NOTES defects=4 memories=2**, no follow-up issue. Pre-PR/post-merge agreement on all three runs today (GH-152 RC/RC, GH-154 AWN/AWN, GH-5999 AWN/AWN) → TASK-620 criterion-2 sample now 10 of 20 two-pass runs, RC rate 2/10.
- **Live from the next box upgrade:** every Slack click logs `action_ts` + `delivery_delay_ms`. The 10-16/10-31 class becomes decidable from one log line. Rides the 11-02 16:00 Berlin train or a hand cut.

## Refs
- Marker: `.agent/.context-markers/2026-10-31_task624-merged-task625-dispatched.md` (working note that opened this)
- Memories: `slack-approval-click-not-received-relay-via-local-decision-api`, `box-slack-socket-mode-config-false-but-socket-mode-starts`, `approval-no-handler-returns-success-wall-clock-applies-default-action`
- TASK-620 § L5 (`pilot-review-rc` hold), TASK-576 leg C (#5758 per-click log line)
- Slack docs: docs.slack.dev/apis/events-api/using-socket-mode; block_actions payload reference

- S1 → https://github.com/qf-studio/studio-sdk/issues/152
- P1 → https://github.com/qf-studio/pilot/issues/5999
