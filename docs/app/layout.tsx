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
  icons: {
    icon: [
      { url: '/favicon.ico' },
      { url: '/favicon.svg', type: 'image/svg+xml' },
    ],
    shortcut: '/favicon.ico',
  },
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
        {/* TODO(nextra-upstream): Remove when Nextra adds native aria-label support.
            Nextra v4.6.1 omits aria-label on the Copy-page split-button and the Discord
            icon link in the navbar. Upstream issues:
              - button-name: https://github.com/shuding/nextra/issues/3823
              - link-name:   https://github.com/shuding/nextra/issues/3824 */}
        <Script id="nextra-aria-backfill" strategy="afterInteractive">{`
          (function () {
            function backfillAriaLabels() {
              // Copy-page split-button (Nextra renders a <button> without aria-label)
              document.querySelectorAll('button[class*="CopyButton"], button[data-nextra-icon]').forEach(function (btn) {
                if (!btn.getAttribute('aria-label')) btn.setAttribute('aria-label', 'Copy page link');
              });
              // Discord icon link in the Navbar chatLink slot
              document.querySelectorAll('a[href*="discord"]').forEach(function (a) {
                if (!a.getAttribute('aria-label')) a.setAttribute('aria-label', 'Join Discord');
              });
            }
            backfillAriaLabels();
            var observer = new MutationObserver(backfillAriaLabels);
            observer.observe(document.body, { childList: true, subtree: true });
          })();
        `}</Script>
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
      </body>
    </html>
  )
}
