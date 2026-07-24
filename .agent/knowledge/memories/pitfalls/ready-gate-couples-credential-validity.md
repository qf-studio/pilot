---
name: ready-gate-couples-credential-validity
description: GH-3769 registered every adapter's live credential probe as a /ready readiness checker, so one expired token makes /ready return 503 — any lifecycle gate polling /ready reads a dead credential as "instance not ready", turning auth failures into provisioning/lifecycle failures
type: pitfall
---

# /ready couples credential validity into lifecycle gates

**What it is:** GH-3769 (TASK-379 V3) wired live adapter probes into
`doctor`, daemon preflight, **and `/ready`**. `registerAdapterReadiness`
(`cmd/pilot/adapter_preflight.go:133`) wraps each `verify.Verifiable` as a
gateway readiness checker; `handleReady`
(`internal/gateway/server.go:414`) returns **503** if *any* checker is not
ready. A single expired or unfunded credential therefore flips the whole
instance to not-ready.

This was the right call for a laptop daemon — "fail loud, don't silently
run with a dead token" was the entire point of the runtime
self-verification track. It becomes a diagnostic trap the moment something
*else* consumes `/ready` as a lifecycle signal.

## Why it bites in the hosted path

On the hosted tenant (2026-07-24), `/pilot/ANTHROPIC_API_KEY` had **zero
credits** and `/pilot/CLAUDE_CODE_OAUTH_TOKEN` was **stale** (401 loops).
The observable symptom is not "auth is broken" — it's `/ready` → 503, which
a fleet reconciler or provisioning gate reports as *the instance failed to
become ready*. Operator attention goes to EC2, user-data, and networking
while the actual fault is one rotted SSM parameter.

## How to avoid

1. When an instance won't go ready, read the `checks` map in the `/ready`
   body before touching infrastructure — it names the failing adapter
   (`{"ready":false,"checks":{"github":true,"anthropic":false}}`).
2. Don't treat `/ready` as a binary provisioning gate in control-plane code.
   Distinguish *never came up* (no response / process dead) from *up but a
   credential is red* — they need different operator actions and different
   alert routing. pilot-console#45 (ready-gate fix, slated for S3) is the
   place this gets addressed.
3. Credential rot is the most likely cause of a previously-green instance
   going 503 with no deploy in between. Check the credential's *validity*,
   not just its presence — the presence pre-check passes on a dead token by
   design (`checkAdapterVerify`, `internal/health/health.go:249`).

Related: [[oauth-ssm-params-rot-live-credentials-source-of-truth]] (what
actually rots), [[claude-cli-refuses-root-hosted-units]] (same day, same
class: laptop-shaped assumptions meeting a hosted unit).
