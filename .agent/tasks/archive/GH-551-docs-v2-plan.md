# Docs v2: Complete Rewrite + Nextra 4 Migration

**Status:** Issues created, awaiting Pilot execution
**Created:** 2026-02-06

## Overview

Rewrite all Pilot documentation from scratch on Nextra 4 (App Router). Current docs cover ~30% of features (13 pages, Nextra v2). Target: 36 pages with full feature coverage.

## Issues

| Issue | Scope | Pages | Depends on |
|-------|-------|-------|------------|
| #551 | Nextra v2 → v4 infrastructure migration | Infra | — |
| #552 | Getting Started (installation, quickstart, config, upgrading) | 5 | #551 |
| #553 | Concepts (why-pilot, how-it-works, model-routing, security) | 4 | #551 |
| #554 | Integrations: GitHub + Telegram | 2 | #551 |
| #555 | Integrations: Linear, GitLab, Slack, Jira, Asana, Azure DevOps | 6 | #551, #554 |
| #556 | Features: Autopilot, Quality Gates, Budget, Alerts | 4 | #551 |
| #557 | Features: Approvals, Epic, Briefs, Replay + Navigator rewrite | 8 | #551, #556 |
| #558 | Dashboard, Monitoring, Advanced, Reference, Community, Homepage | 13 | #551 |

## Execution Order

```
#551 (infra) ──┬── #552 (getting started)
               ├── #553 (concepts)
               ├── #554 (github+telegram) ── #555 (remaining integrations)
               ├── #556 (core features) ── #557 (remaining features + navigator)
               └── #558 (dashboard, monitoring, advanced, reference, community)
```

## New File Structure

```
docs/
  app/
    layout.tsx                          # Root layout (replaces theme.config.tsx)
    [[...mdxPath]]/page.tsx             # Dynamic catch-all
  content/
    _meta.ts                            # 10 sections
    index.mdx                           # Homepage
    getting-started/                    # 4 pages
    concepts/                           # 4 pages
    integrations/                       # 8 pages (NEW section)
    features/                           # 8 pages
    navigator/                          # 4 pages
    dashboard-monitoring/               # 2 pages (NEW section)
    advanced/                           # 3 pages (NEW section)
    reference/                          # 2 pages (NEW section)
    community/                          # 1 page (NEW section)
  mdx-components.ts                     # Required by Nextra 4
  next.config.mjs
  package.json
  tsconfig.json
  docker/Dockerfile
```

## Page Template

Every page follows:
1. Title + 1-2 sentence description
2. Overview (what + why)
3. Prerequisites (if applicable)
4. Configuration (YAML block)
5. Usage (concrete examples)
6. Reference (tables of options)
7. Troubleshooting (if applicable)

## Key Decisions

- **Framework**: Nextra 4 (v4.6.0) with Next.js 15 App Router
- **Search**: Pagefind (replaces Flexsearch)
- **Content dir**: `content/` (not `pages/`)
- **Meta files**: `_meta.ts` (JS exports, not JSON)
- **Deploy**: Same pipeline (GitHub → GitLab sync → Docker → pilot.quantflow.studio)
- **Output**: `standalone` (Docker-compatible)

## Risks

| Risk | Mitigation |
|------|-----------|
| Nextra 4 + standalone Docker | Test early in #551 |
| Pagefind search path | Inspect .next output after build |
| MDX component API changes | Check Nextra 4 docs for imports |
| Content volume (36 pages) | Batched into 7 Pilot issues |
