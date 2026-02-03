import React from 'react'
import { DocsThemeConfig } from 'nextra-theme-docs'

const config: DocsThemeConfig = {
  logo: <span style={{ fontWeight: 800 }}>🚀 Pilot</span>,
  project: {
    link: 'https://github.com/alekspetrov/pilot',
  },
  docsRepositoryBase: 'https://github.com/alekspetrov/pilot/tree/main/docs',
  footer: {
    text: 'Pilot - AI that ships your tickets',
  },
  useNextSeoProps() {
    return {
      titleTemplate: '%s – Pilot Docs'
    }
  },
  head: (
    <>
      <meta name="viewport" content="width=device-width, initial-scale=1.0" />
      <meta name="description" content="Pilot - AI that ships your tickets, powered by Navigator" />
    </>
  ),
  primaryHue: 220,
  sidebar: {
    defaultMenuCollapseLevel: 1,
    toggleButton: true,
  },
  toc: {
    backToTop: true,
  },
}

export default config
