import type { Metadata } from 'next'
import { Footer, Layout, Navbar } from 'nextra-theme-docs'
import { Head } from 'nextra/components'
import { getPageMap } from 'nextra/page-map'
import Script from 'next/script'
import 'nextra-theme-docs/style.css'
import './globals.css'

export const metadata: Metadata = {
  metadataBase: new URL('https://pilot.quantflow.studio'),
  title: 'Pilot — AI That Ships Your Tickets',
  description: 'Autonomous AI development pipeline that turns tickets into pull requests',
  openGraph: {
    type: 'website',
    title: 'Pilot — AI That Ships Your Tickets',
    description: 'Autonomous AI development pipeline. Label a ticket, get a PR. Self-hosted, source-available.',
    url: 'https://pilot.quantflow.studio',
    images: [
      {
        url: 'https://pilot.quantflow.studio/pilot-preview.png',
        width: 1200,
        height: 630,
      },
    ],
    siteName: 'Pilot Docs',
  },
  twitter: {
    card: 'summary_large_image',
    title: 'Pilot — AI That Ships Your Tickets',
    description: 'Autonomous AI development pipeline. Label a ticket, get a PR. Self-hosted, source-available.',
    images: ['https://pilot.quantflow.studio/pilot-preview.png'],
  },
}

export default async function RootLayout({
  children,
}: {
  children: React.ReactNode
}) {
  return (
    <html lang="en" dir="ltr" suppressHydrationWarning>
      <Head />
      <body>
        <Layout
          navbar={
            <Navbar
              logo={
                <span style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
                  <img src="/logo.svg" alt="Pilot" height={24} width={108} style={{ height: 24, width: 'auto', alignSelf: 'center' }} />
                  <span style={{ fontSize: '0.5em', color: '#6b7280', fontWeight: 400 }}>v2.186.3</span>
                </span>
              }
              projectLink="https://github.com/qf-studio/pilot"
              chatLink="https://discord.gg/Hsz63MTB3c"
            />
          }
          pageMap={await getPageMap()}
          footer={<Footer />}
        >
          {children}
        </Layout>
        {/*
          TODO(nextra-upstream): Nextra v4.6.1 renders the copy-page split-button
          and the Discord icon link without accessible names, failing the Lighthouse
          `button-name` and `link-name` audits. This script backfills aria-labels
          client-side until upstream fixes land.
          Filed: https://github.com/shuding/nextra/issues/3721
        */}
        <Script id="a11y-backfill" strategy="afterInteractive">{`
          (function backfillA11y() {
            function applyLabels() {
              // Discord / chat icon link injected by Nextra Navbar chatLink prop
              document.querySelectorAll('a[href*="discord.gg"]:not([aria-label])').forEach(function(el) {
                el.setAttribute('aria-label', 'Join the Pilot Discord community');
              });
              // Copy-page split-button: unlabelled icon-only button in Nextra v4.6.1
              document.querySelectorAll('button:not([aria-label]):not([aria-labelledby])').forEach(function(el) {
                if (el.textContent.trim() === '' && el.querySelector('svg')) {
                  el.setAttribute('aria-label', 'Copy page link');
                }
              });
            }
            applyLabels();
            var observer = new MutationObserver(applyLabels);
            observer.observe(document.body, { childList: true, subtree: true });
          })();
        `}</Script>
      </body>
    </html>
  )
}
