---
name: top-level-autopilot-yaml-binds-to-nothing
description: A top-level `autopilot:` block in config.yaml decodes into nothing — the only binding is `orchestrator.autopilot` — and `Enabled` is otherwise set only by the `--env` flag, so a daemon can execute issues and open PRs while no CI monitor or merger exists at all
type: pitfall
---

# Top-level `autopilot:` binds to nothing; without `--env` autopilot never starts

**What happened (2026-07-24 → 07-26):** the hosted canary tenant
(`i-0decbc0dcf225cf18`) executed issues with real tool-use and opened green PRs
#103/#105 on `pilot-canary-sandbox` — which then sat OPEN and unmerged for two
days. S2 exit evidence went unmet. The roadmap's leading hypothesis was
`stage.require_approval: true` with no approval channel wired. Wrong: the
instance config reads `require_approval: false`. **Autopilot was never running
in the process at all.**

## Mechanism (three layers, each sufficient on its own)

1. **The YAML key binds to nothing.** `internal/config/config.go:41` `Config`
   has no top-level `Autopilot` field. The only binding is
   `OrchestratorConfig.Autopilot` (`config.go:153`, `yaml:"autopilot"`), i.e.
   the block must be nested under `orchestrator:`. No normalization lifts a
   top-level key. yaml decode is non-strict, so an entire misplaced
   `autopilot:` block is discarded **silently** — no warning, no error.
2. **`enabled` was never emitted.** `pilot-console/internal/fleet/configrender.go:109`
   `writeAutopilotBlock` renders `default_environment` + `environments.hosted.*`
   but no `enabled: true`.
3. **No `--env` flag.** `cmd/pilot/main.go:422-426` is the *only* place that
   forces `Autopilot.Enabled = true`, and it runs only when `envFlag != ""`.
   The tenant systemd unit is
   `ExecStart=/opt/pilot/bin/pilot start --config /var/lib/pilot/config.yaml`.

Every controller construction site (`main.go:1687/1768/1828/2349`) is guarded by
`cfg.Orchestrator.Autopilot != nil && …Enabled`. All four skip. The daemon runs
happily: poller dispatches, executor works, PRs get opened — and nothing adopts them.

## The tell

**`autopilot_pr_state` table absent from the ledger** (only `autopilot_metrics`
present). Diagnostics that query `select … from autopilot_pr_state` and reason
about empty results miss this — the correct first probe is `.tables`. Table
missing = subsystem never ran; table present but empty = never adopted.

## How to avoid

1. Debugging "PRs open but never merge": run `.tables` on the ledger BEFORE
   theorizing about approvals, CI checks, or rate limits. Then check the
   process's actual argv (`systemctl show pilot -p ExecStart`), not the config.
2. Treat "config says X" as unverified until the decoded value is observed —
   a silently-dropped block reads identically to a correct one in the file.
3. `configs/pilot.example.yaml:553` documents the same dead top-level shape.
   Copying the example gives an inert autopilot block. Strict decode
   (`yaml.Decoder.KnownFields(true)`) would have turned all of this into a
   startup error.
4. Corollary for hosted tenants: rendered config correctness is not provable
   from the renderer's tests — assert on the *running daemon's* behaviour
   (does an `autopilot_pr_state` row appear for a fresh PR?).

Related: [[required-checks-allowlist-makes-other-gates-decorative]] (same class:
a config value quietly disabling a whole safety leg),
[[board-sourced-repo-ignores-labeled-issues]] (config semantics invisible at
runtime), [[require-approval-flip-doesnt-release-held-prs]].
