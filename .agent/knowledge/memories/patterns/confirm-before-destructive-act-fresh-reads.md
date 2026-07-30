---
name: confirm-before-destructive-act-fresh-reads
description: Never take a destructive/escalating action from ONE read of an eventually-consistent source — require N consecutive confirming FRESH reads within a grace window, reset the counter on any contradicting read, and take one final fresh read immediately before acting.
type: pattern
---

**Established by pilot#4570/#4572** (autopilot declared a 29s-old OPEN PR "closed externally" from one unverified read, then deleted its branch): `internal/autopilot/controller.go` — `externalCloseGraceWindow` (5 min) + `externalCloseConfirmThreshold` (3 consecutive "closed" reads; any "open" read resets `ClosedReadCount` to 0) + `finalizeExternalClose` takes ONE MORE fresh read immediately before the destructive step and aborts if contradicted.

**Reused for pilot#4624** (spec-guard escalating to `pilot-blocked` from one possibly-stale body read): fresh re-fetch + re-validate immediately before the escalating `AddLabels`.

**How to apply:** whenever code reacts to GitHub (or any eventually-consistent API) with an action that is destructive, escalating, or hard to undo — branch delete, label escalation, issue close, tracking drop, execution finalize-as-failed — the trigger must be: (1) fresh single-object read, not a list snapshot (see [[fresh-get-body-discarded-list-snapshot-validated]]); (2) N consecutive confirmations within a grace window for young objects; (3) a final fresh read at the moment of action. One stale read must never be able to destroy state. Counters live alongside the object's tracking state; contradicting reads reset them.
