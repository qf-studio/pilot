import nextra from 'nextra'

const withNextra = nextra({})

export default withNextra({
  output: 'standalone',
  // Docs ship static, pre-sized assets — the on-server image optimizer adds no
  // value but writes optimized variants to .next/cache/images, which is bind-
  // mounted to a persistent host volume in prod (docker-compose.prod.yml) and
  // grows unbounded, exhausting disk. Disable it so the runtime cache stays flat.
  images: {
    unoptimized: true,
  },
})
