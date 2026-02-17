import { notFound } from 'next/navigation'
import { useMDXComponents } from '@/mdx-components'
import * as fs from 'fs'
import * as path from 'path'

async function getMDXContent(pathname: string) {
  const contentPath = path.join(process.cwd(), 'content', ...pathname.split('/').filter(Boolean))

  // Try index.mdx first
  let mdxPath = path.join(contentPath, 'index.mdx')
  if (!fs.existsSync(mdxPath)) {
    // Try direct file.mdx
    mdxPath = contentPath + '.mdx'
  }

  if (!fs.existsSync(mdxPath)) {
    return null
  }

  const content = fs.readFileSync(mdxPath, 'utf-8')
  return content
}

export async function generateStaticParams() {
  // This will be used by Next.js to pre-render dynamic routes
  return []
}

export default async function Page({
  params,
}: {
  params: Promise<{ mdxPath?: string[] }>
}) {
  const { mdxPath = [] } = await params
  const pathname = '/' + (mdxPath?.join('/') || '')

  const content = await getMDXContent(pathname)

  if (!content) {
    notFound()
  }

  // Note: In a real implementation, you'd use MDX compilation here
  // For now, this is a placeholder
  return (
    <div>
      {/* MDX content would be rendered here */}
      Content for: {pathname}
    </div>
  )
}
