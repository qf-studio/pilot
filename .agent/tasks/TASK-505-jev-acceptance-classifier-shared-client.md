# TASK-505: feat(executor): optional Jev (TypeSafe) classifier behind the acceptance-evidence regex floor — shared client, shadow mode, per-gate merge contract

**Status**: 📋 Planned (amends issue #5509; dispatch on stream after body sync)
**Created**: 2026-09-30
**Assignee**: Pilot

---

## Context

**Problem**:
The acceptance-evidence gate classifies each checklist bullet with regexes in `internal/executor/acceptance_evidence.go` (ClassifyAcceptanceItem, line 155; extractMutation, line 270). Every classifier gap so far surfaced in production first and was fixed as one more table row: #5438, #5479, #5486, #5506 (PR #5510), #5513, #5514 (PR #5515). The question the classifier answers ("does this bullet ask for pasted command output, assert that a named test fails after a described change, or neither?") is a bounded semantic judgment with three options, which is what a System One model answers in one call.

Issue #5509 already decided the shape (founder, 2026-09-30): regex stays as the floor, offline path and fallback; Jev (TypeSafe, model `jev-latest`) is an optional second classifier, on only when a key is present; the gate must never get slower or less reliable because of the model. Research on 2026-09-30 (nav-research, two passes) found three conflicts between the #5509 body and the code, and a reuse requirement the body did not anticipate: four more gates rank as Jev candidates (base-presence path token `dependency_detector.go:169`, CI failure class `autopilot/failure_class.go:80`, criteria extraction `acceptance/extract.go`, child-dependency shape `dependency_detector.go:56`). The second of those lives in `internal/autopilot`, which cannot import `internal/executor`. An executor-private client would be rewritten on the second gate.

**Goal**:
One shared TypeSafe client package, one shared merge contract with named reasons, one config block for the connection, and the acceptance-item kind as the first gate on top of it. Shadow mode ships on by default for the gate so the first days produce agree/override counts, not behaviour changes.

Verified API contract (docs.typesafe.ai/api, 2026-09-30): `POST https://api.typesafe.ai/v1/systemone`, `Authorization: Bearer`, body `{state, model, questions}`. A `choice` question is `{type:"choice", instructions, criteria:{option: rubric|null}}` (max 255 options). The answer is `{type, choice, probabilities, confidence}` where `confidence` is server-computed from the spread (three options: about (3·pmax−1)/2). Response also carries `model` and `usage{input_tokens,output_tokens}`. Errors: 401, 422, 429, 529.

---

## Known Pitfalls & Patterns

- **PITFALL** (98%, mem-160): a top-level YAML block that no struct binds to is silently dead (autopilot block, GH-5251). Phase 2 wires `typesafe:` into `config.Load` and pins with a test that a top-level block reaches the runner config.
- **LEARNING** (95%, mem-179): tests that depend on host state (`~/.claude` cache, here `~/.config/typesafe/api_key`) are green on bare CI and fail on production-like machines. Key resolution is env-only; no file fallback; tests use `t.Setenv`.
- **LEARNING** (95%, mem-035): stub the side-effecting boundary. No test in this task performs a network call; the client is exercised against `httptest.NewServer`, the runner against a fake classifier, and a test pins that the default configuration never constructs the client.
- **LEARNING** (95%, mem-203): Pilot implements the Acceptance list literally and copies issue-body claims verbatim. Every required file is named below; nothing is asserted that was not verified against the code on 2026-09-30.
- **PATTERN** (#5486): mutation shape wins over paste-output in the regex. The merge keeps that precedence: a Jev `mutation` verdict without a selectable test-name token degrades to the regex verdict, never to `paste_output`.
- **PITFALL** (evidence gate, SOP Rule 5b): evidence commands must not contain shell operators, even quoted. The Verify commands below use `-run Acceptance`, not `-run 'A|B'`.
- **LEARNING** (Navigator v7.7.1 live read): over four days and 92 judged prompts the typed judge overrode the keyword heuristic 4 times, all on one axis. Expect the value in the tails; shadow counters decide the flip, not intuition.

---

## Acceptance Criteria

- [ ] `go test ./internal/typesafe/ -count=1` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run Acceptance -count=1` passes; paste the output into the PR body.
- [ ] `go vet ./internal/typesafe/ ./internal/executor/ ./internal/config/` passes.
- [ ] New package `internal/typesafe` exports `Config`, `Client`, `NewClient`, `Question`, `ChoiceQuestion`, `Answer`, `Answers`, `Ask`, `Resolve`, `Reason` and the reason constants `ReasonRegexOnly`, `ReasonAgreed`, `ReasonOverrode`, `ReasonLowConfidence`, `ReasonShadow`, `ReasonError`.
- [ ] Client test in `internal/typesafe/client_test.go` against `httptest.NewServer`: the request carries `Authorization: Bearer` with the configured key, `Content-Type: application/json`, `model` equal to the configured model, and one entry under `questions` per supplied question; a server that sleeps past the configured timeout yields an error; a 401, 422, 429 and 529 each yield an error whose message contains the status code and never the key; a 200 with a malformed body yields an error.
- [ ] Config tests in `internal/config/config_test.go`: a top-level `typesafe:` block with `model` and `timeout` reaches `cfg.Executor.TypeSafe`; an absent block yields nil and no error; `timeout` unset or unparsable resolves to 5s through `EffectiveTimeout`; `model` unset resolves to `jev-latest`.
- [ ] Config tests in `internal/executor/backend_test.go`: `AcceptanceEvidenceConfig.Classifier` defaults resolve to provider `regex`, `min_confidence` 0.8, `shadow` true; provider `jev` with `TYPESAFE_API_KEY` unset (use `t.Setenv` with an empty value) resolves to regex behaviour and `EffectiveClassifierProvider` returns `regex`; `min_confidence` outside 0..1 resolves to 0.8.
- [ ] Merge tests in `internal/executor/acceptance_classifier_jev_test.go`, table-driven over `Resolve` and the batch classifier with a fake `Asker`: a Jev kind at confidence 0.9 overrides a differing regex kind with `ReasonOverrode`; at 0.5 the regex kind stays with `ReasonLowConfidence`; equal kinds yield `ReasonAgreed`; shadow true keeps the regex kind on every item and records the reason the non-shadow path would have produced; an `Ask` error keeps the regex kind on every item with `ReasonError`; a Jev `mutation` verdict whose target answer is `none` or below `min_confidence` keeps the regex kind; a Jev `mutation` verdict with a selected token sets `MutationTarget` to that token byte-for-byte.
- [ ] Delete the error branch in the batch classifier that returns the regex items when `Ask` fails -> TestJevClassifier_AskErrorKeepsRegex fails.
- [ ] Delete the `confidence < min` comparison in `Resolve` -> TestResolve_LowConfidenceKeepsRegex fails.
- [ ] New test TestAcceptanceClassifier_DefaultConfigNeverBuildsClient in `internal/executor/acceptance_classifier_jev_test.go`: with `DefaultBackendConfig()` and `TYPESAFE_API_KEY` set to a fake value via `t.Setenv`, the classifier factory returns the regex classifier and a counter on the client constructor hook stays 0.
- [ ] Existing tests in `internal/executor/acceptance_evidence_classify_test.go` and `internal/executor/acceptance_evidence_run_test.go` pass with no edits to their tables; `ClassifyAcceptanceItem`, `ParseAcceptanceItems` and `RunAcceptanceEvidence` keep their signatures.
- [ ] `internal/executor/acceptance_evidence_run.go` logs one info line per task through the runner logger with fields `task_id`, `classifier`, `items`, `regex_only`, `agreed`, `overrode`, `low_confidence`, `errors`, `shadow`, `latency_ms`; a test with a captured `slog.Handler` asserts the field names.
- [ ] The state sent to the API contains only the checklist item texts after `redactSecrets` (new helper in `internal/typesafe/redact.go`, patterns copied from Navigator's judge: key=value shapes, `sk-`, `ghp_`, `AKIA`, `xox`, JWT, 40-hex) with each item capped at 600 characters; a test asserts a bullet containing `token=abc123secret` reaches the fake server as `[redacted]`.
- [ ] `configs/pilot.example.yaml` gains a commented top-level `typesafe:` block and a commented `classifier:` sub-block under `acceptance_evidence`, both stating that the key comes from `TYPESAFE_API_KEY` only and that only checklist text leaves the machine.
- [ ] The acceptance-evidence section of the configuration page under `docs/content` (search for `acceptance_evidence`) gains one paragraph on regex floor, optional Jev, shadow mode and the env var.

---

## Implementation

### Phase 1: Shared client package `internal/typesafe`
**Goal**: one stdlib HTTP client any package can import; no SDK; no retries.

**Tasks**:
- [ ] `config.go`: `Config{Endpoint, Model, Timeout string}` with `EffectiveEndpoint` (default `https://api.typesafe.ai/v1/systemone`), `EffectiveModel` (default `jev-latest`), `EffectiveTimeout` (default 5s, invalid falls back). `APIKeyFromEnv()` reads `TYPESAFE_API_KEY` only and returns the key and a source label `env:TYPESAFE_API_KEY` or empty; the label is what gets logged, never the key.
- [ ] `client.go`: `Client{apiKey, model, endpoint, timeout, httpClient, log *slog.Logger}`; `NewClient(cfg Config, key string, log) *Client`; `Ask(ctx, state any, questions map[string]Question) (Answers, error)`. Per-call `context.WithTimeout`. `NewRequestWithContext`, headers `Authorization`, `Content-Type`, `User-Agent: pilot-typesafe/1`. Non-2xx returns an error with the status code and a body excerpt capped at 200 bytes. Template: `internal/llm/client.go:28-110` and `internal/executor/effort_classifier.go:228-299`.
- [ ] `types.go`: `Question{Type string; Instructions any; Criteria any}` with constructor `ChoiceQuestion(instructions string, criteria map[string]any)`; `Answer{Type, Choice string; Probabilities map[string]float64; Confidence float64; Noul float64}`; `Answers{Model string; Answers map[string]Answer; Usage struct{InputTokens, OutputTokens int}}`. `var _ Asker = (*Client)(nil)` with `type Asker interface{ Ask(...) }` so gates depend on the interface.
- [ ] `resolve.go`: `Resolve(regexVerdict string, a *Answer, minConfidence float64, shadow bool) (verdict string, reason Reason)`. Rules in order: nil answer -> regex, ReasonError; confidence below min -> regex, ReasonLowConfidence; equal -> regex, ReasonAgreed; shadow -> regex, ReasonShadow (the reason the non-shadow path would have produced is returned through a second helper `ResolveShadowReason` for counting); else -> a.Choice, ReasonOverrode.
- [ ] `redact.go`: `redactSecrets(string) string` and `capItem(string, n int) string`.

**Files**:
- `internal/typesafe/config.go`, `client.go`, `types.go`, `resolve.go`, `redact.go` and `_test.go` for each.

### Phase 2: Config wiring
**Goal**: one connection block, one per-gate block, nothing dead (mem-160).

**Tasks**:
- [ ] `internal/config/config.go`: add `TypeSafe *typesafe.Config` with yaml `typesafe` to `Config`; in `Load`, after defaults, copy the pointer into `cfg.Executor.TypeSafe` so the runner reads one place. Pin with a config test.
- [ ] `internal/executor/backend.go`: add `TypeSafe *typesafe.Config` to `BackendConfig` (yaml `typesafe`, populated by Load, documented as such); extend `AcceptanceEvidenceConfig` (line 811) with `Classifier *AcceptanceClassifierConfig` (yaml `classifier`) holding `Provider string`, `MinConfidence *float64`, `Shadow *bool`. Accessors in the existing `Effective*` style: `EffectiveClassifierProvider()` returns `regex` unless provider is `jev` and `typesafe.APIKeyFromEnv()` is non-empty; `EffectiveMinConfidence()` default 0.8, out-of-range falls back; `EffectiveShadow()` default true.
- [ ] Startup: where the runner is constructed (`NewRunnerWithConfig`, runner.go:1300), when provider is `jev` and the key is empty log one warning naming the env var; when the key resolves log one info line with `auth_source` (idiom: `effort_classifier.go:98`).
- [ ] `configs/pilot.example.yaml`: commented `typesafe:` block near the top-level sections; commented `classifier:` block inside the existing `acceptance_evidence` comment block (lines 686-699).

**Files**:
- `internal/config/config.go`, `internal/config/config_test.go`
- `internal/executor/backend.go`, `internal/executor/backend_test.go`
- `configs/pilot.example.yaml`

### Phase 3: Batch classifier and runner seam
**Goal**: Jev sets `Kind` and `MutationTarget` only; the runner, allowlist, confinement and renderer are untouched.

**Tasks**:
- [ ] `internal/executor/acceptance_classifier.go`: `type AcceptanceClassifier interface{ Classify(ctx, criteria []string) ([]AcceptanceItem, AcceptanceClassifyStats) }`; `regexAcceptanceClassifier` wraps `ParseAcceptanceItems` and reports every item as `ReasonRegexOnly`; `newAcceptanceClassifier(cfg *BackendConfig, log) AcceptanceClassifier` returns regex unless `EffectiveClassifierProvider()` is `jev`, in which case it builds the client through a package-level constructor variable `newTypeSafeClient` (tests swap it to count calls).
- [ ] `internal/executor/acceptance_classifier_jev.go`: `jevAcceptanceClassifier{asker typesafe.Asker; minConfidence; shadow; log}`. `Classify`: run `ParseAcceptanceItems` first (today's result); build state `{"items": {"1": text, ...}}` after redaction and capping; for each item a `kind_<n>` choice question with options `mutation`, `paste_output`, `other` and the rubric from `.stream/jev-classify-demo.sh` (mutation: names a change and asserts a named test FAILS as a result; items saying a test must NOT fail are not this; paste_output: run a command and show its output, or names a runnable command that must pass; other: everything else); for each item whose text contains at least one Go test identifier (`\bTest[A-Z]\w*`), a `target_<n>` choice question whose options are those identifiers with `null` rubrics plus `none` ("None of these is the test that must fail"). One `Ask` per task. Merge per item with `typesafe.Resolve`; when the resolved kind is `mutation`, take `MutationTarget` from `target_<n>` only if its confidence is at or above `min_confidence` and its choice is not `none`, else keep the regex item whole (mutation precedence, #5486). When the resolved kind is `paste_output` and the regex item had no `Commands`, keep the regex item whole (Jev never invents a command). On `Ask` error: return the regex items, stats `errors=1`, one warn log with the error class and never the key.
- [ ] `internal/executor/acceptance_evidence_run.go`: add `RunAcceptanceEvidenceItems(ctx, items []AcceptanceItem, ...)` holding today's body from line 573 onward; `RunAcceptanceEvidence` becomes a wrapper that calls `ParseAcceptanceItems` then the new function, so its callers and tests stay unchanged. `appendAcceptanceEvidence` (line 450) calls `r.acceptanceClassifier.Classify` then `RunAcceptanceEvidenceItems`, and emits the per-task info line from the stats.
- [ ] `internal/executor/runner.go`: field `acceptanceClassifier AcceptanceClassifier` beside `acceptanceRunner` (line 1237), set in `NewRunnerWithConfig`; nil means regex.

**Files**:
- `internal/executor/acceptance_classifier.go`, `acceptance_classifier_jev.go`, `acceptance_classifier_jev_test.go`
- `internal/executor/acceptance_evidence_run.go`, `internal/executor/runner.go`

### Phase 4: Docs
**Tasks**:
- [ ] One paragraph in the acceptance-evidence section of the configuration page under `docs/content`: regex floor, optional Jev, shadow default, `TYPESAFE_API_KEY`, only checklist text leaves the machine, per-task log line as the flip signal.

---

## Out of Scope

- The other four Jev gates (base-presence path token, CI failure class, criteria extraction, child-dependency shape). Each is its own task on top of `internal/typesafe`.
- Console-side key delivery for hosted tenants (fleet-supplied `TYPESAFE_API_KEY`). Filed in the console repo once this merges and the env var name is on main.
- Retries and backoff on 429/529. One call, fail open to regex; the docs' backoff advice does not apply when a floor exists.
- File-based key fallback (`~/.config/typesafe/api_key`). Laptop convenience only; the daemon runs on the box with env.
- Making freeform mutations runnable. `parseLineMutation` stays limited to "delete/remove line N in FILE"; Jev changes what a bullet is, not what the runner can execute.
- Any change to `validateEvidenceCommand`, the allowlist, `resolveWorktreeConfinedPath`, or `RenderAcceptanceEvidenceSections`.
- Noul and score question types in the client beyond the struct fields (no gate uses them yet).

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Client location | file in `internal/executor`; new `internal/typesafe`; `internal/llm` | `internal/typesafe` | second gate is in `internal/autopilot`, which cannot import executor; `internal/llm` is the Anthropic-shaped client |
| Connection config | per-gate block (as #5509 wrote); top-level `typesafe:` copied into executor config | top-level, copied in `Load` | five gates would carry five copies of endpoint/model/timeout; mem-160 says the copy must be explicit and pinned |
| Key resolution | `api_key_env` knob; fixed env name; env then file | fixed `TYPESAFE_API_KEY` | no `api_key_env` idiom exists in the repo (effort_classifier.go:93); file fallback is host state (mem-179) |
| Shadow default | off (#5509); on | on | founder wants counts before behaviour; Navigator's judge showed overrides only in the tails |
| Seam | change `RunAcceptanceEvidence` signature; pre-classified `RunAcceptanceEvidenceItems` plus wrapper | wrapper | keeps every existing run test valid |
| Test-name source | model writes the name; model picks among regex-found tokens | picks, with `none` | pre-parsed value extraction pattern; the target reaches a shell command through `buildMutationTestCommand`, so it must be a span from the issue text |
| Fail-open scope | per item; whole task | whole task on transport error, per item on confidence | one request carries the whole checklist, so a transport error has no per-item information |
| Merge contract | inline in the gate; shared `Resolve` with `Reason` | shared | every later gate logs the same six reasons, so the flip decision reads the same for all of them |

---

## Verify

```bash
go test ./internal/typesafe/ -count=1
go test -race ./internal/executor/ -run Acceptance -count=1
go test ./internal/config/ -run TypeSafe -count=1
go vet ./internal/typesafe/ ./internal/executor/ ./internal/config/
```

---

## Done

- [ ] `internal/typesafe` exists with the exported names listed in Acceptance and its own tests.
- [ ] With no `typesafe:` block and no env var, every acceptance test passes unchanged and no client is constructed.
- [ ] With provider `jev`, a key, and shadow on, the box logs one line per task with agree/override counts and the PR body is byte-identical to the regex-only body.
- [ ] Removing either fallback branch fails exactly the named test.
- [ ] Docs and example config name the env var and the privacy boundary.

---

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/5509 (body to be replaced by this document before the `pilot` label is added)
- Research 2026-09-30: nav-research passes on the classifier seam and on the gate inventory (this session); ranked gates recorded in the session marker.
- TypeSafe docs: https://docs.typesafe.ai/api.md · https://docs.typesafe.ai/primitives/choice.md · https://docs.typesafe.ai/confidence.md · https://docs.typesafe.ai/cookbooks/pre_parsed_value_extraction_cookbook.md
- Reference implementation: Navigator plugin `hooks/nav_hook_lib/judge.py` (v7.7.0, TASK-79/80): env-then-file key, redaction, fuse, fail-open, opener injection.
- Prior classifier work: TASK-503 (#5514 / PR #5515), TASK-504 (#5513 / PR #5510), #5435, #5438, #5479, #5486, #5488, #5493, #5506.
- Demo: `.stream/jev-classify-demo.sh` (gitignored) — rubric source; results b4 paste_output 1.00, b5 mutation 1.00, b2 other 0.42 vs mutation 0.39.

---

**Last Updated**: 2026-09-30
