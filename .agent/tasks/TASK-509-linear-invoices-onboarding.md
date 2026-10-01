# TASK-509: Linear Invoices onboarding — two repos, one Linear project, Linear polling routing gap

**Status**: 🚀 LIVE ON GITHUB PATH 2026-10-01 13:15Z — daemon restarted with both repos; first tickets running (service#1, client#3→#4). Linear polling still OFF until [#5570](https://github.com/qf-studio/pilot/issues/5570) is on the box (running since 13:17Z). Key already in `start-pilot.sh` (founder accepted the exposure; key expires soon).
**Created**: 2026-10-01 · **Owner**: Navigator plans, Pilot executes · **Related**: TASK-508 (same day)

## Decisions
- Repos: `alekspetrov/linearinvoices-service` (Go, Makefile) and `alekspetrov/linearinvoices-client` (Next.js 15, pnpm). Both cloned on the box under the standard path, project entries added (backup `config.yaml.bak-20261001-*-linearinvoices`).
- Linear: workspace QuantFlow (urlKey qf-studio), team QF (`0aa5c39a-…`), existing project `invoice-generator-service` (`e16c6f39-…`, 114 open issues spanning both repos). `pilot` label created in team QF. Two extra per-repo Linear projects were created then deleted the same hour (redundant).
- Client gates (per-project `quality:`): `pnpm install --frozen-lockfile && pnpm build` · `pnpm test:ci` · `pnpm lint` (soft). Service uses the global `make build/test/lint`. pnpm installed via bun; `~/go/bin` (golangci-lint 2.12.2) and `~/.bun/bin` added to PATH in `start-pilot.sh`.
- API key: the founder pasted it in chat → treated as exposed, NOT written to the box. Rotate; the new key goes into `start-pilot.sh` as `LINEAR_API_KEY` at flip time, and `api_key: "${LINEAR_API_KEY}"` is added to the config then (the loader pre-scans raw text for `${VAR}` and warns when unset — do not reference it earlier).

## Blocking gap → #5570
On the SDK polling path every Linear issue goes to `deps.ProjectPath` (the default project = pilot repo on the box). `linear.project_id` pairing only works on the legacy webhook path; the SDK event's ProjectID is the team id. Fix: route by Linear label `repo:<pilot-project-name>` > `project_id` pairing > skip, never default. One Linear project across two repos needs the label.

## Also done 13:00–13:25Z
- Per-project `ci_checks`: service `[test-and-lint]` (its PR workflow job), client `[]` (no workflows). Client `test` gate `required: false` TEMP until its suite is green → flip back after client#4 merges.
- Labels `pilot`/`no-decompose`/`bug` created in both repos. Issues: [service#1](https://github.com/alekspetrov/linearinvoices-service/issues/1) skip Postgres tests without DB · [client#3](https://github.com/alekspetrov/linearinvoices-client/issues/3) vitest excludes + orphan test · [client#4](https://github.com/alekspetrov/linearinvoices-client/issues/4) 136 component failures (blocked by #3).
- Queue incident: #5566 (my body had a backticked fake path) was held and starved #5567/#5568/#5570 for 54 min → body fixed, #5567/#5568 unlabeled (re-label after #5570 lands), bug filed [#5572](https://github.com/qf-studio/pilot/issues/5572), pitfall `held-task-at-queue-head-starves-serial-project-worker`.
- Restart via SSM killed the tmux server (exec in start script) → relaunched with `tmux new-session`; pitfall `box-start-script-exec-kills-tmux-on-stop`.

## Repo state found (first tickets after restart)
- service: `make build` OK; `make test` FAILS — `TestPostgresRepository_Create`, `_GetByUserID_Authorization`, `_Update_Authorization` (DB-backed tests run under `-short`).
- client: build + lint OK; `pnpm test:ci` FAILS — 11 cases in EditInvoiceForm (status/due-date/currency/items/totals).
- Both repos last pushed 2025-10/11 and 2026-02. The `test` gate is required, so the first ticket in each repo is "make the unit suite green on the box" or the gate must be relaxed.

## Flip checklist (after #5570 is on the box)
1. In the box config under `adapters.linear`: add `api_key: "${LINEAR_API_KEY}"` (the env var is already exported by `start-pilot.sh`) and set `enabled: true`. Adapter init happens at start → restart: `tmux send-keys -t pilot:0.0 C-c`, wait for pgrep to clear, then `tmux new-session -d -s pilot -x 220 -y 50 'cd /home/ec2-user && ./start-pilot.sh'` (pitfall `box-start-script-exec-kills-tmux-on-stop`).
2. Label Linear issues `repo:linearinvoices-service` / `repo:linearinvoices-client` (114 open; backend-dev/frontend-dev hints in bodies), then `pilot` on the ones to run.
3. Restart via `start-pilot.sh` in the tmux session (operator). Startup proof: "Linear polling enabled … 1 workspace(s)".
