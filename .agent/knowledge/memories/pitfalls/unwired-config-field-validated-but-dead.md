---
name: unwired-config-field-validated-but-dead
description: A config struct field with a Validate() check and a DefaultConfig() seed reads as "already wired" — but neither proves any production constructor actually consumes it (GH-4784, gateway bearer auth)
type: pitfall
---

# A validated config field is not proof it reaches the runtime

**What happened (GH-4784, 2026-08-07):** `internal/config/config.go` has had a
top-level `Auth *gateway.AuthConfig` field (`yaml:"auth"`) since before
PR#4752 — `DefaultConfig()` seeds it with `AuthTypeClaudeCode`, and
`Validate()` even rejects an `api-token` type with an empty token. Every
signal you'd normally trust said "this is live": a bound YAML key, a
default value, and validation logic. It was fully dead. Neither production
gateway-construction call site (`internal/pilot/pilot.go` gateway mode,
`cmd/pilot/main.go` polling mode) ever passed `cfg.Auth` into
`gateway.NewServerWithAuth` — both called `gateway.NewServer`, which hands
`gateway.NewServerWithAuth` a hardcoded `nil`. The daemon served all of
`/api/v1/` (including the PR#4752 approval-decision route) with zero auth
regardless of what operators put in `auth:` — mitigated only by the
default loopback bind.

**Why it bites:** `Validate()` running without error feels like proof of
wiring, but a validator only checks the *shape* of a field, never that
anything downstream reads it. A `DefaultConfig()` seed makes the field look
intentionally populated rather than orphaned. Grepping for the type name
(`AuthConfig`) surfaces the definition, the validator, and test fixtures —
none of which are the constructor call site that would prove liveness.

**How to avoid:**
1. For any config field, grep every production (non-test) call site of the
   constructor/function that's supposed to consume it — not just its
   struct definition, validator, or default. `grep -rn
   "gateway.NewServer(" --include=*.go | grep -v _test.go` is what actually
   found this gap.
2. If a function has both a plain constructor (`NewServer`) and an
   auth/config-accepting variant (`NewServerWithAuth`), audit that *every*
   production call site uses the accepting variant when the field is set —
   a stale non-accepting call left over from before the field existed is
   the exact shape of this bug (mirrors the GH-4738 "wire both, prove with
   a composed test" lesson — GH-4738 was two layers of a metrics pipeline
   both needing the fix; here it's two call sites both needing the same
   constructor swap).
3. Write the composed test starting from the *config* object, not from a
   hand-built version of the type it's supposed to produce — a test that
   builds `AuthConfig{...}` directly and passes it to `NewServerWithAuth`
   proves the middleware works, but does NOT prove `*config.Config` ever
   produces that `AuthConfig` in production. GH-4784's fix added a single
   `Config.GatewayAuthConfig()` gate that both call sites now share, and a
   test that starts from `*config.Config` and asserts 401/200 through a
   real HTTP round trip.
