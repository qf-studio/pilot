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
                  <span style={{ fontSize: '0.5em', color: '#6b7280', fontWeight: 400 }}>v2.166.12</span>
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
        {/* TODO(nextra-upstream): Nextra 4.6.1 omits aria-labels on the Copy-page split-button
            and the Discord chat icon link — https://github.com/shuding/nextra/issues/3950 */}
        <Script strategy="afterInteractive">{`
          function patchNextraA11y() {
            document.querySelectorAll('a[href*="discord.gg"]').forEach(function(el) {
              if (!el.getAttribute('aria-label')) el.setAttribute('aria-label', 'Join our Discord community');
            });
            document.querySelectorAll('button').forEach(function(btn) {
              if (!btn.getAttribute('aria-label') && !btn.getAttribute('aria-labelledby') && !btn.textContent.trim()) {
                btn.setAttribute('aria-label', 'Open menu');
              }
            });
          }
          patchNextraA11y();
          new MutationObserver(patchNextraA11y).observe(document.body, { childList: true, subtree: true });
        `}</Script>
      </body>
    </html>
  )
}
