import { useMDXComponents as getNextraComponents } from 'nextra-theme-docs'
import { CurrentVersion } from './components/current-version'

const sharedComponents = { CurrentVersion }

export function useMDXComponents(components?: Record<string, unknown>) {
  return {
    ...getNextraComponents(),
    ...sharedComponents,
    ...components,
  }
}
