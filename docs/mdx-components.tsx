import type { MDXComponents } from 'mdx/types'
import { Callout, Tabs } from 'nextra/components'

export function useMDXComponents(components: MDXComponents): MDXComponents {
  return {
    Callout,
    Tabs,
    ...components,
  }
}
