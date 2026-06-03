import nextra from 'nextra'

const withNextra = nextra({})

export default withNextra({
  output: 'standalone',
  poweredByHeader: false,
  // Compression is handled by the Traefik compress middleware; standalone
  // server.js does not honour this flag anyway (Next.js self-hosting docs §2.5).
  compress: false,
  // Docs ship static, pre-sized assets — the on-server image optimizer adds no
  // value but writes optimized variants to .next/cache/images, which is bind-
  // mounted to a persistent host volume in prod (docker-compose.prod.yml) and
  // grows unbounded, exhausting disk. Disable it so the runtime cache stays flat.
  images: {
    unoptimized: true,
  },
  async headers() {
    return [
      {
        // All routes except /_next/static (immutable, content-hashed by Next)
        // and favicon paths: force browser revalidation on every visit.
        // ETag already set by Next saves the body on unchanged pages (→ 304).
        // Image/font rules below override Cache-Control for those asset types.
        source: '/((?!_next/static|_next/image|favicon).*)',
        headers: [
          { key: 'Cache-Control', value: 'public, max-age=0, must-revalidate' },
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
