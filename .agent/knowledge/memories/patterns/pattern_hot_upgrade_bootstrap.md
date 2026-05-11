---
name: Hot Upgrade Chicken-and-Egg
description: Deploying a persistence fix via hot upgrade triggers the bug it fixes — first-restart-only problem
type: project
originSessionId: 86aef822-8124-4724-816f-1f26cf305635
---
Deploying a persistence-layer fix via hot upgrade triggers the very bug it fixes. Example: GH-1351 (ProcessedStore) — the hot upgrade that deploys the fix wipes the old in-memory map.

**Why:** Hot upgrade restarts the process; in-memory state from the pre-fix version is lost in the same step that installs the post-fix persistence layer.

**How to apply:** When deploying a fix that converts in-memory → persistent state, expect ONE bootstrap incident on the first restart. After that, future restarts are safe. Communicate this to the user before deploying; consider a one-time data-bridge if the lost state is costly.
