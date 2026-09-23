---
name: ecs-az-rebalancing-requires-az-spread-placement-first
description: ECS AvailabilityZoneRebalancing ENABLED is rejected (400) unless the service's first placement strategy is spread on attribute:ecs.availability-zone — a binpack-only singleton rolls the CFN stack back (console-reconciler 2026-09-23)
type: pitfall
---

# ECS AZ rebalancing needs an AZ `spread` placement first — binpack-only services roll back

**What happened (2026-09-23 11:17Z):** console redeploy 35853276663 to pick up the new golden AMI: `pilot-fleet-svc-console-api` updated, `pilot-fleet-svc-console-reconciler` went `UPDATE_ROLLBACK_COMPLETE`:

```
Service UPDATE_FAILED: The service couldn't be updated because Availability Zone Rebalancing
only supports availability zone spread as task placement strategy. (Service: Ecs, Status Code: 400)
```

Nelya's 1ebdd93 (09-22) set `AvailabilityZoneRebalancing: ENABLED` on every service template. console-api's placement is `spread attribute:ecs.availability-zone` then `binpack memory` → accepted. The reconciler's placement is `binpack memory` only → rejected. Rollback was clean; service stayed 1/1 on the old task definition (old AMI) — nothing down, but the AMI pickup was blocked until her template changed.

## Rule
- Rebalancing only makes sense with ≥2 tasks behind a spread; for a singleton (1 task, no LB) leave it off.
- A CFN template change that touches ECS `Service` properties can fail per-service on ECS validation while other services in the same family succeed — check each stack's events, not the workflow's overall status.
- Compare `aws ecs describe-services … placementStrategy` across services before enabling family-wide settings.
