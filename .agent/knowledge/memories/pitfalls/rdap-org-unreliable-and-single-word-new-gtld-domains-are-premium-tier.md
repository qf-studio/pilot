---
name: rdap-org-unreliable-and-single-word-new-gtld-domains-are-premium-tier
description: rdap.org returned 404 for names that ARE registered (usepilot.io, trypilot.io had live NS) — use the IANA bootstrap (data.iana.org/rdap/dns.json) to hit each TLD's authoritative RDAP, or `whois -h whois.nic.<tld>`. And 'unregistered' ≠ 'registrable': single dictionary words on new gTLDs are registry premium-tier — pilot.build is $719.99/yr renewal, pilot.online $6,399.99 — which some registrar UIs surface as 'can't register'. Check the price at a registrar that shows tiers (Hover does) before recommending. The domain becomes the OIDC issuer baked into tenant tokens — the sticky choice.
type: pitfall
---

# Pitfall: domain availability — rdap.org lies, and 'available' can mean $720/yr

**What happened (2026-09-01→03):** the 09-01 shortlist declared `pilot.build` "available (RDAP)" and recommended it. The founder could not register it at the first registrar. Re-check: `whois -h whois.nic.build pilot.build` → `DOMAIN NOT FOUND` (truly unregistered), but Hover showed it as **tiered: $719.99/yr renewal**; `pilot.online` $6,399.99. Meanwhile the same rdap.org pass returned 404 for `usepilot.io` and `trypilot.io`, both of which have live nameservers (`registrar-servers.com`) — i.e. registered. rdap.org is not authoritative and rate-limits/404s unpredictably.

**Do instead:**
1. `curl https://data.iana.org/rdap/dns.json` → find the TLD's RDAP base (e.g. `.build` → `https://rdap.centralnic.com/build/`, `.dev` → `https://pubapi.registry.google/rdap/`, `.com` → Verisign, `.engineering` → Identity Digital) → `GET <base>domain/<name>`; an `objectClassName: "error"` / 404 there is a real not-found.
2. Cross-check with `dig NS` (NS present ⇒ registered, absent ⇒ inconclusive).
3. Then check the PRICE at a registrar that exposes premium tiers (Hover shows "This is a tiered price domain"); single-word names on new gTLDs are usually reserved or premium.

**Verified 2026-09-03:** `pilot.engineering` $82.99 · `pilothq.dev`/`usepilot.dev` ~$15 · `shipwithpilot.com` ~$15 · `pilot.new` $599.99 AND Google's .new policy requires a creation-flow landing.

**Why it matters here:** the domain feeds ACM, SES, the UI origin — all re-pointable — and the auth-service `OIDC_ISSUER_URL` baked into every tenant token, which is the one that hurts to migrate. Decide on price, not availability.
