# Docs & Historical Notes (moved from MEMORY.md to save space)

## Documentation & Docs Site

### Docs Architecture (updated 2026-11-18 — GitHub + AWS, host `docs.pilotcloud.dev`)
- **Docs site**: Nextra v4 + Next.js 15, lives in `pilot/docs/`
- **Host**: `docs.pilotcloud.dev` since 2026-11-01 (TASK-629). `pilot.quantflow.studio` answers with a 301 to it; the redirect stays 12 months or more.
- **Deploy repo**: GitHub `qf-studio/pilot-docs` (a mirror; this repo is the source of truth). It owns its own `.github/` and `docker/`.
- **Sync**: `.github/workflows/sync-docs.yml` clones pilot-docs with `PILOT_DOCS_PAT`, replaces the content from `docs/`, pushes `main` and a unique `prod-{version}-{timestamp}` tag
- **Deploy**: pilot-docs "Build image" (GHCR) on the `prod-*` tag → "Deploy QuantFlow AWS" (ECR → ECS → CloudFront invalidation), owned by the infra contractor
- **Trigger**: any push to `main` touching `docs/**` or the workflow file; `workflow_dispatch` as the manual backstop
- **Full chain and fragile points**: `system/references/reference_docs_deploy_pipeline.md`
- **GitLab**: not in the chain since 2026-09-06 (TASK-492, TASK-494). Still owed (operator): delete the repo secret `GITLAB_SSH_KEY`, archive the GitLab project `quant-flow/pilot-docs`.

### Docs Content Created
- `pages/index.mdx` — Homepage with ASCII logo, value prop, quick start
- `pages/concepts/why-pilot.mdx` — Vision doc
- `pages/getting-started/quickstart.mdx` — "First PR in 15 Minutes"
- `CONTRIBUTING.md`, `.github/FUNDING.yml`, `theme.config.tsx`

### Docs Key Gotchas
- Nextra needs `output: 'standalone'` in next.config.mjs for Docker
- MDX: markdown lists inside `<Tabs.Tab>` cause compile errors in Nextra v4
- Deploy tag must be decoupled from content diff (GitHub ignores a re-pushed tag, so each deploy uses a new tag name)

## Stability Plan (COMPLETED 2026-02-11)
- 11 issues (GH-718–728) across 3 phases — reliability 3/10 → 8/10 achieved
- Phase 1: Stale labels, per-PR breaker, API retry, branch fail, rate limit
- Phase 2: Conflict detection, auto-rebase, sequential sub-issues
- Phase 3: SQLite state, LLM classifier, metrics
- All done, PRs merged

## Slack Integration (2026-02-09)
- Bot: "Quant Flow MCP Bot", workspace: `quantflow`, channel: `#engineering`
- Outbound working. Socket Mode done (v0.33.13).
