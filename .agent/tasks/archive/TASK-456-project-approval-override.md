# feat(autopilot): ProjectApprovalOverride — per-project require_approval + approval_source

## Problem

`RequireApproval` and `ApprovalSource` resolve globally (per-environment) for every controller (`internal/autopilot/types.go:46-65`, read sites `controller.go:2163` and `:2787`), while controllers are already **per-repo** (`autopilotControllers` map, `cmd/pilot/main.go:1907-2065`) and each knows its repo + project path. The operator needs per-project resolution: a personal project routes approval asks to Telegram, a work project to Slack, and gating strictness set per project. This is config plumbing following an established pattern, not architecture.

## Context (verified 2026-08-06, origin/main)

- **Copy-pattern exists 3×**: `ProjectCIChecksOverride` (`internal/autopilot/types.go:281-308`), `ProjectReleaseConfig`, per-project quality. The 4-part shape: `ProjectConfig` field (`internal/config/config.go:246-284`) → override struct in `autopilot/types.go` → `WithXOverride` ControllerOption (`controller.go:347-360` idiom) → resolution in `NewController`.
- **Critical caveat**: unlike CIChecks (consumed once at construction), BOTH approval reads are per-tick — store resolved values on the controller (the `resolvedReleaseCfg` idiom, `controller.go:477-481`); do NOT rely on a construction-time shallow cfg copy, and never mutate the shared `cfg.Orchestrator.Autopilot` (hazard documented at `controller.go:699-716`).
- Resolution order: project override > env (`EffectiveApprovalSource`, `types.go:416-427`) > global. Empty/nil fields inherit (all-pointer struct, `ProjectReleaseConfig` style).
- **Escalation-gate interplay verified**: size-floor/scope-drift escalations funnel into the same `submitAsyncApprovalRequest` (`controller.go:2790`) where `PreferredChannel` is built — once that reads the resolved per-project source, gate-triggered asks route per-project with zero extra work.
- **Wire at ALL THREE construction sites**: default repo `cmd/pilot/main.go:1983-1991`, projects loop `:2013-2051`, AND the gateway-mode single-controller path `:835` (which today skips even the CIChecks overlay — fix the approval wiring only; the CIChecks asymmetry is a separately filed backlog item, do not fold it in).
- Validation slot: `internal/config/config.go:996-1010` (index-addressed, `projects[%d].approval...`). Example YAML slot: `configs/pilot.example.yaml:491-528` next to the `release:`/`quality:` overlay examples.
- Health checks (`internal/health/health.go:923-942`): extend the require_approval/pre_merge deadlock check to iterate project overrides; ADD a check that each effective `approval_source` maps to a handler the current mode registers (webhook mode registers only Slack — `internal/pilot/pilot.go:248-269` — this check turns that per-tick hard error into a boot-time diagnostic).

## Acceptance

1. `ProjectApprovalOverride { RequireApproval *bool; ApprovalSource *ApprovalSource }` (yaml `require_approval`, `approval_source`) on `ProjectConfig` as `approval`; nil/omitted fields inherit env/global.
2. `WithApprovalOverride` ControllerOption; resolved once in `NewController` into controller fields; read sites `controller.go:2163` + `:2787` switch to the resolved values.
3. Wired at all three construction sites incl. gateway mode.
4. Validation: `approval_source` ∈ registered-channel names (accept the `github-review` alias per TASK-454's normalization).
5. Health: per-project deadlock variant + effective-source-has-registered-handler check.
6. Example YAML block documenting the per-project `approval:` overlay.
7. Tests: two-project config (telegram/slack) resolves gating + channel independently · escalation-gate ask on the slack project carries `PreferredChannel: slack` · gateway-mode controller honors the override · health check fires on unregistered source · no-override config byte-identical behavior (regression guard).
8. `make build` / `make test` / `make lint` green.

## Scope fence

No per-project *destinations* (chat_id/slack channel — needs a `Destination` field on Request; backlog) · no changes to escalation gates themselves · no approval-package changes beyond what resolution requires (routing fidelity is TASK-454) · no config flip (operator step, roadmap B5) · config remains static-at-boot (restart semantics documented; `Reload()` has zero callers).

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

## Refs

- Roadmap: `.agent/system/approval-architecture-roadmap.md` (leg B3 — merges after TASK-454 on the lane)
- Research pass 2026-08-06 (per-project config dimension, 11-point hook map)
- Pitfall on flip day: `.agent/knowledge/memories/pitfalls/require-approval-flip-doesnt-release-held-prs.md`

- **Dispatched**: https://github.com/qf-studio/pilot/issues/4774
