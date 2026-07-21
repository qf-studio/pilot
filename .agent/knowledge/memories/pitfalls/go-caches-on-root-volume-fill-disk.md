---
name: go-caches-on-root-volume-fill-disk
description: Go build+module caches on the box's 30GB root volume grew ~16GB and filled the disk — SSM (RunShellScript AND sessions) fails with empty output/rc=1, cloud-init growpart can't run (ENOSPC chicken-and-egg), recovery required EBS detach-rescue via the mgmt runner
type: pitfall
---

# Go caches on the root volume fill the disk — and a full root bricks every remote-ops path at once

**What happened (2026-07-20):** during the GH-4472 board work the box's 30GB
root volume hit 100%: `~/.cache/go-build` (7.4G) + `~/go/pkg/mod` (8.4G) had
accumulated from every manual rebuild AND every executor quality-gate build.
Symptoms cascade in a confusing order:

1. SSM RunShellScript returns `Failed`, rc=1, **empty stdout/stderr** — even
   for `echo` — while the agent still pings `Online` and EC2 status is `ok`.
2. `aws ssm start-session` dies with `Plugin with name Standard_Stream not
   found` (agent can't write session orchestration files).
3. Reboot does NOT self-heal: cloud-init's growpart also needs disk writes —
   console shows `OSError: [Errno 28] No space left on device:
   '/var/lib/cloud/data/tmp…'`. Growing the EBS volume alone is useless
   because nothing on-box can run to resize the partition/filesystem.

**Recovery that worked (via mgmt runner i-0147f5c24d234cdbb, admin):**
stop box → detach root vol → attach to runner → `growpart /dev/nvmeXn1 1` →
`mount -o nouuid` (xfs UUID collision with runner's own AL2023 root) →
`xfs_growfs` → purge caches → unmount → reattach as `/dev/xvda` → start.

## How to apply

- `GOCACHE=/var/lib/pilot/go/cache` and `GOMODCACHE=/var/lib/pilot/go/mod`
  (200GB data volume) are now exported in `~/.bashrc` AND `start-pilot.sh`
  (executor children inherit). Do not remove; any new build path must inherit
  them too.
- SSM failing with empty output + agent "Online" ⇒ suspect disk full FIRST,
  before agent/network theories.
- Do not burn time on reboots when root is full — go straight to the
  detach-rescue via the runner.
- Related: [[claim-lost-drops-count-toward-hard-cap]] (same-day recovery
  session), TASK-409 (box architecture, path shims).
