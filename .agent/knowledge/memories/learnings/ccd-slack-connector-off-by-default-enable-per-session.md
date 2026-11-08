# Learning: The Slack connector exists in Code sessions but is disabled by default; enable it per session before searching other venues

## Summary
In the Claude desktop Code tab the claude.ai Slack connector is present but status=disabled per session. session_connectors_status lists it; set_session_connector_enabled('Slack', true) turns it on (tools arrive next turn, and it becomes the default for new sessions). Do not conclude Slack is unreachable before that check. #infrastructure is C0BV37L87C1 (private); the infra contractor is U0ADEMJ7J83; the founder is U09HF3HEYDC.

## Context
2026-10-19: founder said the infra contractor answered; GitHub, Linear (the connected workspace is a client's, not Pilot's — PI-* keys are not reachable), Gmail and the browser pane showed nothing. The answer was a Slack thread reply; the connector was simply off.

## Details
Linear MCP here is bound to a client workspace: Pilot's own Linear (LIN-QF-*, PI-*) is not reachable from this session. Slack thread replies do not appear in GitHub or email. Order of checks for 'X answered': Slack connector status first, then GitHub search (--commenter, --involves), then Gmail. Outward posts still follow the double-post rule (read the thread, write the POSTED line into the TASK doc, then send, then stamp the ts).

## Recommended Approach
At session start, when a request involves team chat: call session_connectors_status; if Slack is disabled, enable it and end the turn; next turn read the channel/thread first.

## Related
- TASK-515

---
**Captured**: 2026-10-19
**Confidence**: 95%
**Concepts**: workflow-discipline, tooling
