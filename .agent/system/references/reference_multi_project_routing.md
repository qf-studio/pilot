---
name: Multi-Project Routing
description: Adapter routing across multiple repos — workspace mode + ProcessedStore parity (closed v2.10.0)
type: reference
originSessionId: 86aef822-8124-4724-816f-1f26cf305635
---
Pilot supports multiple repos per adapter via "workspace mode": legacy single-project configs need migration to `project_ids` + `projects` mapping for `ResolvePilotProject()` to work; otherwise issues route to the wrong repo (returns "").

**Status:** Non-GitHub poller parity closed in v2.10.0 — Linear/Jira/Asana/GitLab/AzureDevOps all have ProcessedStore, parallel exec, OnPRCreated wired. Hot-upgrade race (label gap between execution and pilot-done allowing re-dispatch) also closed.

**How to apply:** When debugging "wrong repo" routing, first check the adapter config has `projects` mapping (workspace mode), not just legacy single-project fields. Check `ResolvePilotProject()` returns non-empty.
