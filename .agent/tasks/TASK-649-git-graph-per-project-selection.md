# TASK-649: Git graph per-project selection — the gateway and the desktop app serve any configured project's graph, and the GIT GRAPH panel gets a project tab row (pilot#6207)

**Status**: ✅ **BOTH LEGS MERGED 11-08 — A1 #6219 → [PR#6225](https://github.com/qf-studio/pilot/pull/6225) 17:12Z · B1 #6220 → [PR#6229](https://github.com/qf-studio/pilot/pull/6229) 18:18Z; unreleased (next tag).** Post-merge APPROVE-WITH-NOTES on both, minors unfiled: a configured project with an empty `Path` falls back to `"."` instead of 404 (`internal/gateway/dashboard.go` lines 464–476); in browser mode a removed project shows "no git graph data" instead of the 404 error (`desktop/frontend/src/hooks/useGitGraph.ts` line 35). Done check after the tag: the GIT GRAPH tab row on the box's desktop/browser build. Was: 🚀 **A1 + B1 DISPATCHED 2026-11-08 → A1 [pilot#6219](https://github.com/qf-studio/pilot/issues/6219) · B1 [pilot#6220](https://github.com/qf-studio/pilot/issues/6220)** (`pilot` + `no-decompose`; B1 `Depends on: #6219`, nothing held). Was: 📋 PLANNED 2026-11-08 — two legs, A1 (Go) → B1 (frontend).
**Created**: 2026-11-08
**Assignee**: Navigator (plan, reviews) → Pilot (legs)
**Source**: community issue [pilot#6207](https://github.com/qf-studio/pilot/issues/6207) (scrappydog, 2026-11-07). The reporter runs six repositories on one daemon. ID note: TASK-648 is taken in `qf-studio/pilot-console` (shared ID space), so this doc is TASK-649.

---

## Context

**Problem**: with several entries under `projects:`, the GIT GRAPH panel always shows one project.
- Desktop (Wails): `desktop/app.go:408` `GetGitGraph(limit int)` loads the config on every call and uses `cfg.Projects[0].Path` (line 417), else `"."`.
- Gateway (browser mode and the embedded dashboard): `internal/gateway/dashboard.go:432` `handleGitGraph` reads the single `gitGraphPath` (`server.go:141`), set once by `SetGitGraphPath` (`server.go:512`) at the two daemon sites `cmd/pilot/main.go:1520` (gateway mode) and `:3017` (polling mode). The route `/api/v1/gitgraph` (`server.go:290`) takes only `limit`.
- No dashboard-class endpoint lists the configured projects. `GET /api/v1/chat/projects` (`internal/gateway/chat.go:205`) exists only when chat is enabled.

**Goal**: any configured project's graph is selectable in the API and in the UI, and today's callers see no change.

**Research (navigator-research 2026-11-08, over gateway, desktop, frontend, config, tests, memories):**
- Resolver: `(*config.Config).GetProjectByName` (`internal/config/config.go:1680`, case-insensitive, first match, nil when unknown) and `comms.ProjectSource` (`internal/comms/project.go:45-50`, `GetProjectByName` + `ListProjects`), built by `config.NewProjectSource(cfg)` (`internal/config/projects.go:13`). The gateway holds no `*config.Config`; it needs a setter.
- `dashboard.FetchGitGraph` (`internal/dashboard/gitgraph.go:77`) runs `git -C <path> fetch --prune` (10 s timeout) and then `git log --graph` on every call, with no cache. A raw path from a request must never reach it.
- Existing `?project=` params (`docs.go:125`, `events.go:122`) take a filesystem path and compare it to the one configured path. Metrics/queue/history take no param (`--dashboard-scope`). A name-based param is new and safer: it resolves only against config.
- Frontend: React 18 + Vite + TypeScript in `desktop/frontend/`. `src/provider.ts` picks `src/wailsjs.ts` (Wails, `goCall('GetGitGraph', limit)` at line 43, hand-maintained) or `src/apijs.ts` (browser, `fetchJSON('/api/v1/gitgraph?limit=N')` at line 43). `src/hooks/useGitGraph.ts` polls every 15 s. `src/components/GitGraphPanel.tsx` renders `<Card title="GIT GRAPH">`. `wailsjs/` bindings and `dist/` are generated and gitignored. There is no JS test runner; `npm run build` runs `tsc && vite build`. PR CI does not build the frontend (only `.github/workflows/release-desktop.yml` does).
- The TUI (`internal/dashboard/tui.go:1077` `syncGitGraphToSelectedTask`) already follows the selected task's project. Out of scope.

---

## Known Pitfalls & Patterns

- **PITFALL** (wiring parity, GH-4784 class; `cmd/pilot/docs_wiring_test.go:26`): `SetGitGraphPath(` is pinned at 2 occurrences in `main.go`, with `SetDocsProjectPath(` in parity. → A1 wires the new setter at both daemon sites, under a different name, and extends the wiring test to pin it at both sites.
- **PITFALL** (#4297, path canonicalization; cited at `docs.go:105`): path comparisons without canonicalization miss. → A1 does not compare paths at all for the new param; it resolves a name to a configured path, and the `default` flag in the project list uses `memory.CanonicalizeProjectPath` on both sides.
- **PITFALL** (generated Wails bindings): `desktop/frontend/wailsjs/**` regenerates at build; only `src/wailsjs.ts` is checked in. → A1 adds new `App` methods and keeps `GetGitGraph(limit int)` unchanged, so the current frontend works between the legs; B1 updates `src/wailsjs.ts` by hand.
- **PITFALL** (no frontend CI): green PR CI says nothing about the TypeScript. → B1's acceptance requires the pasted `npm ci && npm run build` output.

---

## Acceptance Criteria

- [ ] `GET /api/v1/gitgraph?project=<name>` returns that configured project's graph; the name match is case-insensitive.
- [ ] An unknown name returns 404 with a JSON error; a path-shaped value (`/tmp`, `../x`) is treated as a name and returns 404.
- [ ] Without `project`, the response is byte-for-byte today's behaviour (`gitGraphPath`, else `"."`).
- [ ] `GET /api/v1/gitgraph/projects` lists the configured projects in config order, with the one that matches `gitGraphPath` marked `default: true`.
- [ ] The desktop app exposes the same two capabilities as Wails methods; `GetGitGraph(limit int)` is unchanged.
- [ ] The GIT GRAPH panel shows one tab per configured project when there are two or more, labelled by name; one or zero projects shows no tab row.
- [ ] The selection defaults to the `default: true` project (else the first), survives an app reload (localStorage), and falls back to the default when the stored name is no longer configured.
- [ ] Only the selected project is polled; a tab switch fetches once immediately and resets the 15 s timer.

---

## Dispatch

Serial: A1 → B1 (`Depends on: #{{A1}}`). Both legs in `qf-studio/pilot`; `pilot` + `no-decompose`. File both in one run; Pilot holds B1 until A1 closes.

### LEG A1 — repo: qf-studio/pilot — title: feat(gateway,desktop): git graph for any configured project — /api/v1/gitgraph?project=<name> resolved against config (404 when unknown), GET /api/v1/gitgraph/projects, and desktop GetGitGraphForProject + ListGitGraphProjects

## Context
Community request #6207. The GIT GRAPH panel shows only one project when several are configured. The gateway route `/api/v1/gitgraph` (`internal/gateway/server.go` line 290, handler `handleGitGraph` in `internal/gateway/dashboard.go` line 432) reads the single `gitGraphPath` field (`server.go` line 141), set once by `SetGitGraphPath` at `cmd/pilot/main.go` line 1520 (gateway mode) and `:3017` (polling mode). The desktop method `GetGitGraph(limit int)` (`desktop/app.go` line 408) uses `cfg.Projects[0].Path`. `dashboard.FetchGitGraph` runs `git -C <path> fetch --prune` and `git log`, so a request value must never become a path.

probe: present qf-studio/pilot func \(s \*Server\) handleGitGraph\(
probe: present qf-studio/pilot func \(a \*App\) GetGitGraph\(limit int\)
probe: present qf-studio/pilot func NewProjectSource\(cfg \*Config\)
probe: absent qf-studio/pilot func \(s \*Server\) SetGitGraphProject[s]\(
probe: absent qf-studio/pilot func \(a \*App\) GetGitGraphForProjec[t]\(
probe: absent qf-studio/pilot /api/v1/gitgraph/project[s]

## Scope
1. `internal/gateway/server.go`: add a `gitGraphProjects comms.ProjectSource` field and `func (s *Server) SetGitGraphProjects(src comms.ProjectSource)` next to `SetGitGraphPath` (same mutex discipline). Register `GET /api/v1/gitgraph/projects` on `apiMux` next to `/api/v1/gitgraph`, so the same auth middleware covers it.
2. `internal/gateway/dashboard.go` `handleGitGraph`: read `project := r.URL.Query().Get("project")`. When it is empty, keep today's code path unchanged. When it is set: if `gitGraphProjects` is nil, return 404; else resolve with `GetProjectByName(project)`; nil → 404 with a JSON error body (`writeJSON` shape the file already uses); found → call the fetcher with the resolved `Path` and the parsed `limit`. Never pass `project` itself to the fetcher.
3. New handler `handleGitGraphProjects` (GET only, 405 otherwise): returns `{"projects":[{"name":..., "default":bool}]}` in `ListProjects()` order; skip entries with an empty name. `default` is true for the first entry whose canonicalized path (`memory.CanonicalizeProjectPath`) equals the canonicalized `gitGraphPath`. A nil source returns `{"projects":[]}`. Do not include paths in the response.
4. `cmd/pilot/main.go`: call `SetGitGraphProjects(config.NewProjectSource(cfg))` next to each `SetGitGraphPath(projectPath)` call (lines 1520 and 3017). Extend `cmd/pilot/docs_wiring_test.go`: `SetGitGraphProjects(` occurs exactly twice and gateway mode uses `p.Gateway().SetGitGraphProjects(`. Do not add a third `SetGitGraphPath(` call.
5. `desktop/app.go`: add `GetGitGraphForProject(project string, limit int) GitGraphData` (empty `project` = today's `GetGitGraph` behaviour; unknown name = `GitGraphData{Error: "unknown project: <name>"}`; found = that project's `Path` via `GetProjectByName`) and `ListGitGraphProjects() []GitGraphProject` (config order, empty names skipped, the first entry `default: true`, which matches today's `Projects[0]` choice). Make `GetGitGraph(limit)` call `GetGitGraphForProject("", limit)`. Add `GitGraphProject{Name string \`json:"name"\`; Default bool \`json:"default"\`}` to `desktop/types.go`.
6. Tests (table-driven):
   - `internal/gateway/dashboard_test.go` `TestHandleGitGraph`: rows `project=known` (fetcher gets the configured path), `project=KNOWN` (case-insensitive), `project=unknown` → 404 and fetcher not called, `project=/tmp` → 404 and fetcher not called, `project=` with nil source → today's path, `project=x` with nil source → 404. Put `// mutation pin: an unknown ?project= never reaches the fetcher -> this test fails` above the unknown/path rows.
   - New `TestHandleGitGraphProjects`: order, `default` flag, empty-name skip, nil source, 405.
   - `desktop/app_test.go`: with the existing fixture helpers (`newGitGraphFixtureRepo`, `configureFixtureProject`), add a second project and assert `GetGitGraphForProject("second", n)` reads the second repo, an unknown name sets `Error`, and `ListGitGraphProjects()` returns both with the first as default.

## Acceptance
- `go test -race ./internal/gateway/ ./desktop/ ./cmd/pilot/` passes; paste the output into the PR body.
- `curl` shapes in the PR body: `/api/v1/gitgraph?project=<name>` (200), `?project=nope` (404 body), `/api/v1/gitgraph/projects` (JSON).
- `GetGitGraph(limit int)` and the response of `/api/v1/gitgraph` without `project` are unchanged (existing test rows still pass unedited).

## Out of scope
Frontend changes (B1). `desktop/frontend/src/wailsjs.ts` / `desktop/frontend/src/apijs.ts`. The TUI. Caching or rate limits for `git fetch`. Project-name uniqueness validation in config. Any change to `--dashboard-scope` or to the metrics/queue/history endpoints.

## Refs
- #6207 (request) · Navigator TASK-649 in this repo

### LEG B1 — repo: qf-studio/pilot — title: feat(desktop): GIT GRAPH project tab row — one tab per configured project, last choice remembered, only the selected project is polled, in Wails and browser mode

## Context
Depends on: #{{A1}}
After A1 (#{{A1}}), the backend serves any configured project's graph: browser mode via `GET /api/v1/gitgraph?project=<name>` and `GET /api/v1/gitgraph/projects` (`{"projects":[{"name","default"}]}`), Wails via `App.GetGitGraphForProject(project string, limit int)` and `App.ListGitGraphProjects()` (same `{name, default}` items). The frontend still calls only `GetGitGraph(limit)`. Community request #6207 asks for a row of tabs above the GIT GRAPH panel, labelled with project names, default first, last choice remembered.

probe: present qf-studio/pilot export function GetGitGraph\(limit: number\)
probe: present qf-studio/pilot title="GIT GRAPH"
probe: absent qf-studio/pilot export function ListGitGraphProject[s]\(
probe: absent qf-studio/pilot export function GetGitGraphForProjec[t]\(

## Scope
1. `desktop/frontend/src/types.ts`: add `GitGraphProject { name: string; default: boolean }`.
2. `desktop/frontend/src/wailsjs.ts`: add `GetGitGraphForProject(project, limit)` → `goCall('GetGitGraphForProject', project, limit)` and `ListGitGraphProjects()` → `goCall('ListGitGraphProjects')`. The `wailsjs/` directory is generated; do not edit it.
3. `desktop/frontend/src/apijs.ts`: add the same two functions. `GetGitGraphForProject` calls `/api/v1/gitgraph?limit=N&project=<encodeURIComponent(name)>` (omit `project` when empty); `ListGitGraphProjects` calls `/api/v1/gitgraph/projects` and returns `projects`. Export both through `desktop/frontend/src/provider.ts` the way `GetGitGraph` is exported.
4. `desktop/frontend/src/hooks/useGitGraph.ts`: take a `project` argument; poll only that project every 15 s; on a project change, fetch once immediately, reset the timer, and ignore a response that arrives for a project that is no longer selected.
5. A project selection hook (new file desktop/frontend/src/hooks/useGitGraphProject.ts): load the list once on mount; the selected name is the localStorage key `pilot.gitgraph.project` when that name is in the list, else the `default: true` entry, else the first; persist every change.
6. `desktop/frontend/src/components/GitGraphPanel.tsx`: render a tab row above the graph when the list has two or more entries (none for zero or one), one button per project labelled by name, the selected one visibly active (reuse the existing Tailwind tokens of the dashboard), keyboard-focusable. Wire it in `desktop/frontend/src/App.tsx`.
7. If the list call fails (older daemon without A1), show no tab row and call `GetGitGraph(limit)` as today.

## Acceptance
- `cd desktop/frontend && npm ci && npm run build` passes; paste the output into the PR body (PR CI does not build the frontend).
- `go build ./desktop/...` passes.
- The PR body describes the manual check: two projects configured → two tabs; switch → the graph changes and survives a reload; one project → no tab row.
- No edits under `desktop/frontend/wailsjs/` or `desktop/frontend/dist/`.

## Out of scope
Go backend changes (A1). A JS test runner. The TUI. Showing several graphs at once.

## Refs
- #6207 (request) · #{{A1}} · Navigator TASK-649 in this repo

---

## Out of Scope (doc-level)

- TUI changes: `syncGitGraphToSelectedTask` already follows the selected task's project.
- A cache or a fetch throttle in `FetchGitGraph` (each call still runs `git fetch --prune`; polling stays on the selected project only).
- Project-name uniqueness validation in config (first case-insensitive match wins, as everywhere else).
- Path-based `?project=` (the docs/events convention); this route takes names only.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Param form | path (docs/events convention), name, config index | name, case-insensitive via `GetProjectByName` | Resolves only against config, so no request value becomes a `git -C` path; matches the issue's ask |
| Project list source | field on the gitgraph response, `/api/v1/chat/projects`, new endpoint | new `GET /api/v1/gitgraph/projects` | The chat route is gated on `chat.enabled`; a separate endpoint keeps the gitgraph response unchanged |
| Desktop API | change `GetGitGraph` signature, add methods | add `GetGitGraphForProject` + `ListGitGraphProjects`, keep `GetGitGraph(limit)` | Wails calls are positional; a signature change breaks the shipped frontend between A1 and B1 |
| Default project | `Projects[0]` everywhere, daemon `gitGraphPath` | daemon: the project whose path is `gitGraphPath`; desktop: `Projects[0]` | Each surface keeps its current default, so nothing moves for existing users |
| Leg split | one leg, two legs | two (Go, then TS) | A1 is verifiable with curl and Go tests; B1 needs a frontend build that PR CI does not run |
| Paths in the list response | include, omit | omit | The UI needs names only; no reason to publish filesystem layout over the API |

---

## Verify

```bash
go test -race ./internal/gateway/ ./desktop/ ./cmd/pilot/
cd desktop/frontend && npm ci && npm run build
curl -s -H "Authorization: Bearer $TOKEN" "http://localhost:9090/api/v1/gitgraph/projects"
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $TOKEN" "http://localhost:9090/api/v1/gitgraph?project=nope"   # 404
```

---

## Done

- [ ] A1 merged; `?project=<name>` and `/api/v1/gitgraph/projects` answer on the box's gateway after the next release.
- [ ] B1 merged; the desktop build from the release shows the tab row with two or more projects.
- [ ] Comment on #6207 with the release that ships both legs; close it.

---

## Refs

- https://github.com/qf-studio/pilot/issues/6207
- Archive: `.agent/tasks/archive/gh-1633.md` (original gitgraph endpoint), `.agent/tasks/archive/TASK-462-dashboard-fleet-wide-metrics-scope.md` (#4829)
- A1 → https://github.com/qf-studio/pilot/issues/6219
- B1 → https://github.com/qf-studio/pilot/issues/6220

---

**Last Updated**: 2026-11-08
