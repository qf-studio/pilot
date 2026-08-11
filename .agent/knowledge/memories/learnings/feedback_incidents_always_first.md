---
name: Incidents are always first
description: Founder directive 2026-08-11 — open incidents outrank every other thread (design, reviews, archives) at session start
type: feedback
---

When a session starts (or a marker is restored) with an open incident, work the incident before any other thread — including the marker's "stated next steps".

**Why:** Founder stated it verbatim ("Incidents are always first") when a session summary led with design work while PR#4846 sat stranded on a bogus CI timeout. An open incident means the pipeline is degraded or evidence is decaying; design/review/archive threads keep.

**How to apply:** In session-start summaries and planning, list open incidents at the top and begin work on them immediately (diagnosis → resolution decision → defect issue), even if the previous marker names a different "next thread". Only founder-gated decisions inside the incident wait for the founder; everything else proceeds.
