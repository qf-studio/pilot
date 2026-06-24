# TASK-370: Unify comms.Handler wiring across all adapters via a shared factory

**Status**: ✅ Shipped — PR [#3646](https://github.com/qf-studio/pilot/pull/3646) merged (CI green)
**Created**: 2026-06-24
**Assignee**: Pilot

---

## Context

**Problem**:
Intent classification (task / chat / question / research / planning / greeting) is
supposed to be **identical across every I/O adapter** — Telegram, Slack, Discord,
and any future adapter — because they all share the single `internal/comms.Handler`
engine. In practice the behavior diverges, because each adapter hand-builds its
`comms.HandlerConfig` with **inline copy-pasted code** and there is **no shared
factory**. Fields get silently forgotten per adapter.

Observed live (2026-06-24, Slack): `@Pilot check the pilot queue` was classified as
a **code task** (and had to be cancelled), while `what is in the queue?` was
classified as a question. Root cause: Slack runs **pure regex** intent detection —
the LLM classifier is never wired into Slack's handler, so action words like
"check" default to `IntentTask`.

**Divergence matrix (5 construction sites, all different):**

| Site | LLMClassifier | ConvStore | RateLimit | MemberResolver | Store |
|------|:---:|:---:|:---:|:---:|:---:|
| Telegram `cmd/pilot/main.go:1906` | ✅ | ✅ | ✅ | ✅ | ✅ |
| Slack `cmd/pilot/main.go:2629` | ❌ | ❌ | ❌ | ✅ | ✅ |
| Discord `cmd/pilot/poller_discord.go:78` | ✅ | ✅ | ❌ | ❌ | ❌ |
| Telegram gateway `internal/pilot/pilot.go:607` | ❌ | ❌ | ✅ | ✅ | ✅ |
| Slack gateway `internal/pilot/pilot.go:653` | ❌ | ❌ | ❌ | ✅ | ✅ |

Supporting debt: the classifier bootstrap block (`NewAnthropicClient` + model/URL
config + `NewConversationStore`) is copy-pasted between the Telegram and Discord
sites; `LLMClassifierConfig` is triplicated (telegram, discord, and a dead stub in
`slack/handler.go`); `slack.Config` has **no** `LLMClassifier` field at all.

**Goal**:
Uniform intent behavior **by construction**: one shared factory assembles the
`comms.Handler` for every adapter so a field cannot be silently omitted. Slack and
Discord and gateway-mode reach feature parity with Telegram. No config-schema
migration (backward compatible).

---

## Acceptance Criteria

- [ ] A single factory (e.g. `buildCommsHandlerConfig(deps) *comms.HandlerConfig`)
      is the **only** place `comms.HandlerConfig` is assembled. All 5 call sites
      route through it.
- [ ] The classifier bootstrap (`intent.NewAnthropicClient` + model/URL +
      `intent.NewConversationStore`) lives in exactly **one** place (consumed by the
      factory), not copy-pasted per adapter.
- [ ] `slack.Config` gains an `LLMClassifier` field so Slack is configurable; the
      dead `LLMClassifierConfig` stub in `slack/handler.go` is removed.
- [ ] After the change, Telegram / Slack / Discord (main + gateway) all wire
      `LLMClassifier`, `ConvStore`, `RateLimit`, `MemberResolver`, and `Store`
      consistently (each may be nil only when genuinely unavailable, never by
      accidental omission).
- [ ] Slack classifies `check the pilot queue` as a non-task intent when the LLM
      classifier is enabled (regression for the reported bug).
- [ ] Backward compatible: with no `llm_classifier` configured, every adapter
      behaves exactly as before (regex fallback). No breaking config change.
- [ ] `configs/pilot.example.yaml` documents `llm_classifier` under `slack:` and
      `discord:` (currently only under `telegram:`).

---

## Implementation

### Phase 1: Adapter-agnostic intent config + classifier bootstrap helper
**Goal**: One classifier-construction path, one config shape.

**Tasks**:
- [ ] Introduce an adapter-agnostic intent/classifier config value (fields:
      `Enabled`, `APIKey`, `HistorySize`, `HistoryTTL`) that each adapter config maps
      into. Collapse the triplicated `LLMClassifierConfig` usage onto it (keep
      per-adapter YAML fields for back-compat; map them to the agnostic struct).
- [ ] Extract the classifier bootstrap into one helper that returns
      `(intent.Classifier, *intent.ConversationStore)` from the agnostic config +
      executor model/URL settings.

**Files**:
- `internal/comms/` (or `cmd/pilot/`) — factory + bootstrap helper.
- `internal/adapters/slack/notifier.go` — add `LLMClassifier` to `slack.Config`.
- `internal/adapters/slack/handler.go` — remove the dead `LLMClassifierConfig` stub.

### Phase 2: Shared HandlerConfig factory
**Goal**: One assembly point for every adapter.

**Tasks**:
- [ ] Add `buildCommsHandlerConfig(deps)` taking adapter name, agnostic intent
      config, runner, messenger, rate-limit, member-resolver, store, project source,
      task-id prefix. It bootstraps the classifier and returns a fully populated
      `*comms.HandlerConfig`.
- [ ] Route all 5 call sites through it:
      `main.go:1906` (TG), `main.go:2629` (Slack), `poller_discord.go:78` (Discord),
      `pilot.go:607` (TG gateway), `pilot.go:653` (Slack gateway).
- [ ] Wire Discord's missing `RateLimit` / `MemberResolver` / `Store`; wire
      gateway-mode classifiers. (If `discord.RateLimitConfig` units differ from
      `comms.RateLimitConfig`, add a small conversion — see Out of Scope note.)

**Files**:
- `cmd/pilot/main.go`, `cmd/pilot/poller_discord.go`, `internal/pilot/pilot.go`.

### Phase 3: Config docs + tests
**Tasks**:
- [ ] `configs/pilot.example.yaml`: add `llm_classifier` blocks under `slack:` and
      `discord:`.
- [ ] Test: factory produces a HandlerConfig with all expected fields populated for
      each adapter (table-driven over adapter name).
- [ ] Test: with classifier enabled, Slack handler resolves `check the pilot queue`
      to a non-task intent (or at least routes through the LLM classifier path).
- [ ] Test: classifier disabled → regex fallback unchanged (back-compat).

**Files**:
- `configs/pilot.example.yaml`, factory test file, `internal/comms/handler_test.go`.

---

## Out of Scope

- **Operational/meta intent** ("what's in the queue / status / what are you working
  on" answering from the **live daemon queue** instead of scanning a repo's
  `.agent/tasks/*.md`). Separate follow-up — this task only makes *classification*
  uniform, not the *answer source*.
- Option B (single top-level `intent:` config + one shared classifier instance) —
  deferred; it's a config-schema migration. This task is the factory (Option A),
  which is backward compatible and a prerequisite that makes B trivial later.
- Changing `comms.Handler` internals or `detectIntent` logic — the engine is correct;
  only the wiring changes.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| How to enforce uniformity | C: patch each site; A: shared factory; B: top-level intent config | **A: factory** | Uniform *by construction* — a field can't be forgotten because there's one assembly path. Backward compatible, no config migration. C perpetuates the copy-paste that caused the drift; B is a breaking config change (do later). |
| Classifier instance sharing | one shared instance; per-adapter instances | per-adapter via factory | `intent.AnthropicClient` is adapter-agnostic so sharing is safe, but per-adapter `ConvStore` keeps conversation TTL/memory cleanly scoped. Factory hides the choice. |

---

## Verify

```bash
make build
go test ./internal/comms/... ./internal/intent/... -v
go test ./...
make lint
# manual: enable adapters.slack.llm_classifier, restart daemon (USER runs it),
#   send "@Pilot check the pilot queue" in Slack → must NOT become a code task.
```

---

## Done

- [ ] Single `buildCommsHandlerConfig` factory is the only HandlerConfig assembly point.
- [ ] All 5 call sites route through it; classifier bootstrap exists once.
- [ ] `slack.Config.LLMClassifier` added; dead stub removed.
- [ ] `pilot.example.yaml` documents slack + discord `llm_classifier`.
- [ ] Tests green (`go test ./...`), `make lint` clean.
- [ ] Manual: Slack no longer misclassifies `check the pilot queue` as a task.

---

## Refs

- Architecture research 2026-06-24 (navigator-research): shared `comms.Handler`,
  5 divergent inline call sites, no factory, triplicated `LLMClassifierConfig`.
- Shared engine: `internal/comms/handler.go` (`HandlerConfig` L28, `detectIntent` L194,
  LLM gate L213).
- Adapter-agnostic classifier: `internal/intent/classifier.go`, `internal/intent/conversation.go`.
- Related: [TASK-369](TASK-369-readonly-intents-ghost-sha-guard.md) (same root cause
  family — inconsistent comms.Handler wiring; shipped v2.192.1).

---

**Last Updated**: 2026-06-24
