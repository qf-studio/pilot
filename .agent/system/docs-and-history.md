# Docs & Historical Notes (moved from MEMORY.md to save space)

## Documentation & Docs Site

### Docs Architecture (2026-02-06)
- **Docs site**: Nextra v2.13.0 + Next.js 14, lives in `pilot/docs/`
- **Separate GitLab repo**: `git@gitlab.com:quant-flow/pilot-docs.git`
- **Sync**: GitHub Action (`.github/workflows/sync-docs.yml`) clones GitLab, replaces content from `docs/`, normal push (no force)
- **Deploy**: GitLab CI builds Docker image → **auto-deploy via `prod-{version}` tag** at `pilot.quantflow.studio`
- **Trigger**: Any push to `main` touching `docs/**` or the workflow file
- **SSH key**: `~/.ssh/pilot-docs-sync` (ed25519), GitHub secret `GITLAB_SSH_KEY`, GitLab deploy key with write access
- **GitLab `main` unprotected** to allow sync pushes (repo is a mirror, GitHub is source of truth)

### Docs Content Created
- `pages/index.mdx` — Homepage with ASCII logo, value prop, quick start
- `pages/concepts/why-pilot.mdx` — Vision doc
- `pages/getting-started/quickstart.mdx` — "First PR in 15 Minutes"
- `CONTRIBUTING.md`, `.github/FUNDING.yml`, `theme.config.tsx`

### Docs Key Gotchas
- `git init` on GitHub runners defaults to `master` — use `git init -b main`
- GitLab protected branches/tags block deploy keys — unprotect `prod-*` tags
- Nextra needs `output: 'standalone'` in next.config.mjs for Docker
- **Nextra v2 deps must be pinned** — `^2.13.0` resolves to v4.x, use `~2.13.0`
- MDX v1: markdown lists inside `<Tabs.Tab>` cause compile errors
- Deploy tag must be decoupled from content diff

## Stability Plan (COMPLETED 2026-02-11)
- 11 issues (GH-718–728) across 3 phases — reliability 3/10 → 8/10 achieved
- Phase 1: Stale labels, per-PR breaker, API retry, branch fail, rate limit
- Phase 2: Conflict detection, auto-rebase, sequential sub-issues
- Phase 3: SQLite state, LLM classifier, metrics
- All done, PRs merged

## Slack Integration (2026-02-09)
- Bot: "Quant Flow MCP Bot", workspace: `quantflow`, channel: `#engineering`
- Outbound working. Socket Mode done (v0.33.13).
