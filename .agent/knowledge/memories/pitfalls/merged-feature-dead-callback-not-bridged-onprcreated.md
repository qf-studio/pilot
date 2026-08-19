---
name: merged-feature-dead-callback-not-bridged-onprcreated
description: PR#4992 (Jira merge-side done leg, GH-4987) merged green with good tests and ZERO production effect — the leg hangs off autopilot PR tracking, but no path registers a pilot/JIRA-* PR into activePRs; sdk v0.35.1's jira adapter silently drops core.PollerDeps.OnPRCreated (only the github adapter bridges it), reconciler adoption filters pilot/GH- prefixes, and the external-merge path never calls the leg. TASK-460 false-success evidence. Rule — a feature gated on PR lifecycle must be traced from tracker event to the call site for THAT task family; GH-only assumptions hide in SDK adapter bridges and branch-prefix filters. Follow-ups: pilot#4999, sdk#123, sdk#124.
type: pitfall
created: 2026-08-19
---

# Merged, tested, dead: the Jira done leg couldn't fire because nothing tracks Jira PRs

**What happened (2026-08-19).** GH-4987 asked for a merge-side Jira done leg
(comment + transition when a `pilot/JIRA-*` PR merges — the KAN-6 card had
sat in "К выполнению" forever). Pilot shipped PR#4992: clean seam, right
client (SDK notifier per [[jira-two-parallel-clients-poller-is-sdk]]),
WARN-only, tests pass, CI green, issue auto-closed pilot-done. Post-merge
review traced reachability end-to-end and found the feature **cannot execute
in production**:

1. `notifyJiraDone` is called only from `handleMerging`, which requires the
   PR in `activePRs`. For Jira PRs, nothing ever puts it there:
   - sdk v0.35.1 `sdk/integrations/jira/adapter.go` bridges only
     Handler/ProcessedStore/MaxConcurrent from `core.PollerDeps` and **drops
     `OnPRCreated`** — only the github adapter bridges it. Pilot's callback
     registration (`cmd/pilot/poller_jira.go:95`) is dead wiring.
   - Reconciler untracked-PR adoption filters `pilot/GH-` prefixes.
   - The merged-PR scanner does metrics/self-heal only.
2. The motivating case — KAN-6's PR#4955 was HUMAN-merged — routes through
   `checkExternalMergeOrClose`, which never calls the leg
   ([[handlemerged-shadowed-dead-by-external-merge-detector]] adjacent).
3. Even if reachable: pinned SDK does English-name transition fallback
   (dead on the Russian-locale site) and comment-first-early-return (the
   pre-sdk#122 ADF parse failure would skip the transition).

The PR's FEATURE-MATRIX entry *claimed* reachability via the scanner's
adoption — the claim was checkable and false. The intent judge, quality
gates, and fixture tests all passed because the code is locally correct;
only cross-component reachability was missing.

**Rules.**
1. A feature that hangs off the autopilot PR lifecycle must be traced
   tracker-event → PR registration → stage transition → call site **for that
   task family** (GH / JIRA / LIN prefixes are routed differently). "The
   call site exists and is tested" proves shape, not delivery.
2. GH-only assumptions concentrate in three places: SDK adapter bridge
   structs (which PollerDeps fields survive), branch-prefix filters
   (`pilot/GH-`), and the external-vs-own merge detector. Check all three
   when extending lifecycle behavior to a new tracker.
3. Reviews of "wire X on merge" PRs: ask which concrete production event
   sequence invokes the new code, then find it in the daemon log or prove
   it can't appear. This is the second same-week catch of this class
   (see [[graphql-mock-tests-cannot-catch-schema-validation]]) — both are
   TASK-460 delivery-evidence rows.

**Follow-ups filed**: pilot#4999 (external-merge leg + idempotency flag) ·
sdk#123 (bridge OnPRCreated in jira/asana/gitlab/linear/plane adapters) ·
sdk#124 (statusCategory-matched transitions + decouple from comment failure) ·
sdk PR#122 tag + pin bump (activation gate).
