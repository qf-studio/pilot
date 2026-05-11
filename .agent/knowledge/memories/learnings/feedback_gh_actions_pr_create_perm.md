---
name: GH Actions "create and approve PRs" — org-level setting wins
description: Why workflows that auto-PR fail with "GitHub Actions is not permitted to create or approve pull requests"
type: feedback
originSessionId: 401056a8-07c0-4512-a11f-12384dcaa532
---
When a workflow using `peter-evans/create-pull-request@v6` (or similar)
fails with:

```
##[error]GitHub Actions is not permitted to create or approve pull requests.
```

even though `permissions: pull-requests: write` is set in the workflow
YAML — the block is at **org level**, not repo level.

**Why:** GitHub's permission model has three layers:
1. Workflow YAML `permissions:` (sets GITHUB_TOKEN scope)
2. Repo `Settings → Actions → General → Workflow permissions`
3. Org `Settings → Actions → General → Workflow permissions`

When the org-level "Allow GitHub Actions to create and approve pull
requests" is disabled, the repo-level checkbox renders **greyed-out
disabled and unchecked** — repo cannot override the org policy.

**How to apply:**

1. First diagnose with the API. If it returns 403 with `"You must be an
   org admin..."`, that confirms an org-level policy exists:
   ```bash
   gh api repos/{owner}/{repo}/actions/permissions/workflow
   # → {"can_approve_pull_request_reviews": false, ...}
   gh api orgs/{owner}/actions/permissions/workflow
   # → 403 if org policy is restricting (and you're not org admin)
   ```
2. Org owner flips: https://github.com/organizations/{org}/settings/actions
   → "Allow GitHub Actions to create and approve pull requests"
3. Repo-level checkbox un-greys; leave default (org-enable is enough).
4. Verify: `can_approve_pull_request_reviews: true` at repo level.

**Bonus gotcha (related but distinct):** even with the toggle on, pushes
made by `GITHUB_TOKEN` (e.g. `gh pr merge --auto`) do NOT trigger other
`push`-event workflows. If you need a chained pipeline, swap to a PAT.
See [Docs deploy pipeline](reference_docs_deploy_pipeline.md) for the
qf-studio/pilot resolution (PILOT_DOCS_PAT).

**Repo state at qf-studio/pilot (2026-04-27):**
- Org-level: enabled
- Repo-level: `can_approve_pull_request_reviews: true`
- PAT secret `PILOT_DOCS_PAT` created for chained-trigger workflows.
