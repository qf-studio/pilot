# fix(http): API error messages dropped — server envelope is `{"error": msg}`, httpClient reads `body.message`

**Created**: 2026-07-31 · **Status**: 🚀 Dispatched to Pilot · **Last Updated**: 2026-07-31
**Repo**: `qf-studio/pilot-console-ui` · Drift-defect **#8** (envelope-mismatch class, same as GH-32)

## Problem

Every console error envelope is `{"error": <message>}` — all three
`writeJSONError` sites: `internal/orgs/handlers.go:571`,
`internal/bff/handlers.go:257`, `internal/proxy/proxy.go:395` (pilot-console).
But the UI's `src/lib/api/httpClient.ts` (~line 56) parses only
`body.message` and falls back to `response.statusText`:

```ts
const message = body && typeof body === 'object' && 'message' in body ? String(body.message) : undefined
throw new ApiError(response.status, message ?? response.statusText, body)
```

Net effect: **every server-provided error message is dropped**. Observed live
2026-07-31 during the GH-36/console#83 SOP verify: an invalid Anthropic key
produced the genuinely useful server message `credential validation failed:
orgs: anthropic rejected api key (status 401)` — the operator saw only
"Unprocessable Entity".

## Fix (mandated shape)

In `httpClient.ts` error mapping, read the server envelope key too:
`message ?? error ?? statusText` (keep attaching the raw `body` to
`ApiError`). Direction precedent: GH-32 aligned the UI adapter to the server
wire shape, not the reverse — same here. No server changes.

## Tests

Extend `src/lib/api/__tests__/httpClient.spec.ts`, failing against current
code:

- 422 response with body `{"error": "credential validation failed: …"}` →
  thrown `ApiError.message` equals the server message (not "Unprocessable
  Entity").
- Existing `message`-key and no-body → statusText behaviors unchanged.

## Verify (SOP)

Per `sops/quality/real-stack-verify-gates-ui-merges.md`: on the live local
stack (:5173), save an obviously-invalid Anthropic key → the inline error
(GH-36 surface) must show the server's validation message instead of the raw
status text.

## Refs

- Prior in class: GH-32 (list envelopes) · GH-36 / drift-defect #6 (the error
  surface this feeds) · pilot-console#83 / #7 (made this observable locally).
- Pilot issue: https://github.com/qf-studio/pilot-console-ui/issues/38
