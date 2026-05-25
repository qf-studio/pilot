# TASK-295: Webhook auth for Linear (Ed25519) + Azure DevOps (Basic Auth)

**Wave:** 2 (S) · **Parallel-safe with TASK-292/293/294** · **Audit ref:** §2 Action #7, §3.3 P2, CS-8

---

## Problem

6 of 8 webhook handlers verify signatures; Linear and Azure DevOps do not. Hostile requests to `/webhooks/linear` or `/webhooks/azuredevops` can inject fake issue events.

**⚠️ Asymmetry:** Linear and Azure DevOps do NOT share a verification scheme. Do NOT write a shared `VerifySignature(secret, header, body)` helper — each handler gets its own implementation.

- **Linear:** Ed25519 signature in `linear-signature` HTTP header. Verify with `crypto/ed25519`.
- **Azure DevOps:** HTTP Basic Auth (no HMAC). Parse `Authorization: Basic <base64(user:pass)>` against configured shared-secret.

## Approach

### Step 1 — Linear Ed25519 verification (S, ~60 min)

- `internal/adapters/linear/webhook.go`: add `VerifyLinearSignature(publicKey ed25519.PublicKey, header string, body []byte) error`
- Linear's docs: signature header is hex-encoded; body is the raw request body BEFORE JSON parsing
- Add `LinearConfig.WebhookPublicKey` (PEM-encoded ed25519 public key) to `internal/adapters/linear/config.go` (or wherever Linear config lives)
- Hook into the handler: reject with 401 if verification fails; log at WARN with source IP

### Step 2 — Azure DevOps Basic Auth (S, ~45 min)

- `internal/adapters/azuredevops/webhook.go`: add `VerifyAzureDevOpsBasicAuth(authHeader, expectedUser, expectedPass string) error`
- Parse `Authorization: Basic ...`, base64-decode, split on `:`, constant-time compare both halves
- Add `AzureDevOpsConfig.WebhookUser` and `WebhookPassword` config fields
- Hook into the handler: reject 401 if verification fails

### Step 3 — Tests (S, ~60 min)

- New `internal/adapters/linear/webhook_test.go` (mirror `plane/webhook_test.go` structure):
  - `TestVerifyLinearSignature_Valid` — generate keypair, sign payload, assert verify succeeds
  - `TestVerifyLinearSignature_InvalidSignature` — flip a byte, assert error
  - `TestVerifyLinearSignature_MissingHeader` — assert error
  - `TestVerifyLinearSignature_TamperedBody` — sign body A, verify against body B
- New `internal/adapters/azuredevops/webhook_test.go`:
  - `TestVerifyAzureDevOpsBasicAuth_Valid` — assert succeeds
  - `TestVerifyAzureDevOpsBasicAuth_WrongPassword` — assert error
  - `TestVerifyAzureDevOpsBasicAuth_MalformedHeader` — assert error
  - `TestVerifyAzureDevOpsBasicAuth_NoHeader` — assert error
- Both test files use constants from `internal/testutil/tokens.go` (per CLAUDE.md), not bespoke literals

### Step 4 — Config + docs (XS, ~30 min)

- Add example values to `docs/content/getting-started/configuration.mdx` for both new fields
- If either config field is empty AND adapter is enabled, emit startup WARN: "webhook signature verification disabled for <adapter>; set <field> in config to enable"

## Files to modify

- `internal/adapters/linear/webhook.go` (+ likely `client.go` or `types.go` for config)
- New: `internal/adapters/linear/webhook_test.go`
- `internal/adapters/azuredevops/webhook.go` (+ config)
- New: `internal/adapters/azuredevops/webhook_test.go`
- `internal/config/config.go` (add new YAML fields)
- `docs/content/getting-started/configuration.mdx`

## Test Strategy

- Unit per-platform tests as above
- Manual: send signed and unsigned webhook payloads via curl; confirm rejection of unsigned

## Effort

S (~3h total). One PR per platform OR one combined PR — either works since the changes don't overlap.

## Out of Scope

- Rotating webhook secrets (operator-facing config workflow)
- Audit log of webhook auth failures (separate observability task — audit §3.4 P3)
