import type { Metadata } from 'next'
import Script from 'next/script'
import { Footer, Layout, Navbar } from 'nextra-theme-docs'
import { Head } from 'nextra/components'
import { getPageMap } from 'nextra/page-map'
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
                  <img src="/logo.svg" alt="Pilot" width={108} height={24} style={{ height: 24, width: 'auto', alignSelf: 'center' }} />
                  <span style={{ fontSize: '0.5em', color: '#6b7280', fontWeight: 400 }}>v2.166.13</span>
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
        {/* TODO(nextra-upstream): remove once Nextra labels its copy-page split-button and chat
            icon link — https://github.com/shuding/nextra/issues/3826 */}
        <Script
          id="nextra-aria-backfill"
          strategy="afterInteractive"
          dangerouslySetInnerHTML={{
            __html: `
              function patchAriaLabels() {
                document.querySelectorAll('a[href*="discord.gg"]:not([aria-label])').forEach(function(a) {
                  a.setAttribute('aria-label', 'Discord community');
                });
                document.querySelectorAll('button:not([aria-label])').forEach(function(btn) {
                  if (!btn.textContent.trim() && btn.querySelector('svg')) {
                    var label = btn.getAttribute('title') || btn.getAttribute('data-state') === 'closed' && 'Copy page';
                    if (label) btn.setAttribute('aria-label', label);
                  }
                });
              }
              patchAriaLabels();
              new MutationObserver(patchAriaLabels).observe(document.body, { childList: true, subtree: true });
            `,
          }}
        />
      </body>
    </html>
  )
}
