import { generateStaticParamsFor, importPage } from 'nextra/pages'
import { useMDXComponents } from 'nextra-theme-docs'

export const generateStaticParams = generateStaticParamsFor('mdxPath')

export async function generateMetadata(props: PageProps) {
  const params = await props.params
  const { metadata } = await importPage(params.mdxPath)
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
