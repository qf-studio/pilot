import { generateStaticParamsFor, importPage } from 'nextra/pages'
import { useMDXComponents } from 'nextra-theme-docs'

export const generateStaticParams = generateStaticParamsFor('mdxPath')

export async function generateMetadata(props: PageProps) {
  const params = await props.params
  const { metadata } = await importPage(params.mdxPath)

  // The root page has no frontmatter `title` and no H1, so Nextra falls back
  // to a filename-derived title ("Index") that would otherwise clobber the
  // site default from app/layout.tsx once a title.template is in play.
  // `absolute` bypasses the template so it renders exactly as the default.
  if (!params.mdxPath || params.mdxPath.length === 0) {
    return { ...metadata, title: { absolute: 'Pilot — AI That Ships Your Tickets' } }
  }

  return metadata
}

const { wrapper: Wrapper } = useMDXComponents()

export default async function Page(props: PageProps) {
  const params = await props.params
  const { default: MDXContent, ...rest } = await importPage(params.mdxPath)

  return (
    <Wrapper {...rest}>
      <MDXContent {...props} params={params} />
    </Wrapper>
  )
}

type PageProps = {
  params: Promise<{
    mdxPath?: string[]
  }>
}
