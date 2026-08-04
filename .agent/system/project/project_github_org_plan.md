---
name: github-org-plan-team
description: qf-studio upgraded to GitHub Team 2026-08-04 (3 seats) — does NOT raise the 5000/hr API pool; does unlock branch protection on private repos
metadata:
  type: project
---

# GitHub org plan: Team (since 2026-08-04)

`qf-studio` is on **GitHub Team**, 3 filled seats (verified via `gh api /orgs/qf-studio`
2026-08-04).

## What it does NOT change

- **REST API rate limit is unchanged: 5000/hr per user.** Verified same day:
  `gh api rate_limit` → `limit: 5000`. Team plan does not raise API limits — only
  GitHub Enterprise Cloud membership bumps PATs to 15,000/hr. The shared user-pool
  constraint (pilot-aws hard rule #3, GH-4391 rate-budget client) stands as-is.
- The real decoupling fix for GH-4391 remains a **GitHub App installation token** for
  the daemon: an installation gets its own 5000/hr pool, separate from the user pool,
  isolating daemon polling from interactive `gh` sessions.

## What it DOES unlock

- **Branch protection / rulesets on private repos** — previously unavailable on the
  free org plan. The parked founder item "branch protection on `qf-studio/pilot` main"
  (TASK-405 founder decision 7) is now technically possible; it stays a founder
  decision because required-checks/review rules interact with autopilot auto-merge.
- CODEOWNERS enforcement, required reviewers, draft PRs on private repos; 3,000
  Actions minutes/mo.
