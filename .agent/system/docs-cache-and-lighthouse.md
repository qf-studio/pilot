---
name: Next.js self-hosted caching + Lighthouse 100% playbook (pilot.quantflow.studio)
description: Authoritative reference for caching, HTTP headers, security, and Lighthouse-100 strategy for the docs site — Nextra v4 / Next 15 / React 19 / Traefik v2.11, no CDN. Cited against official sources.
type: reference
---

> Companion to [[reference_docs_deploy_pipeline]] (which covers *how the site
> deploys*). This file covers *how it behaves once deployed* — caching, headers,
> compression, Lighthouse score blockers, and the prioritised fix plan.
>
> **Baseline measured 2026-06-03** against the running `prod-2.166.11-…` container.
> Update the "Live state" table when prod headers change.

## TL;DR — measured baseline

**Lighthouse 2026-06-03 (live `prod-2.166.11-…`, npx lighthouse@13.3.0):**

| Category | Mobile | Desktop |
|---|---|---|
| Performance | **100** | 96 (LCP 1.4 s → 0.83) |
| Accessibility | 92 | 89 |
| Best Practices | 96 | 96 |
| SEO | 91 | 91 |

The actual gap to 100 is **not** Performance / cache / compression — Next 15's
full-route cache + a fast TTFB already carry it. The blockers are:

1. **Upstream Nextra v4.6.1 a11y bugs** — the "Copy page" split-button and the
   Discord `chatLink` icon-only `<a>` ship without aria-labels (`button-name`
   w=10 + `link-name` w=7). Selectors:
   - `body > header.nextra-navbar > nav > a[href="https://discord.gg/…"]`
   - `article > div.x:border > button#headlessui-listbox-button-*` (no `title` attr)
2. **`/favicon.ico` 404** → fires `errors-in-console` (the only network error)
   AND (separately) `has-favicon`. One missing file = two audit fixes.
3. **`[learn more]` literal link text** in `content/index.mdx:84` and
   `content/getting-started/configuration.mdx:271` → `link-text` SEO audit.
4. **`opacity: 0.5` on the version badge** in `app/layout.tsx:48` → desktop
   `color-contrast` (mobile passes because the badge is hidden by responsive
   layout).
5. **Desktop LCP 1.4 s** — likely the hero text block; expected to improve to
   <1.2 s once HTML compression lands (217 KB → ~80 KB gzip).

PR 1 (Section 5) targets all of those for a Lighthouse-100 outcome.
PR 2 is cache / compression / security headers — **does not directly move the
score** (compression helps LCP indirectly) but is the right long-lived hygiene
+ regression protection. The rest of this document is the cited reference behind
both PRs.

### Snapshot — live response state (2026-06-03)

What's served today (raw, behind Traefik v2.11 TLS-only): no browser caching of
HTML (only `s-maxage=31536000` which browsers ignore — see §2.2), no
compression on HTML (`Accept-Encoding: gzip,br` ignored — see §2.5), no
security headers (no HSTS/CSP/X-Content-Type-Options/Referrer-Policy/
Permissions-Policy), `/favicon.ico` 404, `pilot-preview.png` (1.2 MB OG image)
at `max-age=0`, `x-powered-by: Next.js`. None of this is exotic — it's the
default `output: 'standalone'` shape with a TLS-only proxy in front.

## Live state (probed 2026-06-03)

