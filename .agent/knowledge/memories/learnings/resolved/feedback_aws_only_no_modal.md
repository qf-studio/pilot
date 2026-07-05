> **RESOLVED/SUPERSEDED (2026-07-05):** Codified in .agent/system/aws-sandbox-infra.md + sops/daytona-bench-operations.md; bench dormant

---
name: AWS only — never use Modal for bench runs
description: Terminal Bench runs MUST use AWS infrastructure (warm pool EC2), never Modal. User has AWS infra specifically for this.
type: feedback
originSessionId: 33a7ad30-b5e9-49dd-b7a5-bf0e6a562c99
---
NEVER use Modal for benchmark runs. The user has AWS infrastructure (5 × t3.xlarge warm pool, deployer-runner EC2) specifically built for this purpose.

**Why:** Modal runs from local machine die when session closes. AWS instances persist. The user invested in building AWS infra and expects it to be used. Using Modal was a waste of money and time.

**How to apply:** When running Terminal Bench or any long-running bench job, always use `-e docker` on AWS EC2, never `-e modal`. Push bench code to the deployer-runner and execute from there.
