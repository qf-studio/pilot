# Docs & Historical Notes (moved from MEMORY.md to save space)

## Documentation & Docs Site

### Docs Architecture (updated 2026-09-04 — GitLab → GitHub migration IN TRANSITION)
- **2026-09-04 migration**: docs repo moving to **GitHub `qf-studio/pilot-docs`** (created 09-04, full GitLab history mirrored, HEAD `ba193a0`; seed commit `8233e2c` adds a GHCR build workflow: image `ghcr.io/qf-studio/pilot-docs` on `main` push + `prod-*` tags). AWS deploy leg owned by Nelya — questions posted in Slack `#infrastructure` (C0BV37L87C1) 09-04. Transition = dual-push sync (pilot#5312); GitLab keeps deploying `pilot.quantflow.studio` until AWS serves it. **Operator step owed by founder**: extend `PILOT_DOCS_PAT` to `qf-studio/pilot-docs` (Contents r/w + Workflows r/w) — deploy keys are disabled by org policy. Retire GitLab (repo + `devops` runner + registry) only after AWS cutover is verified.
- **Docs site**: Nextra v4 + Next.js 15, lives in `pilot/docs/`
- **Legacy GitLab repo (still the deploy source until cutover)**: `git@gitlab.com:quant-flow/pilot-docs.git`
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
- MDX: markdown lists inside `<Tabs.Tab>` cause compile errors in Nextra v4
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
