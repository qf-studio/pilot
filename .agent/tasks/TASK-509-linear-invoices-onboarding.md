# TASK-509: Linear Invoices onboarding — two repos, one Linear project, Linear polling routing gap

**Status**: 🟡 PARTIAL 2026-10-01 — GitHub path wired on the box (restart pending, operator); Linear polling OFF until [#5570](https://github.com/qf-studio/pilot/issues/5570) ships.
**Created**: 2026-10-01 · **Owner**: Navigator plans, Pilot executes · **Related**: TASK-508 (same day)

## Decisions
- Repos: `alekspetrov/linearinvoices-service` (Go, Makefile) and `alekspetrov/linearinvoices-client` (Next.js 15, pnpm). Both cloned on the box under the standard path, project entries added (backup `config.yaml.bak-20261001-*-linearinvoices`).
- Linear: workspace QuantFlow (urlKey qf-studio), team QF (`0aa5c39a-…`), existing project `invoice-generator-service` (`e16c6f39-…`, 114 open issues spanning both repos). `pilot` label created in team QF. Two extra per-repo Linear projects were created then deleted the same hour (redundant).
- Client gates (per-project `quality:`): `pnpm install --frozen-lockfile && pnpm build` · `pnpm test:ci` · `pnpm lint` (soft). Service uses the global `make build/test/lint`. pnpm installed via bun; `~/go/bin` (golangci-lint 2.12.2) and `~/.bun/bin` added to PATH in `start-pilot.sh`.
- API key: the founder pasted it in chat → treated as exposed, NOT written to the box. Rotate; the new key goes into `start-pilot.sh` as `LINEAR_API_KEY` at flip time, and `api_key: "${LINEAR_API_KEY}"` is added to the config then (the loader pre-scans raw text for `${VAR}` and warns when unset — do not reference it earlier).

## Blocking gap → #5570
On the SDK polling path every Linear issue goes to `deps.ProjectPath` (the default project = pilot repo on the box). `linear.project_id` pairing only works on the legacy webhook path; the SDK event's ProjectID is the team id. Fix: route by Linear label `repo:<pilot-project-name>` > `project_id` pairing > skip, never default. One Linear project across two repos needs the label.

## Repo state found (first tickets after restart)
- service: `make build` OK; `make test` FAILS — `TestPostgresRepository_Create`, `_GetByUserID_Authorization`, `_Update_Authorization` (DB-backed tests run under `-short`).
- client: build + lint OK; `pnpm test:ci` FAILS — 11 cases in EditInvoiceForm (status/due-date/currency/items/totals).
- Both repos last pushed 2025-10/11 and 2026-02. The `test` gate is required, so the first ticket in each repo is "make the unit suite green on the box" or the gate must be relaxed.

## Flip checklist (after #5570 is on the box)
1. Rotate the Linear key; `export LINEAR_API_KEY=…` in `start-pilot.sh`; add `api_key: "${LINEAR_API_KEY}"` under `adapters.linear`; `enabled: true`.
2. Label Linear issues `repo:linearinvoices-service` / `repo:linearinvoices-client` (114 open; backend-dev/frontend-dev hints in bodies), then `pilot` on the ones to run.
3. Restart via `start-pilot.sh` in the tmux session (operator). Startup proof: "Linear polling enabled … 1 workspace(s)".
