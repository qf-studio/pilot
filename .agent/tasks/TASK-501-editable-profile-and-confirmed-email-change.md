# TASK-501: Editable Profile — display name now, confirmed email change behind the email transport

**Status**: 📋 PLANNED 2026-09-28 (founder ask same day: "I can't edit the profile; email change must be confirmed"). Leg A ready to dispatch; Leg B gated on Leg C.
**Owner**: Navigator plans, Pilot executes per repo.
**Repos**: `qf-studio/auth-service` · `qf-studio/pilot-console` · `qf-studio/pilot-console-ui`

## Ground truth (nav-research 2026-09-28)

- `GET /api/v1/me` (pilot-console `internal/bff/handlers.go:243`) returns `{id,email,name,org?}` from the **console session row**, captured once at login/register. It never calls auth-service's `GET /auth/me`. Once name/email become mutable this cache goes stale.
- auth-service (`origin/main`) has: register + verify-email (`POST /auth/verify-email`), password reset (`/auth/password/reset[/confirm]`), authenticated password change (`PUT /auth/me/password`, re-checks the old password). It has **no** profile-update route and **no** change-email route. gRPC exposes only `ValidateToken/GetUser/CheckPermission/IntrospectToken`.
- BFF → auth-service writes: `AuthBackend` (`internal/bff/authservice.go:56`) has `Register/Login/Refresh` only. `sess.AuthAccessToken` is used for gRPC validation, never as a Bearer on an outbound HTTP call. New plumbing is needed; pattern to copy is the `authenticate(bff.CSRFGuard(...))` wrapper of the orgs routes.
- UI `stores/auth.ts` hydrates via `getMe()` once; `updateOrgName` (`:75`) is the precedent for a local patch without refetch. `ProfileView.vue` is read-only by design (GH-174). `User` type has `id/email/name` only.
- Email transport is **off everywhere**: console-api task def 13 has `PILOT_CONSOLE_EMAIL_ENABLED=false` (checked 09-28); auth-service `EMAIL_ENABLED` requires `PASSWORD_RESET_URL_BASE` + `EMAIL_VERIFY_URL_BASE`. SES identity `console@quantflow.studio` verified, production access granted (Nelya, 09-22). Decision: `console-email-transport-ses-not-email-service-repo`.
- No audit trail in pilot-console for user changes; auth-service has `audit.LogEvent` (`EventRegister`, `EventEmailVerified`). Rate limiting exists only on auth-service (`mw.RateLimit`).

## Design

### Leg A — display name edit (no email, no transport) — dispatch now
1. **auth-service**: `PUT /auth/me` `{name}` → `Service.UpdateProfile` (mirror `ChangePassword`, `internal/auth/service.go:584`): validate 1–80 chars, trim, audit `EventProfileUpdated`. Returns `UserInfo`.
2. **pilot-console**: `AuthServiceClient.UpdateProfile(ctx, accessToken, name)` sending `Authorization: Bearer <sess.AuthAccessToken>`; `PATCH /api/v1/me` `{name}` under `authenticate(bff.CSRFGuard(...))` beside `handleMe`; on success update the session row's `Name` and return the `meResponse`. 401 from auth-service → 401 to the UI (session expired path).
3. **pilot-console-ui**: Profile "Name" row becomes an inline edit (pencil → input → Save/Cancel, Enter/Escape), `api.updateProfile(name)`; on success patch `auth.user` like `updateOrgName`. Keep read-only rendering for email with a "Change" affordance disabled + tooltip "coming with email verification" until Leg B ships.
4. **Fix the cache**: `handleMe` refreshes `name`/`email` from auth-service `GET /auth/me` when the session row is older than N minutes, or on every call (cheap, one HTTP hop inside the VPC). Decide in the PR; default: on every call, fail-soft to the row.

### Leg B — email change with confirmation — gated on Leg C
1. **auth-service** state machine: `users.pending_email`, `email_change_token` (to NEW address, TTL 24 h), `email_revert_token` (to OLD address, TTL 7 d).
   - `POST /auth/me/email` `{new_email, password}`: re-auth with password (same bar as password change), reject if new == current or already taken, store pending + both tokens, send "confirm your new address" to NEW and "your email is being changed, revert here" to OLD. Audit `EventEmailChangeRequested`.
   - `POST /auth/email-change/confirm` `{token}`: swap `email ← pending_email`, mark verified, clear pending, keep the revert token alive, revoke all refresh tokens except none (force re-login is the simplest correct behaviour: sessions carry the old email). Audit `EventEmailChanged`.
   - `POST /auth/email-change/revert` `{token}`: restore old email, clear pending, revoke all sessions. Audit `EventEmailChangeReverted`.
   - Rate limit `POST /auth/me/email` per user (3/h).
2. **pilot-console**: relay routes `POST /api/v1/me/email` (session + CSRF → Bearer forward), `POST /api/v1/email-change/confirm|revert` (public, token in body, no session). On confirm/revert the BFF clears the caller's session (re-login).
3. **pilot-console-ui**: Profile "Email" row → modal (new email + password) → success state "check <new>"; landing routes `/email-change/confirm?token=` and `/email-change/revert?token=` (same family as the still-unbuilt reset/verify pages) that call the relay and redirect to login with a banner.
4. Session email drift: after confirm the old session is invalid by design; the UI handles the 401 with the existing expired-session path.

### Leg C — email transport prerequisites (blocks Leg B; also unblocks verify-on-register and password reset)
The 4-leg email track already planned (decision memo 09-22), now concrete:
1. pilot-console: relay route `POST /api/v1/send-email` exists behind `PILOT_CONSOLE_EMAIL_ENABLED`; flip to `true` on the console-api template (Nelya, template-first) with `PILOT_CONSOLE_EMAIL_FROM=console@quantflow.studio` (already set).
2. auth-service env (Nelya's quantflow stack): `EMAIL_ENABLED=true`, `EMAIL_SERVICE_URL` → console relay, `EMAIL_VERIFY_URL_BASE=https://console.pilotcloud.dev/verify-email`, `PASSWORD_RESET_URL_BASE=https://console.pilotcloud.dev/reset-password`, template token secret shared with the relay.
3. pilot-console-ui: `/verify-email` and `/reset-password` landing pages (not built yet).
4. One SES send proven from auth-service through the relay (smoke SOP).

## Dispatch order
1. Leg A: three issues (auth-service → pilot-console → pilot-console-ui), each depends on the previous being merged. auth-service PRs are reviewed by us; deploy of auth-service is Nelya's (ask with the version).
2. Leg C asks to Nelya (two template changes) + UI landing pages issue.
3. Leg B: three issues after C is live.

## Open questions (founder)
- Leg B re-auth: password confirm on email change (recommended, matches password change) — or MFA step-up when enrolled?
- Force re-login after a confirmed email change (recommended, simplest correct) — or live session patch?
- Leg A scope: name only, or also avatar (no field exists anywhere today; would need storage) — recommend name only.

## Refs
- ui#174 / PR#177 (read-only profile) · ui#180 (Profile page column) · decision `console-email-transport-ses-not-email-service-repo` · memory `ses-identity-has-no-consumer-email-service-is-the-missing-middle`
