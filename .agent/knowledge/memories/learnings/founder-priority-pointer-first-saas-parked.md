---
name: founder-priority-pointer-first-saas-parked
description: Founder directive (2026-07-17) — delivery priority is pointer first, then pilot reliability fixes; SaaS/platform work (TASK-405 Pilot Cloud, auth-service) is parked and must not be pushed in reports or queue recommendations
type: learning
---

# Founder priority: pointer delivery first, pilot reliability second, SaaS parked

**What happened (2026-07-17):** During the post-cutover watch/fix marathon, session
reports kept surfacing SaaS-roadmap items (TASK-405 S2 reconciler/B8, TASK-393
concurrency canary, auth-service progress) alongside pointer and pilot-bug status.
Founder corrected: "Stop pushing SaaS project's issues, we have to deliver pointer
first, and pilot's issues."

## Why
Pointer is the product being delivered now; pilot reliability fixes are what keep
its delivery pipeline alive. Platform/SaaS build-out consumes queue lanes, review
bandwidth, and report attention without advancing the current deliverable.

## How to apply
- Reports and plans: lead with pointer blockers, then pilot-repo fix status. Do
  not recommend SaaS-roadmap next steps unless the founder raises them.
- Queue watch: if auth-service / Pilot-Cloud tasks compete with pointer for
  project-worker lanes or review bandwidth, flag them as de-label candidates
  rather than progress.
- PR review priority ordering: pointer PRs and pilot-reliability PRs before any
  platform PRs.
- Related: [[decision-phase-ordering-measure-first]], TASK-393 (M3 baseline
  continues passively — no active pushes), TASK-405 (parked).
