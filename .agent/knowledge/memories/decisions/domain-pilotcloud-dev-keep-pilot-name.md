---
name: domain-pilotcloud-dev-keep-pilot-name
description: Founder decision 2026-09-21 — hosted product ships as "Pilot Cloud" on `pilotcloud.dev` (console./api.); marketing+docs stay on pilot.quantflow.studio; the OSS name "Pilot" is kept (686 stars, page-one on the intent query); premium `pilot.build` ($720/yr) rejected because no domain buys search for a generic word Pilot.com already owns
type: decision
created: 2026-09-21
---

# Domain: `pilotcloud.dev`; keep the name "Pilot"; no rename of the hosted product

**Decided 2026-09-21** after three weeks parked (since 09-01). Sent to Nelya the same day
(`#infrastructure`, `1789989147.511449`) to register in Route 53 and start phase 2
(`-domain`, `-domain-config`: ALB + ACM + CloudFront SPA).

## The pick

- **`pilotcloud.dev`** — ~$15/yr, available at the .dev registry (checked registry-direct RDAP
  2026-09-21). Matches the roadmap product name "Pilot Cloud". `.dev` is HSTS-preloaded →
  HTTPS-only by default.
- Hosts: `console.pilotcloud.dev` (SPA) · `api.pilotcloud.dev` (console-api). Nelya may front both
  under one origin so the `__Host-` session cookie stays same-origin (CloudFront `/api/*` → ALB).
- **Marketing + docs stay on `pilot.quantflow.studio`** — it already ranks and sits behind her
  CloudFront.
- The domain also becomes the auth-service OIDC issuer for the console app and the Paddle
  webhook host (values to send once the API host answers).

## Why not the alternatives

- **`pilot.build` ($720/yr premium)** — search showed no domain buys the word "pilot": `pilot` →
  aviation/Honda/Pilot.com; `pilot AI` → Pilot.com's bookkeeping feature + pilot.ai; the query
  that finds us (`pilot AI coding agent ships tickets`) already ranks `qf-studio/pilot` #3 and
  `pilot.quantflow.studio` on page one — discoverability comes from content, not the TLD.
- **`pilothq.dev`** — rejected: `pilothq` is Pilot.com's handle on X/Instagram/LinkedIn/Greenhouse;
  we'd look like their dev site.
- **`usepilot.dev`, `pilot.engineering`** — available, fine, less on-brand / longer.
- Taken: `pilot.dev`, `pilotai.dev`, `pilotcloud.com`, `usepilot.io`, `trypilot.io`.

## Rename considered and rejected

`pilot-ai-5.polsia.app` ("Pilot — AI Coding Agent", same pitch) turned out to be an
AI-generated placeholder from Polsia (auto-fabricated companies on subdomains; placeholder
repo `yourorg/repo`, invented stats, Polsia's own $49 price pasted in). Not a competitor, no
code, nothing to act on. The lesson is that the name is trivially cloneable, which is a
branding fact, not a reason to throw away the OSS brand. See
[[trademark-clearance-pilot-us-2026-09]] for the registrability read.

**How to apply:** all new hosted-console URLs, OIDC issuer, ACM certs, SES sending domain
(if moved off quantflow.studio), Paddle webhook destination → `pilotcloud.dev`. Do not resurrect
the domain question; if the brand ever changes it is a separate decision with the trademark
memory as input.