| Asset | Live `Cache-Control` | Compression | Notes |
|---|---|---|---|
| `/` (HTML, 217 KB raw) | `s-maxage=31536000` (no `max-age`) | **none** — `Accept-Encoding: br, gzip` ignored | `x-nextjs-prerender: 1` ✅ |
| `/_next/static/*.js .css` | `public, max-age=31536000, immutable` | `Vary: Accept-Encoding` (gzip when negotiated) | Next default ✅, cannot be overridden ([Next.js headers reference](https://nextjs.org/docs/app/api-reference/config/next-config-js/headers#cache-control)) |
| `/logo.svg` | `public, max-age=0` | – | Default for `public/` ([Next.js public folder reference](https://nextjs.org/docs/app/api-reference/file-conventions/public-folder)) |
| `/pilot-preview.png` (1.2 MB OG image) | `public, max-age=0` | – | Every social-share preview refetch is a 1.2 MB hit |
| `/favicon.ico`, `/favicon.svg` | **404** | – | Lighthouse `has-favicon` ❌ |
| Security headers | – | – | None of HSTS · CSP · X-Content-Type-Options · Referrer-Policy · Permissions-Policy · COOP set |
| Server identification | `x-powered-by: Next.js` | – | Info leak |
| TLS | HTTP/2 · LE cert · TTFB ~130 ms | – | ✅ |

## 1. Next.js 15 caching model — applied to a static Nextra MDX site

Next 15 has four caches; only two of them ever fire here.

### 1.1 Request Memoization — **doesn't apply**
> "Request memoization is React's feature, not a Next.js feature… in the same render pass."
> — [Next.js — Caching: Request Memoization](https://nextjs.org/docs/app/deep-dive/caching#request-memoization)

Per-render React dedupe inside a single component tree. The catch-all MDX page
doesn't issue `fetch()` calls at all, so there's nothing to dedupe. Latent
only.

### 1.2 Data Cache — **inert until we add `fetch()` to MDX components**
**Behaviour change Next 14 → 15** (verified): `fetch()` is **no longer cached
by default** in 15. Opt in per-call with `fetch(url, { cache: 'force-cache' })`
or set route default with `export const fetchCache = 'default-cache'`.
> "By default, fetch requests are not cached." — [Next.js 15 upgrade guide](https://nextjs.org/docs/app/guides/upgrading/version-15#caching-semantics)

We have **zero `fetch()` calls** in the docs catch-all route (`app/[[...mdxPath]]/page.tsx`).
Setting `fetchCache` would do nothing today, but is the lever to pull if we ever
add a server-rendered widget that pulls remote data.

### 1.3 Full Route Cache — **the one we live on** ✅
Static prerender of HTML + RSC payload at build time, served straight from
`.next/server/app/`. Verified live: every page response has `x-nextjs-cache: HIT`
and `x-nextjs-prerender: 1`.
> "If a route is statically rendered, the route is fully cached at build time."
> — [Next.js — Caching: Full Route Cache](https://nextjs.org/docs/app/deep-dive/caching#full-route-cache)

Because the route exports no `dynamic`, no `revalidate`, and reads no dynamic
APIs (`cookies()`, `headers()`, etc.), Next prerenders the entire MDX tree at
build. **Nothing we add to runtime caching matters until a page becomes dynamic.**

### 1.4 (Client) Router Cache — **a small win we leave alone**
Browser-side cache of prefetched RSC payloads, lifetime configured by
`staleTimes`. Default is fine for docs. The live header
`x-nextjs-stale-time: 300` confirms the static client-cache TTL (300 s) for
prefetched payloads. ([Next.js — `staleTimes`](https://nextjs.org/docs/app/api-reference/config/next-config-js/staleTimes))

### 1.5 New 15.x APIs — what's relevant
- **`use cache` directive / `cacheLife` / `cacheTag` / `updateTag`** ("Cache
  Components", successor to PPR) is a **canary** feature gated behind
  `experimental.useCache` and explicitly *replaces* `unstable_cache`. ([Next.js — `useCache` reference](https://nextjs.org/docs/app/api-reference/config/next-config-js/useCache))
  Irrelevant to us until a route becomes dynamic.
- **`unstable_cache`**: not used; replaced by `use cache`.
- **`revalidatePath` / `revalidateTag`**: usable from a Route Handler / Server
  Action. We have neither today. Adding them would be the path to "rebuild only
  one page after a docs edit" if we ever serve docs from a remote source.

### 1.6 ISR self-hosted — works, but persists to disk
`revalidate` *does* work without Vercel. The revalidated HTML and the
`fetch()` cache land in `.next/cache/` on the container's filesystem.
> "Self-hosting Next.js applications… The cache will be written to disk in
> `.next/cache` by default." — [Next.js — Self-hosting](https://nextjs.org/docs/app/guides/self-hosting#caching-and-isr)

**Relevant to us:** this is exactly the dir we just stopped bind-mounting to
`/data/quantflow/pilot_cache` (PR #3373). If we ever re-enable any feature that
writes to `.next/cache` (ISR, `fetch()` caching, image optimization, `use cache`),
we'll regenerate the disk-bloat vector. **Mitigation:** keep `images.unoptimized: true`,
keep zero fetch caching, OR — if we ever need ISR — provide a
custom `cacheHandler` that points at Redis instead of disk. ([Next.js custom cache handler](https://nextjs.org/docs/app/guides/self-hosting#configuring-caching))

## 2. HTTP cache headers — defaults and how to fix the gaps

### 2.1 What Next 15 emits by default

| Path | Default header | Source | Override? |
|---|---|---|---|
| Statically prerendered HTML routes (App Router) | `cache-control: s-maxage=31536000, stale-while-revalidate` for static; mixed for partial | [Next.js — Caching headers](https://nextjs.org/docs/app/api-reference/config/next-config-js/headers#cache-control) | ✅ via `headers()` in `next.config.js` (but **not** for the bullets below) |
| `/_next/static/**` | `public, max-age=31536000, immutable` | same source as above | ❌ — Next blocks override (chunk filenames are content-hashed so they're permanently safe to cache) |
| `/_next/image` (the optimizer endpoint) | `public, max-age=...` driven by `images.minimumCacheTTL` | [Next.js — Image config](https://nextjs.org/docs/app/api-reference/components/image#minimumcachettl) | We have it OFF; N/A |
| `public/*` static files | `public, max-age=0` | [Next.js public folder reference](https://nextjs.org/docs/app/api-reference/file-conventions/public-folder) | ✅ via `headers()` |

### 2.2 Why the live `s-maxage=31536000` does nothing for us

`s-maxage` is a **shared-cache** directive — only honoured by CDNs / shared
proxies, ignored by browsers. ([MDN — Cache-Control: s-maxage](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Cache-Control#s-maxage))
We have no CDN. Traefik does not cache. So no client ever benefits from this.

The browser only sees a response with **no `max-age`** — meaning RFC 9111 says
the cache is **heuristically freshness-validated** (typically 10% of
`Last-Modified` age, often near-zero for fresh deploys). In practice this
means most clients do a conditional GET on every navigation. ETag still saves
us the body — we get 304s — but we pay the round-trip.

### 2.3 What to send instead

For a static docs site, deployment is the only invalidation event. Two policies
that work together:

| Asset class | Recommended `Cache-Control` | Why |
|---|---|---|
| HTML (top-level prerendered routes) | `public, max-age=0, must-revalidate` | Force a conditional revalidation (the 304 path stays cheap thanks to the ETag Next already sets) but stop the 10% heuristic. Standard for "always-fresh HTML with versioned assets" pattern. ([MDN — `Cache-Control: must-revalidate`](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Cache-Control#must-revalidate)) |
| `/_next/static/**` | (already) `public, max-age=31536000, immutable` | Filenames are content-hashed; safe to cache forever |
| `public/*` (logo, OG image, etc.) | `public, max-age=604800, stale-while-revalidate=86400` | Versioned by content, not by hash. A week cache + 1-day SWR is the [web.dev "static assets" guidance](https://web.dev/articles/http-cache#flowchart) |
| `/favicon.ico` | `public, max-age=31536000` | Browsers refetch favicons aggressively; cache it hard |

### 2.4 How to set them — `headers()` vs Traefik

`next.config.mjs` `headers()` is the **canonical** place ([Next.js — headers reference](https://nextjs.org/docs/app/api-reference/config/next-config-js/headers)):

```js
// docs/next.config.mjs
async headers() {
  return [
    { source: '/:path*\\.html', headers: [
      { key: 'Cache-Control', value: 'public, max-age=0, must-revalidate' },
    ]},
    { source: '/((?!_next/static|_next/image|favicon).*)', headers: [
      { key: 'Cache-Control', value: 'public, max-age=0, must-revalidate' },
    ]},
    { source: '/:all*(svg|png|jpg|jpeg|gif|webp|avif|ico|woff2?)', headers: [
      { key: 'Cache-Control', value: 'public, max-age=604800, stale-while-revalidate=86400' },
    ]},
  ]
}
```

Use Traefik labels only for things `headers()` can't set: HSTS (which depends
on TLS-termination context) and Permissions-Policy. Trade-off summary:

- **`next.config.mjs` `headers()`** — version-controlled with the code; affects
  build manifest; *cannot* override `/_next/static/**` (Next blocks it).
- **Traefik `Headers` middleware** — out of band; survives container rebuilds;
  good for security headers; cannot see Next's `etag` to interact with it.

### 2.5 Compression — Traefik does it, not `server.js`

Next's `compress: true` (default) **only applies to `next start`**. The
standalone `server.js` does **not** compress responses.
> "When deploying a custom server, you'll need to set up your own gzip
> compression." — [Next.js — Self-hosting: compression](https://nextjs.org/docs/app/guides/self-hosting#compression)

Traefik v2.11 has a **gzip-only** compress middleware. Brotli requires
Traefik v3.x. ([Traefik v2.11 — Compress middleware](https://doc.traefik.io/traefik/v2.11/middlewares/http/compress/);
brotli was added in v3 — [Traefik PR #9387](https://github.com/traefik/traefik/pull/9387))

For us:

1. **Now (P0):** add Traefik compress middleware to the docs router for gzip — closes Lighthouse `uses-text-compression`.
2. **Soon (P1):** set `compress: false` in `next.config.mjs` to skip Next's
   no-op compress overhead. (Defensive — verified the standalone path doesn't
   compress anyway, but explicit is better.)
3. **Strategic:** schedule Traefik v3.x upgrade for brotli (~20% smaller HTML
   over gzip on typical text/HTML). Out of repo — track alongside the GitLab
   retention item in #3380.

## 3. Lighthouse 100% — concrete checklist

Lighthouse scores against [the published audit catalog](https://github.com/GoogleChrome/lighthouse/blob/main/docs/scoring.md).
Below: only the audits we'd actually fail on the current site, and what
specifically flips each one to green.

### 3.1 Performance (mobile is the hard target)

| Audit | Current | Fix | Source |
|---|---|---|---|
| `uses-text-compression` | ❌ fail | Enable Traefik compress middleware | [web.dev — text compression](https://web.dev/articles/uses-text-compression) |
| `uses-long-cache-ttl` | ⚠ partial (public/* at max-age=0) | `headers()` override for `public/*` | [web.dev — efficient cache policy](https://web.dev/articles/uses-long-cache-ttl) |
| `unsized-images` | ❌ if `<img>` lacks width/height | Add explicit `width`/`height` to every `<img>`/MDX image | [web.dev — unsized images](https://web.dev/articles/optimize-cls) |
| `modern-image-formats` | ⚠ PNG/JPEG without webp/avif siblings | Build-time pipeline to emit `.webp`/`.avif` and use `<picture>` | [web.dev — modern formats](https://web.dev/articles/uses-webp-images) |
| `uses-optimized-images` | ❌ 1.2 MB OG PNG | Recompress at build (`sharp`) | [web.dev — optimize images](https://web.dev/articles/uses-optimized-images) |
| `render-blocking-resources` | usually OK on Nextra v4 | Verify in Lighthouse run | [web.dev — render-blocking](https://web.dev/articles/render-blocking-resources) |
| `unused-css-rules`, `unused-javascript` | depends on Nextra theme | Don't fight Nextra defaults; verify they're not blocking 100 | – |

**Nextra v4 specifics:** Nextra v4 ships a precomputed CSS bundle from the theme
package and the MDX renderer. There is **no documented official knob** to trim
the theme CSS short of overriding the theme; treat the bundle as immutable until
benchmarks prove it's the blocker. ([Nextra v4 — theme docs reference](https://nextra.site/docs/docs-theme))

**Fonts:** we have **no `next/font` imports**. Whatever Nextra ships, its theme
loads. Verify with DevTools → Network: if the theme pulls a remote font, override
with `next/font/local` self-hosted woff2 and `display: 'swap'` to eliminate
FOIT/render-blocking. ([Next.js — Font optimization](https://nextjs.org/docs/app/api-reference/components/font))

**Images:** for docs content where bytes are small and dimensions known,
plain `<img loading="lazy" decoding="async" width="X" height="Y">` is enough to
get to 100. `next/image` becomes essential only when (a) we want responsive
`srcset`s for retina, or (b) we want format negotiation. Since we disabled the
optimizer to fix the disk-bloat root cause, the build-time recompress pipeline
(Section 5 PR2) is the right substitute. ([web.dev — Use Image CDNs / explicit dimensions](https://web.dev/articles/serve-images-with-correct-dimensions))

### 3.2 Accessibility

| Audit | Likely state | Fix |
|---|---|---|
| `color-contrast` | Nextra default light theme passes 4.5:1; dark mode close but verify | If a custom token fails, override CSS variable |
| `document-title` | ✅ Nextra emits from frontmatter `title` | – |
| `html-has-lang` | ⚠ if `app/layout.tsx` is missing `<html lang="en">` | Add `lang="en"` |
| `meta-viewport` | ✅ Nextra ships it | – |
| `landmark-one-main` | ✅ via theme | – |
| `link-name` / `button-name` | risk only on custom MDX | Audit our `pages/*.mdx` |
| `aria-hidden-focus` | usually OK | – |

WCAG 2.2 AA is the implicit target. ([W3C — WCAG 2.2](https://www.w3.org/TR/WCAG22/))

### 3.3 Best Practices

| Audit | Current | Fix | Source |
|---|---|---|---|
| `is-on-https` | ✅ | – | – |
| `csp-xss` | ❌ no CSP set | Add CSP via Traefik `Headers` middleware (start in report-only) | [web.dev — CSP](https://web.dev/articles/strict-csp); [Lighthouse `csp-xss` audit](https://web.dev/articles/csp-xss) |
| `has-hsts` | ❌ no `Strict-Transport-Security` | Add `max-age=63072000; includeSubDomains; preload` (Lighthouse minimum is 1 year) | [MDN — HSTS](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Strict-Transport-Security); [HSTS preload requirements](https://hstspreload.org/) |
| `errors-in-console` | risk: Pagefind init errors if no `/pagefind` mount | Verify with DevTools | – |
| `no-document-write` | ✅ | – | – |
| `geolocation-on-start`, `notification-on-start` | ✅ | – | – |
| `no-vulnerable-libraries` | run `npm audit` periodically | – | – |
| `has-favicon` (perf adjacent) | ❌ 404 | Add `public/favicon.ico` and `public/favicon.svg` | – |

**Minimum to score 100 on Best Practices:**

```http
Strict-Transport-Security: max-age=63072000; includeSubDomains
X-Content-Type-Options: nosniff
Referrer-Policy: strict-origin-when-cross-origin
Content-Security-Policy: default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline' data:; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'
Permissions-Policy: geolocation=(), microphone=(), camera=(), payment=()
```

CSP gotcha: Nextra v4 inlines small CSS chunks, hence the `'unsafe-inline'` for
`style-src` and `script-src`. Lighthouse `csp-xss` will *warn* on
`'unsafe-inline'` but does **not** fail the audit because of it. ([Lighthouse — Best Practices scoring](https://github.com/GoogleChrome/lighthouse/blob/main/docs/scoring.md#best-practices))
Going stricter (nonces/hashes) is a separate hardening task — out of scope for
"reach 100".

### 3.4 SEO

| Audit | Current | Fix |
|---|---|---|
| `meta-description` | ⚠ depends on `app/layout.tsx` metadata | Set in `app/layout.tsx` via [Next.js metadata API](https://nextjs.org/docs/app/api-reference/file-conventions/metadata) |
| `link-text` | – | – |
| `crawlable-anchors` | ✅ Nextra | – |
| `robots-txt` | usually missing | Add `app/robots.ts` per [Next.js robots file convention](https://nextjs.org/docs/app/api-reference/file-conventions/metadata/robots) |
| `tap-targets`, `viewport` | ✅ | – |
| `canonical` | ⚠ Nextra v4 sets canonical when `metadataBase` is set in layout | Set `metadataBase: new URL('https://pilot.quantflow.studio')` in `app/layout.tsx` |
| sitemap | not required by Lighthouse, but Google needs it | Add `app/sitemap.ts` — [Next.js sitemap file convention](https://nextjs.org/docs/app/api-reference/file-conventions/metadata/sitemap) |
| Open Graph / Twitter | depends on metadata | [Next.js openGraph metadata](https://nextjs.org/docs/app/api-reference/functions/generate-metadata#opengraph) |

## 4. Self-hosted gotchas

### 4.1 `output: 'standalone'` semantics

> "By default, `next build` will create a `.next` folder that includes all the
> output files… Self-hosting Next.js can be deployed to any Node.js or Docker
> hosting provider." — [Next.js — Self-hosting](https://nextjs.org/docs/app/guides/self-hosting)

The standalone build emits a minimal `node_modules` plus `server.js` that runs
the Next runtime in process. Caveats:

- The `compress` Next.js config flag is **ignored** in standalone (see §2.5).
- The `cacheHandler` for ISR is a *file* path written from `.next/cache/` —
  unmounted means cache is ephemeral per container, which is what we want for
  a static site. ([Next.js — Custom cache handler](https://nextjs.org/docs/app/guides/self-hosting#configuring-caching))
- `output: standalone` + `images.unoptimized: true` correctly prevents the
  optimizer from writing to `.next/cache/images/` — this is what closed the
  disk-bloat root cause (#3287).

### 4.2 `X-Powered-By` info leak

Disable with `poweredByHeader: false` in `next.config.mjs`. ([Next.js — `poweredByHeader`](https://nextjs.org/docs/app/api-reference/config/next-config-js/poweredByHeader))

### 4.3 Health-checks / liveness

`server.js` doesn't expose a built-in health endpoint. Use `GET /` and check for
200 — Traefik does not currently do this. Add an `app/api/health/route.ts` if
we ever move to orchestrators that require an explicit liveness probe.

### 4.4 Env vars — build-time vs runtime

`NEXT_PUBLIC_*` variables are baked in at `next build` time. Standalone
builds are immutable; you cannot change a `NEXT_PUBLIC_` after the fact without
rebuilding. The GitLab CI build is the right injection point. ([Next.js — Environment variables](https://nextjs.org/docs/app/guides/environment-variables))

## 5. Implementation checklist — apply to pilot.quantflow.studio

Reordered after the 2026-06-03 baseline. Two PRs, in this order:
- **PR 1 — Lighthouse-100 blockers** (a11y + console + SEO + favicon).
- **PR 2 — Defence in depth** (cache + compression + security headers).

Per `CLAUDE.md`, both flow through Pilot (interactive session = plan, not implement).

### PR 1 — `perf(docs): close Lighthouse 100% gaps — a11y, favicon, link text, contrast`

| # | Pri | Change | File | Closes Lighthouse audit |
|---|---|---|---|---|
| 1 | P0 | Add `public/favicon.ico` + `public/favicon.svg` (export from `logo.svg`) | `docs/public/favicon.{ico,svg}` | `errors-in-console` + `has-favicon` |
| 2 | P0 | Rewrite `[learn more]`/`[Learn more →]` in `content/index.mdx:63,84` and `content/getting-started/configuration.mdx:271,349` to descriptive text (e.g. `[Learn how epic decomposition works →](/features/epic-decomposition)`) | content MDX | `link-text` (SEO) |
| 3 | P0 | Replace `opacity: 0.5` on the version badge with a full-opacity gray that passes 4.5:1 (`color: '#6b7280'` / `text-gray-500`) — keep `font-size: 0.5em` | `docs/app/layout.tsx:48` | `color-contrast` (desktop) |
| 4 | P0 | Add `width={24}` to `<img src="/logo.svg" alt="Pilot" height={24} …>` (currently only `height`) | `docs/app/layout.tsx:46` | `unsized-images`, CLS |
| 5 | P0 | Post-hydration a11y backfill — small client script in `app/layout.tsx` to set `aria-label` on Nextra's icon-only listbox buttons and the Discord `chatLink` `<a>`, until upstream Nextra is fixed (see §A11y notes below) | `docs/app/layout.tsx` (Script with `strategy="afterInteractive"`) | `button-name`, `link-name` |
| 6 | P1 | Add `metadataBase: new URL('https://pilot.quantflow.studio')` + sitewide `metadata` defaults | `docs/app/layout.tsx` | `canonical`, OG completeness |
| 7 | P1 | Add `app/robots.ts` + `app/sitemap.ts` | `docs/app/` | SEO completeness |

**A11y backfill snippet (PR 1, item 5):** Pilot should ship a `<Script>` block in `app/layout.tsx` like:

```tsx
import Script from 'next/script'
…
<Script id="nextra-a11y-backfill" strategy="afterInteractive">{`
  // Workaround for upstream Nextra v4.6.1 missing aria-labels.
  // Safe to remove once nextra/nextra#<issue-id> is fixed.
  (function fix() {
    document.querySelectorAll('button[aria-haspopup="listbox"]:not([aria-label])')
      .forEach(b => b.setAttribute('aria-label', b.title || 'Options'));
    document.querySelectorAll('header.nextra-navbar a[href*="discord.gg"]:not([aria-label])')
      .forEach(a => a.setAttribute('aria-label', 'Discord community'));
  })();
  // Re-apply after route changes (Nextra is SPA).
  if (typeof window !== 'undefined') {
    const mo = new MutationObserver(fix);
    mo.observe(document.body, { childList: true, subtree: true });
  }
`}</Script>
```

**Verification commands (PR 1):**

```bash
# favicon present
curl -sSI https://pilot.quantflow.studio/favicon.ico | head -1   # expect 200, not 404

# Lighthouse — repeat the baseline
npx -y lighthouse@13.3.0 https://pilot.quantflow.studio/ --form-factor=mobile  --output=json --output-path=./mobile-after
npx -y lighthouse@13.3.0 https://pilot.quantflow.studio/ --preset=desktop      --output=json --output-path=./desktop-after
# Targets after PR 1: 100/100/100/100 (mobile) · ~100/100/100/100 (desktop, perf may stay at 96 until PR 2 compression lands)
```

### PR 2 — `perf(docs): cache + compression + security headers (defence in depth)`

| # | Pri | Change | File | Closes Lighthouse audit |
|---|---|---|---|---|
| 8 | P1 | Add Traefik gzip `compress` middleware to docs router | `docs/docker-compose.prod.yml` labels | `uses-text-compression`, helps LCP |
| 9 | P1 | `headers()` for HTML: `public, max-age=0, must-revalidate` | `docs/next.config.mjs` | (TTFB / repeat-visit perceived perf) |
| 10 | P1 | `headers()` for `public/*` images / fonts: `public, max-age=604800, stale-while-revalidate=86400` | `docs/next.config.mjs` | `uses-long-cache-ttl` |
| 11 | P1 | `headers()` for `favicon.ico`: `public, max-age=31536000` | `docs/next.config.mjs` | – |
| 12 | P1 | Traefik `headers` middleware: HSTS, X-Content-Type-Options=nosniff, Referrer-Policy=strict-origin-when-cross-origin, Permissions-Policy minimal-deny, CSP **report-only first** | `docs/docker-compose.prod.yml` labels | `has-hsts`, `csp-xss` (already at 96, this hardens) |
| 13 | P2 | `poweredByHeader: false` + `compress: false` (explicit) | `docs/next.config.mjs` | – |

**Verification commands:**

```bash
# compression
curl -sSI -H 'Accept-Encoding: gzip,br' https://pilot.quantflow.studio/ | grep -i content-encoding

# cache headers — HTML
curl -sSI https://pilot.quantflow.studio/ | grep -i cache-control

# cache headers — public asset
curl -sSI https://pilot.quantflow.studio/logo.svg | grep -i cache-control

# security headers
curl -sSI https://pilot.quantflow.studio/ | grep -iE 'strict-transport|content-security|x-content-type|referrer-policy|permissions-policy'

# favicon
curl -sSI https://pilot.quantflow.studio/favicon.ico

# lighthouse (CLI)
npx lighthouse https://pilot.quantflow.studio/ --preset=desktop --quiet --chrome-flags="--headless"
npx lighthouse https://pilot.quantflow.studio/ --form-factor=mobile --quiet --chrome-flags="--headless"
```

Diff size estimate: ~30 LOC across 3 files. No runtime behavior change; deploy
via the standard `sync-docs.yml` → GitLab → `prod-*` tag path.

### PR 2 — `perf(docs): build-time image optimization pipeline`

| # | Change | File | Closes |
|---|---|---|---|
| P0-4 | New `scripts/optimize-images.mjs` using [`sharp`](https://sharp.pixelplumbing.com/) to walk `public/` + every MDX-referenced image, recompress, and emit `.webp` + `.avif` siblings (skip if already exists or source is SVG) | new `docs/scripts/optimize-images.mjs` | `uses-optimized-images`, `modern-image-formats`, LCP |
| P1-10 | Same script: also emit `.gz` and `.br` siblings of HTML in `out/` for Traefik's `compress` to short-circuit on (gzip) and seed Traefik v3 brotli once upgraded | extend the script | (defensive, future-proof) |
| – | Wire as `prebuild` in `package.json` and add `sharp` to devDependencies | `docs/package.json` | – |
| – | If `sharp` requires libvips at build time and `oven/bun:1-alpine` lacks the libs, switch the builder stage to `node:24-bookworm-slim` | `docs/docker/Dockerfile` | – |

**Verification:**

```bash
# bytes shipped for the OG image, before / after
curl -sSI https://pilot.quantflow.studio/pilot-preview.png | grep -i content-length
# lighthouse delta on the homepage (LCP)
npx lighthouse https://pilot.quantflow.studio/ --form-factor=mobile --quiet --only-categories=performance
```

### Out-of-repo follow-ups (track under issue #3380)

- Upgrade Traefik v2.11 → v3.x (unlocks native brotli + better `compress`
  middleware). One-line image tag change in *the prod stack*, not our compose.
- GitLab Container Registry retention policy.
- Schedule periodic `docker builder prune` on the host (already addressed once
  this session — needs a systemd timer to prevent recurrence).
- (Optional) HTTP/3 at Traefik.

## Anti-patterns / pitfalls

- **Do not** bind-mount `/data/quantflow/pilot_cache → /app/.next/cache` again.
  It re-introduces the disk-bloat vector (PR #3373 removed it). ([[reference_docs_deploy_pipeline]])
- **Do not** rely on `s-maxage` until we put a CDN in front. It's silent
  no-op for browsers.
- **Do not** set `compress: true` and expect standalone to honour it. It won't.
- **Do not** set CSP without first running `Content-Security-Policy-Report-Only`
  for a full day. Nextra theme inlines styles; a strict CSP will blank-screen
  the docs if applied untested.
- **Do not** override `/_next/static/**` headers in `headers()` — Next blocks
  it and silently no-ops your config.
- **Do not** re-enable `images.unoptimized: false` without first deciding where
  the optimizer cache lives (custom `cacheHandler` → Redis, or no persistence,
  or accept the slow disk growth and add a janitor).

## Source map

| Claim | Source |
|---|---|
| Next 15 fetch is uncached by default | [Next.js 15 upgrade guide — caching semantics](https://nextjs.org/docs/app/guides/upgrading/version-15#caching-semantics) |
| The 4 caches: Request Memo / Data / Full Route / Router | [Next.js — Caching deep-dive](https://nextjs.org/docs/app/deep-dive/caching) |
| `/_next/static/**` headers immutable + overridable status | [Next.js — `headers` reference](https://nextjs.org/docs/app/api-reference/config/next-config-js/headers) |
| `s-maxage` is shared-cache only | [MDN — Cache-Control: s-maxage](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Cache-Control#s-maxage) |
| `must-revalidate` semantics | [MDN — Cache-Control: must-revalidate](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Cache-Control#must-revalidate) |
| HTTP cache policy guidance | [web.dev — HTTP caching](https://web.dev/articles/http-cache) |
| Next standalone does not compress | [Next.js — Self-hosting: compression](https://nextjs.org/docs/app/guides/self-hosting#compression) |
| Next ISR self-hosted writes `.next/cache` | [Next.js — Self-hosting: caching and ISR](https://nextjs.org/docs/app/guides/self-hosting#caching-and-isr) |
| Custom cache handler for self-hosted ISR | [Next.js — Configuring caching](https://nextjs.org/docs/app/guides/self-hosting#configuring-caching) |
| Traefik v2.11 gzip-only compress | [Traefik v2.11 — Compress middleware](https://doc.traefik.io/traefik/v2.11/middlewares/http/compress/) |
| Traefik brotli is v3+ | [Traefik PR #9387 — Add brotli](https://github.com/traefik/traefik/pull/9387) |
| `poweredByHeader` reference | [Next.js — `poweredByHeader`](https://nextjs.org/docs/app/api-reference/config/next-config-js/poweredByHeader) |
| HSTS minimum + preload | [MDN — HSTS](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Strict-Transport-Security) · [hstspreload.org](https://hstspreload.org/) |
| Strict CSP guidance | [web.dev — Strict CSP](https://web.dev/articles/strict-csp) |
| WCAG 2.2 AA | [W3C — WCAG 2.2](https://www.w3.org/TR/WCAG22/) |
| Lighthouse audit catalog & scoring | [Lighthouse scoring docs](https://github.com/GoogleChrome/lighthouse/blob/main/docs/scoring.md) |
| Next.js Image config + minimumCacheTTL | [Next.js — Image config](https://nextjs.org/docs/app/api-reference/components/image) |
| Next.js Font API | [Next.js — Font optimization](https://nextjs.org/docs/app/api-reference/components/font) |
| Next.js robots / sitemap conventions | [robots](https://nextjs.org/docs/app/api-reference/file-conventions/metadata/robots) · [sitemap](https://nextjs.org/docs/app/api-reference/file-conventions/metadata/sitemap) |
| Next.js metadata API + openGraph | [Generate metadata](https://nextjs.org/docs/app/api-reference/functions/generate-metadata) |
| Nextra v4 theme reference | [Nextra — Docs theme](https://nextra.site/docs/docs-theme) |

## Maintenance

Re-measure the "Live state" table after every significant docs/Nextra/Next
upgrade. Run a Lighthouse pass on `pilot.quantflow.studio/` (homepage) +
`/getting-started/quickstart` (a representative content page) and record the
four scores at the top of this file.
