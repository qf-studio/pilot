# TASK-24: Non-Anthropic Model Provider Support

**Status**: 🚧 In Progress
**Created**: 2026-04-12
**Assignee**: Pilot

---

## Context

**Problem**: Pilot hardcodes Anthropic model names (`claude-haiku-4-5-20251001`, `claude-sonnet-4-6`, `claude-opus-4-6`) in 12+ locations. When Claude Code CLI is configured with `ANTHROPIC_MODEL=glm-5.1` and `ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic` (Z.AI), Pilot overrides this by passing `--model claude-haiku-4-5-20251001` etc., which Z.AI rejects. Additionally, components that call the Anthropic Messages API directly hardcode `https://api.anthropic.com/v1/messages`.

**Goal**: Add `default_model` and `api_base_url` config fields to `BackendConfig` that override ALL model name and API URL references. When set with `claude-code` backend, main execution skips `--model` entirely (lets CC use its own `settings.json`).

**Success Criteria**:
- [ ] Pilot executes tasks on GLM 5.1 via Z.AI without errors
- [ ] No `--model` flag passed to Claude Code CLI for main execution when `default_model` set
- [ ] All internal LLM calls (classifiers, judges, parsers) use `default_model` + `api_base_url`
- [ ] Existing Anthropic configs continue to work (backward compatible)
- [ ] Tests pass, build succeeds

---

## Implementation Plan

### Phase 1: Foundation — BackendConfig fields + helpers
**Goal**: Add `DefaultModel` and `APIBaseURL` fields with resolution helpers.

**Tasks**:
- [ ] Add `DefaultModel string` and `APIBaseURL string` fields to `BackendConfig` struct
- [ ] Add `ResolveModel(explicit string) string` method
- [ ] Add `ResolveAPIBaseURL() string` method
- [ ] Add unit tests for helpers

**Files**:
- `internal/executor/backend.go` — Add fields and methods
- `internal/executor/backend_test.go` — Add `TestResolveModel`, `TestResolveAPIBaseURL`

### Phase 2: Make internal components configurable
**Goal**: Replace hardcoded models/URLs in classifiers, judges, parsers, release summary.

**Tasks**:
- [ ] `effort_classifier.go` — Add `apiURL` field, use in direct API mode
- [ ] `complexity_classifier.go` — No struct change, model already settable
- [ ] `intent_judge.go` — Already has `apiURL`/`model` fields (no change)
- [ ] `subtask_parser.go` — Already has `baseURL`/`model` fields (no change)
- [ ] `parallel.go` — Add `defaultModel` field + `SetDefaultModel()` method
- [ ] `release_summary.go` — Add `model`/`apiURL` fields + setters
- [ ] `intent/classifier.go` — Add `apiURL` field + `SetModel()`/`SetAPIURL()` methods
- [ ] `backend_anthropic.go` — Use `ResolveModel()` for default model

**Files**:
- `internal/executor/effort_classifier.go`
- `internal/executor/parallel.go`
- `internal/autopilot/release_summary.go`
- `internal/intent/classifier.go`
- `internal/executor/backend_anthropic.go`

### Phase 3: Wire overrides in runner + main
**Goal**: Thread `DefaultModel`/`APIBaseURL` from config to all components.

**Tasks**:
- [ ] `runner.go` — Wire 8 override sites (effort classifier, complexity classifier, subtask parser, intent judge, parallel runner, main execution model routing, post-exec summary)
- [ ] `main.go` — Wire intent classifier overrides for Telegram
- [ ] `poller_discord.go` — Wire intent classifier overrides for Discord

**Files**:
- `internal/executor/runner.go` — 8 change sites
- `cmd/pilot/main.go` — Intent classifier wiring
- `cmd/pilot/poller_discord.go` — Intent classifier wiring

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Override mechanism | Per-component config, global fields, env var detection | Two global fields on BackendConfig | Single source of truth, minimal config surface, backward compatible |
| CC main execution | Pass `default_model` as `--model`, skip `--model` entirely | Skip `--model` | CC already knows its model from settings.json; passing --model would override with a name the provider may not recognize |
| Direct API callers | Add per-component URL config, global override | Global `api_base_url` on BackendConfig | Less config duplication, all internal API calls use same endpoint |

---

## Config YAML

```yaml
executor:
  type: claude-code
  default_model: "glm-5.1"                          # Override all model references
  api_base_url: "https://api.z.ai/api/anthropic"    # Override all direct API URLs
  # When default_model is set:
  #   - model_routing still determines timeouts/effort but not model name
  #   - classifiers, judges, parsers use glm-5.1
  #   - main execution does NOT pass --model (CC uses its own settings)
```

---

## Hardcoded Locations (all fixed)

| # | File | Line | Model/URL | Component |
|---|------|------|-----------|-----------|
| 1 | `runner.go` | ~4007 | `claude-haiku-4-5-20251001` | Post-exec summary |
| 2 | `parallel.go` | 241-245 | `haiku`/`sonnet`/`opus` | Subagent research |
| 3 | `complexity_classifier.go` | 60 | `claude-haiku-4-5-20251001` | Complexity classifier |
| 4 | `effort_classifier.go` | 77 | `claude-haiku-4-5-20251001` | Effort classifier |
| 5 | `effort_classifier.go` | 231 | `https://api.anthropic.com/v1/messages` | Direct API URL |
| 6 | `intent_judge.go` | 40 | `claude-haiku-4-5-20251001` + hardcoded URL | Intent judge |
| 7 | `haiku_parser.go` | 27 | `claude-haiku-4-5-20251001` + hardcoded URL | Subtask parser |
| 8 | `subtask_parser.go` | 39 | `claude-haiku-4-5-20251001` + hardcoded URL | Subtask parser (v2) |
| 9 | `backend_anthropic.go` | ~515 | `claude-opus-4-6` | Direct API backend default |
| 10 | `release_summary.go` | 18,30 | model + URL constants | Release notes |
| 11 | `intent/classifier.go` | 43,106 | model + hardcoded URL | Telegram/Discord classifier |
| 12 | `backend.go` | 575-593 | Default model routing config | Default config |

---

## Dependencies

**Requires**:
- None

**Blocks**:
- GLM 5.1 bench testing
- Z.AI provider support in production

---

## Verify

```bash
make build
make test
# Manual: configure default_model in config.yaml, run pilot task, check logs
```

---

## Done

- [ ] `BackendConfig.DefaultModel` and `APIBaseURL` fields exist with helpers
- [ ] All 12 hardcoded locations respect `default_model`/`api_base_url`
- [ ] Main execution skips `--model` when `default_model` set with claude-code backend
- [ ] `make build` succeeds
- [ ] `make test` passes
- [ ] Pilot executes a task with GLM 5.1 via Z.AI without model errors

---

**Last Updated**: 2026-04-12
