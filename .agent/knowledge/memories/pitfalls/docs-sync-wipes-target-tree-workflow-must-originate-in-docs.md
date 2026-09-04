---
name: docs-sync-wipes-target-tree-workflow-must-originate-in-docs
description: sync-docs.yml replaces the ENTIRE target repo tree (everything except .git and docker/) from pilot/docs/ on every sync — any file committed only in the target (CI workflow, README, compose) is deleted on the next docs change. Target-repo infra must be sourced from pilot/docs/ (that is why .gitlab-ci.yml and docker-compose.prod.yml live there).
type: pitfall
---

# Pitfall: the docs sync wipes the target tree — target-only files do not survive

**Mechanism** (`.github/workflows/sync-docs.yml`): clone target → `find "$WORK" -mindepth 1 -maxdepth 1 ! -name '.git' -exec rm -rf {} +` → `cp -a docs/. "$WORK"/` → restore only the target's `docker/` from a backup. Net: the target repo's tree is exactly `pilot/docs/` plus the target's own `docker/`.

**Consequence found 2026-09-04 (TASK-492):** the GHCR build workflow seeded directly on `qf-studio/pilot-docs` (`8233e2c`) will be deleted by the first sync unless the identical file also exists at `docs/.github/workflows/build.yml` in `pilot`. Same reason `.gitlab-ci.yml` / `docker-compose.prod.yml` / `.dockerignore` are in `pilot/docs/`.

**Corollary:** `docker/` is the reverse case — the sync restores the TARGET's copy over ours, so a Dockerfile edit in `pilot/docs/docker/` is silently discarded unless also pushed to the target.

**Rule:** anything the deploy-unit repo needs must be committed under `pilot/docs/` first; treat the target repo as a build artifact, never edit it directly except `docker/`.
