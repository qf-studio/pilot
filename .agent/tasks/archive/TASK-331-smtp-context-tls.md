# TASK-331: SMTP alert sender — honor context + enforce TLS

**Wave:** 2 (S) · **Pilot** · **Severity:** HIGH (combines a high + a medium SMTP finding) ·
**Audit ref:** TASK-322 §high "SMTP alert sender discards the context" + §medium "SMTP always plaintext"

---

## Problem

`SMTPSender.Send` (`internal/alerts/smtp.go:31,44-49`) signs the context as `_ context.Context` and calls
`net/smtp.SendMail`, which opens a raw TCP connection with **no deadline** and **no TLS enforcement**:
- The dispatcher wraps each channel send in a 30s timeout context (`dispatcher.go:177-181`), but SMTP
  ignores it — a hung/blackholed mailserver blocks on OS-level TCP timeouts (minutes), tying up a
  dispatch goroutine and defeating the timeout guarantee on the alerting path.
- `smtp.PlainAuth` only refuses plaintext creds if the server doesn't advertise STARTTLS; `SendMail`
  will happily fall back to plaintext on port 25, so SMTP username/password + alert bodies can transit
  in cleartext.

## Approach
- Replace `net/smtp.SendMail` with a context-aware flow: `net.Dialer{}.DialContext(ctx, "tcp", addr)`,
  wrap with `smtp.NewClient`, set conn deadlines from the context, require STARTTLS (explicit
  `tls.Config`) and fail if the server doesn't offer it; run Auth/Data. Honor `ctx.Done()` for
  connect/send so a wedged server can't block dispatch.
- Reject sending credentials over a non-TLS connection rather than relying on PlainAuth's implicit check.

## Files to modify
- `internal/alerts/smtp.go`
- `internal/alerts/smtp_test.go`

## Test Strategy
- Unit: a stalled mock SMTP server → `Send` returns on ctx timeout (well under the OS TCP timeout); a
  server not offering STARTTLS → `Send` errors rather than sending plaintext. Use a local `net.Listener`
  mock.

## Effort
S (~2h). One PR.

## Out of Scope
- The alerts event-loop decoupling (E1) — separate task.
