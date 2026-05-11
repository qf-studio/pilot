---
name: bench-cost-safety
description: Cost safety rules for bench runs — never pass ANTHROPIC_API_KEY to containers, use dedicated env vars
type: feedback
---

NEVER pass `ANTHROPIC_API_KEY` directly to bench containers via `--ae`.

Claude Code auto-detects `ANTHROPIC_API_KEY` and uses it for ALL API calls (including Opus). This bills Opus execution to pay-per-use API instead of the subscription OAuth token.

**Why:** Discovered 2026-03-23. A single k=5 submission (445 Opus calls) would cost $130-220 on API billing vs $0 on subscription.

**How to apply:**
1. Use `PILOT_CLASSIFIER_API_KEY` for the Haiku classifier API key
2. Pass to container: `--ae "PILOT_CLASSIFIER_API_KEY=$ANTHROPIC_API_KEY"`
3. CC only sees `CLAUDE_CODE_OAUTH_TOKEN` → uses subscription
4. Our Go classifier reads `PILOT_CLASSIFIER_API_KEY` → uses API for Haiku only (~$0.09/run)

**Cost breakdown per k=5 run:**
- Modal compute: ~$20-30
- Opus via subscription: $0 (covered by Claude Max)
- Haiku classifier via API: ~$0.09 (89 tasks × $0.001)
- Total: ~$20-30

**WRONG (expensive):**
```bash
--ae "ANTHROPIC_API_KEY=$KEY"  # CC uses this for Opus = $200+
```

**RIGHT (cheap):**
```bash
--ae "PILOT_CLASSIFIER_API_KEY=$KEY"  # Only classifier uses this
```
