# fix(autopilot): Validate() accepts built-in default_environment that ResolvedEnv() cannot resolve — silently runs stage

**Created**: 2026-07-26 · **Status**: 🚀 Dispatched to Pilot · **Last Updated**: 2026-07-26

## Problem

`default_environment: dev|stage|prod` with no `environments:` map passes startup
validation but is silently ignored at runtime — the daemon runs stage semantics
while reporting nothing wrong at load time. This is the GH-4544 defect class
(declared config ≠ actual behavior, silently), 5th member.

Reproduction (verified against `main` @ `729045cc`):

```go
cfg := &autopilot.Config{DefaultEnvironment: "dev"} // no Environments map
cfg.Validate()    // nil — accepts built-ins dev/stage/prod (GH-4546 / #4552)
cfg.ResolvedEnv() // error: default_environment "dev" does not match any key
                  //        in environments config (available: )
// → ResolvedEnvOrDefault() logs and falls back to stage
// → EnvironmentName() reports "stage"
```

Three components already treat built-in names as valid; `ResolvedEnv()`'s
`DefaultEnvironment` branch alone rejects them:

- `Config.Validate()` (`internal/autopilot/types.go` ~482): valid-set is
  `{dev, stage, prod} ∪ keys(Environments)`
- `SetActiveEnvironment` error text: "must be one of dev, stage, prod or
  defined in environments config"
- Startup log (`cmd/pilot/main.go` ~2350) reports `EnvironmentName()`

## Why this slice is missing

Parent GH-4544 was decomposed; child GH-4547 (PR #4553) carried exactly this —
its `ResolvedEnv` fell back to `defaultEnvironments()[c.DefaultEnvironment]`
after the map miss. #4553 was closed as superseded by sibling #4551, which
landed the precedence chain (`--env` > `default_environment` > legacy
`Environment` > stage) but WITHOUT the built-in fallback. The close dropped
this one slice.

## Fix (single file: `internal/autopilot/types.go` + its test file)

1. **`ResolvedEnv()`** — in the `DefaultEnvironment != ""` branch, after the
   `c.Environments` map miss, consult `defaultEnvironments()[c.DefaultEnvironment]`
   before returning the error. Keep the error for names in neither source
   (unreachable post-Validate, but the method is called on configs that never
   went through Validate).
2. **`EnvironmentName()`** — mirror: when `DefaultEnvironment` is set and not
   in the map, return `DefaultEnvironment` if it is a built-in
   (`defaultEnvironments()` key) instead of the current `"stage"`. The name
   reported must always name the config `ResolvedEnv()` actually returns.
3. **Stale doc comment** — the `DefaultEnvironment` field comment
   (`types.go:88-95`) still claims it "is not consulted automatically:
   ResolvedEnv() only honors an environment once SetActiveEnvironment has
   populated activeEnvName/activeEnvConfig". False since #4551 — rewrite to
   describe the actual precedence chain.

## Acceptance criteria

- `Config{DefaultEnvironment: "dev"}` (no Environments map): `Validate()` nil,
  `ResolvedEnv()` returns the built-in dev config with nil error,
  `EnvironmentName()` returns `"dev"`. Table-test all three built-ins.
- `Config{DefaultEnvironment: "custom"}` with `Environments["custom"]` present:
  unchanged — map entry wins (existing `TestResolvedEnv_DefaultEnvironmentMatch`
  stays green).
- `Config{DefaultEnvironment: "typo"}` (neither map nor built-in):
  `ResolvedEnv()` still errors; `ResolvedEnvOrDefault()` still falls back to
  stage (existing mismatch tests stay green, error message may extend).
- Precedence unchanged: `activeEnvName` (via `SetActiveEnvironment`) still wins
  over `DefaultEnvironment`; absence of `DefaultEnvironment` preserves the
  legacy `Environment` → stage fallback exactly (existing tests stay green).
- Invariant test: any name `Validate()` accepts must resolve via
  `ResolvedEnv()` without error.
- `go test ./internal/autopilot/` green; no behavior change for tenant-rendered
  configs (`default_environment: "hosted"` + matching `environments.hosted`
  key — map branch, untouched).

## Scope fence

ONLY `internal/autopilot/types.go` and `internal/autopilot/types_test.go`.
Do not touch `cmd/pilot/main.go`, config loading, or `SetActiveEnvironment`.
Do not decompose — this is a one-file fix.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4558
- Dropped slice: GH-4547 / PR #4553 (closed as superseded 2026-07-26)
- Siblings that landed: #4551 (GH-4550 precedence + honor), #4552 (GH-4546
  Validate), parent GH-4544
