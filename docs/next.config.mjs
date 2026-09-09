import nextra from 'nextra'

const withNextra = nextra({})

export default withNextra({
  output: 'standalone',
  poweredByHeader: false,
  // pilot.quantflow.studio is served through CloudFront (since 2026-09-06);
  // the edge only compresses uncompressed origin responses, so leaving Next's
  // own compression off is what lets CloudFront apply Brotli. standalone
  // server.js does not honour this flag anyway (Next.js self-hosting docs §2.5).
  compress: false,
  // Docs ship static, pre-sized assets — the on-server image optimizer adds no
  // value but writes optimized variants to .next/cache/images, which is bind-
  // mounted to a persistent host volume in prod and grows unbounded, exhausting
  // disk. Disable it so the runtime cache stays flat.
  images: {
    unoptimized: true,
  },
  async headers() {
    return [
      {
        // All routes except /_next/static (immutable, content-hashed by Next)
        // and favicon paths: force browser revalidation on every visit
        // (max-age=0 — ETag already set by Next saves the body on unchanged
        // pages via 304), while letting the CloudFront edge cache the page
        // for a day (s-maxage=86400) and serve stale for an hour during
        // revalidation. Deploys invalidate CloudFront's cache anyway, so this
        // doesn't risk serving outdated content after a release.
        // Image/font rules below override Cache-Control for those asset types.
        source: '/((?!_next/static|_next/image|favicon).*)',
        headers: [
          {
            key: 'Cache-Control',
            value: 'public, max-age=0, s-maxage=86400, stale-while-revalidate=3600',
          },
        ],
      },
      {
        // Public images and fonts: one-week browser cache + 1-day SWR.
        // Overrides the must-revalidate rule above for these file types.
        source: '/:all*(svg|png|jpg|jpeg|gif|webp|avif|ico|woff2?)',
        headers: [
          { key: 'Cache-Control', value: 'public, max-age=604800, stale-while-revalidate=86400' },
        ],
      },
      {
        // Favicons: cache aggressively — browsers refetch them on every tab
        // open without a long TTL. Overrides the ico match from the rule above.
        source: '/favicon.ico',
        headers: [
          { key: 'Cache-Control', value: 'public, max-age=31536000' },
        ],
      },
    ]
  },
})
