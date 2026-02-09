import React from 'react'
import { DocsThemeConfig } from 'nextra-theme-docs'

const config: DocsThemeConfig = {
  logo: <span style={{ fontWeight: 800 }}>Pilot</span>,
  project: {
    link: 'https://github.com/alekspetrov/pilot',
  },
  docsRepositoryBase: 'https://github.com/alekspetrov/pilot/tree/main/docs',
  footer: {
    text: (
      <span>
        Pilot — AI that ships your tickets. Built by{' '}
        <a href="https://quantflow.studio" target="_blank" rel="noopener noreferrer">
          QuantFlow Studio
        </a>
      </span>
    ),
  },
  useNextSeoProps() {
    return {
      titleTemplate: '%s – Pilot Docs'
    }
  },
  head: (
    <>
      <meta name="viewport" content="width=device-width, initial-scale=1.0" />
      <meta name="description" content="Pilot documentation — autonomous AI development pipeline that turns tickets into pull requests" />
      <meta property="og:type" content="website" />
      <meta property="og:title" content="Pilot — AI That Ships Your Tickets" />
      <meta property="og:description" content="Autonomous AI development pipeline. Label a ticket, get a PR. Self-hosted, source-available." />
      <meta property="og:url" content="https://pilot.quantflow.studio" />
      <meta property="og:image" content="https://pilot.quantflow.studio/pilot-preview.png" />
      <meta property="og:site_name" content="Pilot Docs" />
      <meta name="twitter:card" content="summary_large_image" />
      <meta name="twitter:title" content="Pilot — AI That Ships Your Tickets" />
      <meta name="twitter:description" content="Autonomous AI development pipeline. Label a ticket, get a PR. Self-hosted, source-available." />
      <meta name="twitter:image" content="https://pilot.quantflow.studio/pilot-preview.png" />
      <link rel="icon" href="/favicon.ico" />
    </>
  ),
  banner: {
    key: 'pilot-v025',
    text: (
      <a href="https://github.com/alekspetrov/pilot/releases" target="_blank" rel="noopener noreferrer">
        Pilot v0.25 — email &amp; PagerDuty alerts, Jira webhooks, outbound webhooks. Read the changelog →
      </a>
    ),
  },
  primaryHue: 220,
  sidebar: {
    defaultMenuCollapseLevel: 2,
    toggleButton: true,
  },
  toc: {
    backToTop: true,
  },
  navigation: {
    prev: true,
    next: true,
  },
}

export default config
