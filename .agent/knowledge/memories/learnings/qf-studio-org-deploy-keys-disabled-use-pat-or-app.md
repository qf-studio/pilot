---
name: qf-studio-org-deploy-keys-disabled-use-pat-or-app
description: GitHub org qf-studio has deploy keys DISABLED by policy (gh repo deploy-key add → 422 "Deploy keys are disabled for this repository", seen on pilot-docs 2026-09-04). Cross-repo pushes from Actions need a fine-grained PAT (per-repo scope, Contents r/w + Workflows r/w if pushing workflow files) or a GitHub App token; PILOT_DOCS_PAT is scoped to qf-studio/pilot only and must be extended per target repo by the founder.
type: learning
---

# Learning: no deploy keys on qf-studio — cross-repo Actions pushes go through a PAT or App token

**Seen 2026-09-04 (TASK-492):** `gh repo deploy-key add ~/.ssh/pilot-docs-sync.pub --repo qf-studio/pilot-docs --allow-write` → HTTP 422 *Deploy keys are disabled for this repository*. Org-level policy (team plan), not repo-level; I am not org admin (`gh secret list --org` also 403s).

**Options, in order:**
1. Fine-grained PAT — `PILOT_DOCS_PAT` already exists on `qf-studio/pilot` but its repository access list is **only `qf-studio/pilot`**. Adding a target repo = founder edits the PAT (Settings → Developer settings → Fine-grained tokens) and adds *Workflows: read/write* when the push includes `.github/workflows/*` (otherwise GitHub rejects the push with "refusing to allow a Personal Access Token to create or update workflow"). Expiry reminder ~11 months from creation (2026-05-29).
2. GitHub App installation token via `actions/create-github-app-token` — no App id/private key secrets exist on `qf-studio/pilot` today (only `CANARY_GH_TOKEN`, `GITLAB_SSH_KEY`, `HOMEBREW_TAP_GITHUB_TOKEN`, `PILOT_DOCS_PAT`). Better long-term (TASK-461 already moved the daemon to App auth).
3. SSH deploy key — not possible on this org.

**Pattern:** gate the leg on the secret with `env: HAS_PAT: ${{ secrets.X != '' }}` + shell `if`, never `if: ${{ secrets.X }}` (parse-time rejection — reference `reference_docs_deploy_pipeline.md`).
