---
name: traefik-middleware-provider-qualifier
description: In multi-provider Traefik setups, router middleware references must be `@<provider>`-qualified — bare names silently break routing and the host returns 404.
type: pitfall
---

## Pitfall

When a Traefik instance has **more than one provider** active (e.g. the Docker
provider + a file provider for other services), middleware references on a
router defined via Docker labels **must** be qualified with `@docker`:

```yaml
# ❌ wrong — silently breaks routing on multi-provider hosts
- "traefik.http.routers.pilot-docs.middlewares=docs-compress,docs-secheaders"

# ✅ correct
- "traefik.http.routers.pilot-docs.middlewares=docs-compress@docker,docs-secheaders@docker"
```

The bare-name form happens to work in single-provider setups (no ambiguity to
resolve), so it tends to pass local `docker compose up` tests cleanly. In
production with a file provider also in the mix, Traefik can't resolve the
reference, the router becomes unsatisfiable, the host falls through to the
404 catch-all, and **the only header on the response is `x-content-type-options:
nosniff`** — which is set by Go's stdlib `http.Error()`, not by any middleware
we configured. That misleading header was the diagnostic distractor on 2026-06-03.

## Same pitfall, second form

Traefik headers middleware **only emits known native fields directly**. The
`contentSecurityPolicyReportOnly` key isn't a Traefik v2.11 native field and
will silently no-op. To set arbitrary response headers, route through
`customResponseHeaders`:

```yaml
# ❌ silently inert in Traefik v2.x
- "traefik.http.middlewares.x.headers.contentSecurityPolicyReportOnly=…"

# ✅ correct
- "traefik.http.middlewares.x.headers.customResponseHeaders.Content-Security-Policy-Report-Only=…"
```

## Triggered by

PR #3415 (cache + compression + security headers) → live site went 404 on deploy.
Fixed by hotfix #3421 — full incident in the post-merge comments on both PRs.

## How to apply

When adding a Traefik middleware to a router in `docs/docker-compose.prod.yml`
(or any other service's compose on shared Traefik hosts):

1. Always `@<provider>`-qualify middleware references at the router site.
2. For any header not in Traefik's [headers-middleware native field list](https://doc.traefik.io/traefik/v2.11/middlewares/http/headers/), use `customResponseHeaders.<Header-Name>=<value>`.
3. Smoke-test after deploy with the GET probe (HEAD doesn't trigger compression):
   ```bash
   curl -sS -D - -o /dev/null -H 'Accept-Encoding: gzip,br' https://<host>/
   ```
   Expect both `HTTP/2 200` and the explicit headers in the response.

Related: [[docs-cache-and-lighthouse]] (full SOP for the docs site).
