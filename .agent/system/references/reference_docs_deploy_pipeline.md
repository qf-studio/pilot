---
name: Docs deploy pipeline — chain self-heals via PILOT_DOCS_PAT
description: How v* binary release auto-deploys pilot.quantflow.studio end-to-end, the fragile points, and how to manually backstop
type: reference
originSessionId: 401056a8-07c0-4512-a11f-12384dcaa532
---
`pilot.quantflow.studio` deploys on a `prod-X.Y.Z-<ts>` tag pushed to the
**GitLab** mirror — not GitHub. As of 2026-04-27 the chain is
self-healing across binary releases.

### Full chain (post v2.100.4)

1. `v*` tag pushed → [release.yml](.github/workflows/release.yml) GoReleaser
   builds binaries + GitHub Release.
2. Same `v*` tag → [docs-version-sync.yml](.github/workflows/docs-version-sync.yml)
   bumps `docs/app/layout.tsx` header + `.agent/*.md` versions, opens PR
   using `secrets.PILOT_DOCS_PAT` (`||` fallback to `GITHUB_TOKEN`),
   auto-merges the PR with the same token.
3. The auto-merge commits with PAT identity, which **does** trigger
   downstream `push`-event workflows (unlike `GITHUB_TOKEN`, which
   doesn't).
4. [sync-docs.yml](.github/workflows/sync-docs.yml) fires on the
   docs/-touching merge → clones GitLab `quant-flow/pilot-docs`, copies
   `docs/`, pushes content + a unique `prod-X.Y.Z-<unix-ts>` tag to
   GitLab.
5. GitLab CI's `deploy-prod` job fires on `prod-*` tag → docker compose
   up on prod server → site refreshes.

### Fragile points

- `PILOT_DOCS_PAT` is a fine-grained PAT (Contents r/w + Pull-requests r/w
  on qf-studio/pilot only). When it expires, `docs-version-sync.yml`'s
  fallback to `GITHUB_TOKEN` keeps the auto-merge working but breaks
  step 3 — sync-docs no longer chains. Symptom: docs site stuck on the
  prior version while pilot.quantflow.studio still shows old header.
  Calendar reminder ~11 months out from creation.
- The PAT-missing fallback emits `::warning::PILOT_DOCS_PAT not set —
  …sync-docs.yml will not chain` in the workflow log. Watch for it on
  release runs.
- **`secrets.*` is illegal in step-level `if:` conditions** (parse-time
  rejection: `Unrecognized named-value: 'secrets'`). This silently broke
  the workflow for v2.100.5 → v2.102.1 (commit `79f25717` introduced
  `if: ${{ !secrets.PILOT_DOCS_PAT }}`). Tag pushes produced no run
  records; main pushes produced empty zero-job failed runs. Fixed in
  `9e22014f` by moving the check into `env:` (legal) +
  shell `if`. Pattern for future PAT-presence checks:
  ```yaml
  env:
    HAS_PAT: ${{ secrets.PILOT_DOCS_PAT != '' }}
  run: |
    if [ "$HAS_PAT" != "true" ]; then echo "::warning::..."; fi
  ```

### Manual backstop (`workflow_dispatch` exists since GH-2423)

If sync-docs.yml didn't fire after a release:

```bash
gh workflow run sync-docs.yml --repo qf-studio/pilot
```

That re-pushes `docs/` + a fresh `prod-X.Y.Z-<ts>` tag to GitLab using
the latest `v*` tag found via `git describe`. Idempotent; safe to
re-run.

### Anti-patterns (do not do)

- Do not `git tag prod-X.Y.Z && git push origin prod-X.Y.Z` to GitHub.
  The deploy listens on the GitLab mirror, not GitHub — a GitHub-side
  prod tag does nothing.
- Do not `git push --force` on `docs/version-sync-v*` branches; the
  workflow recreates them per release.

### Org-level prerequisite (one-time)

`Settings → Actions → General → "Allow GitHub Actions to create and
approve pull requests"` must be enabled at **org** level (`qf-studio`),
not just repo level. Without it, repo-level checkbox shows greyed-out
disabled and PR creation 403s. Enabled 2026-04-27.

### Cleanup history

- GH-2426 (commit `79f25717`, 2026-04-27): replaced `Resolve token` step
  that wrote secret to `$GITHUB_OUTPUT` with native `||` fallback at use
  sites. Same commit accidentally introduced the `secrets`-in-`if:` bug
  above; fixed in `9e22014f` (2026-04-29).
