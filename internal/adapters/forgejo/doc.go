// Package forgejo is the pure-transport layer for a Forgejo instance: a small
// REST client for the Gitea-compatible v1 API and a verified webhook receiver.
//
// It uses only the Go standard library and deliberately has no dependency on
// the config, executor or pilot packages; wiring lives elsewhere.
//
// Targeted version: Forgejo 11.0.x (verified against a disposable 11.0.16
// instance). References: the Forgejo API usage guide and the instance's
// /api/swagger document, plus the Forgejo webhook documentation.
//
// # Client
//
// A [Client] is bound to one instance and one repository ("owner/name"). The
// base URL must be https://, or http:// with a literal loopback IP address
// (127.0.0.1, ::1) for local testing. The API token is sent only as an
// "Authorization: token ..." header, redirects are never followed (so the
// credential can never leave the configured origin), response bodies and page
// counts are bounded, and errors never include the token or raw response
// bodies.
//
// # Webhook
//
// [NewWebhookHandler] accepts only signed POST deliveries. The set confirmed
// against Forgejo 11.0.16:
//
//   - Signature: hex HMAC-SHA256 of the exact raw request body, in
//     X-Forgejo-Signature (primary) or X-Gitea-Signature (fallback).
//   - Event: X-Forgejo-Event is "issues" for both an opened issue and a label
//     change. X-Forgejo-Event-Type is "issues" for action "opened" and
//     "issue_label" for action "label_updated". The action in the payload is
//     authoritative; the Event-Type header is not required.
//   - Payload fields consumed: action, repository.full_name, issue.number, and
//     the presence of a pull_request object (which marks a PR payload).
//
// Status codes: 204 accepted or deliberately ignored, 400 invalid content,
// 401 missing/invalid signature, 405 non-POST, 413 oversize body, 503 when the
// callback reports an error.
package forgejo
