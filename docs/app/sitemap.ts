import type { MetadataRoute } from 'next'
import { readdirSync, statSync } from 'fs'
import { join } from 'path'

function collectPages(dir: string, base = ''): string[] {
  const entries = readdirSync(dir)
  const pages: string[] = []
  for (const entry of entries) {
    if (entry.startsWith('_') || entry.startsWith('.')) continue
    const full = join(dir, entry)
    const rel = base ? `${base}/${entry}` : entry
    if (statSync(full).isDirectory()) {
      pages.push(...collectPages(full, rel))
    } else if (/\.(mdx?|tsx?)$/.test(entry) && !entry.startsWith('_meta')) {
      pages.push(rel.replace(/\.(mdx?|tsx?)$/, '').replace(/\/index$/, ''))
    }
  }
  return pages
}

export default function sitemap(): MetadataRoute.Sitemap {
  const base = 'https://pilot.quantflow.studio'
  const contentDir = join(process.cwd(), 'content')
  const pages = collectPages(contentDir)

  return [
    { url: base, lastModified: new Date(), changeFrequency: 'weekly', priority: 1 },
    ...pages
      .filter((p) => p !== 'index')
      .map((page) => ({
        url: `${base}/${page}`,
        lastModified: new Date(),
        changeFrequency: 'monthly' as const,
        priority: 0.8,
      })),
  ]
}
