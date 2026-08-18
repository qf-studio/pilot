---
name: jira-two-parallel-clients-poller-is-sdk
description: Pilot has TWO parallel Jira clients — internal/adapters/jira serves the webhook path only; the live polling path is studio-sdk sdk/integrations/jira (cmd/pilot/poller_jira.go → jiraSDK). A poller-symptom fix landed in the internal copy (pilot#4929) ships nothing for polling users; verify the live import path in cmd/pilot/poller_*.go before filing adapter fixes.
type: pitfall
created: 2026-08-18
---

# Jira adapter fixes: the poller runs the SDK client, not internal/adapters/jira

**What happened (2026-08-18).** External report pilot#4917 (Jira Cloud poll dead:
ADF description object vs `Fields.Description string`) was root-caused in
`internal/adapters/jira/types.go:111` and fixed there via the #4929 epic
(PR#4939/#4943/#4948) — then the fix was validated live against a real Jira
Cloud site (quantflowstudio, issue KAN-6 with headings+bullets) and **the exact
reported error reproduced on the patched binary**:

```
WARN Failed to fetch issues error="failed to parse response: json: cannot
unmarshal object into Go struct field Fields.issues.fields.description of type string"
```

Root cause of the root-cause: `pilot start` polling goes through
`cmd/pilot/poller_jira.go` → `jiraSDK` = **studio-sdk `sdk/integrations/jira`**,
which is a line-for-line parallel copy (its own `types.go:111 Description
string`, its own `client.go` with the identical v3/legacy endpoint split and
even the same doc comments). `internal/adapters/jira` serves the **webhook**
path (gateway) only. The two copies drift-share bugs; a fix in one is invisible
to the other.

**Why the false confidence:** the internal copy contains a full poller too
(`internal/adapters/jira/poller.go`) — dead for the daemon path. Reading the
adapter package alone convinces you the fix shipped.

**How to apply.**

- Before filing/fixing ANY adapter defect from a runtime symptom, grep the
  live wiring first: `cmd/pilot/poller_<adapter>.go` imports decide which
  client actually runs. SDK import ⇒ the fix belongs in studio-sdk (+ release
  + pilot pin bump), not in `internal/adapters/`.
- Symptom side-channel: SDK poller log lines do NOT contain the adapter name
  (`msg="Failed to fetch issues"` with no `component=jira`) — grepping logs
  for the adapter name misses them entirely.
- The live-fire validation (real tracker instance + rich-text issue + labeled
  pickup) is what caught this; fixture tests in the patched package all passed.
  Same class as the real-stack-verify SOP for UI merges.
- Port filed: studio-sdk#119 (ADFText dual unmarshal + 410 hint). pilot#4917
  reopened until a pilot release carries the fixed SDK.

Related: [[pilot_issue_missing_no_decompose_fragments_single_fix]] ·
`.agent/sops/quality/real-stack-verify-gates-ui-merges.md`
