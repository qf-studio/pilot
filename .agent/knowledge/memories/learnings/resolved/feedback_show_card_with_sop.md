> **RESOLVED/SUPERSEDED (2026-07-05):** STALE: pilot-bench/bench-status.py no longer exists; bench dormant

---
name: Show bench card via SOP script
description: When asked to "check" or "show results", always run bench-status.py — never build inline cards
type: feedback
originSessionId: aeaf0f5e-b335-4bda-bd0e-ec1e09d65a61
---
When user asks to see bench results, status, or "show me the card", run `python3 pilot-bench/bench-status.py` directly. Don't build inline ASCII cards manually.

**Why:** User wants the real SOP dashboard, not ad-hoc grep output. The script auto-detects the latest `/tmp/bench-v*.log` for AWS runs.

**How to apply:** Any request like "check", "show results", "show card", "status" → run the SOP script. If it's broken or showing stale data, fix the script rather than building a workaround.
