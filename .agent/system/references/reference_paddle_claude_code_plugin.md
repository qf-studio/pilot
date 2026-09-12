---
name: reference_paddle_claude_code_plugin
description: Paddle's official Claude Code plugin (paddle@claude-community) — install commands, what it ships (10 skills, 3 MCP servers), the sandbox-key config step, and how Navigator sessions use it for TASK-497.
type: reference
---

# Paddle Claude Code plugin (`paddle@claude-community`)

**Installed**: 2026-09-12 on the founder laptop, user scope (`~/.claude/plugins/cache/claude-community/paddle/<version>/`). Not installed on the box; Pilot executor sessions do not have it.

**Setup guide**: https://developer.paddle.com/get-started/ai/claude-code/ · repo https://github.com/PaddleHQ/paddle-agent-skills (Apache-2.0)

```bash
claude plugin marketplace add anthropics/claude-plugins-community
claude plugin install paddle@claude-community
# then, once a sandbox API key exists (sensitive; stored by Claude Code):
/plugin configure paddle@claude-community      # userConfig key: sandbox_api_key (pdl_sdbx_…)
```

## What it ships
- **Skills (10)**: billing-history, catalog-setup, checkout-web, customer-portal, pricing-pages, sandbox-testing, subscription-cancel, subscription-sync, subscription-update, webhooks. Samples are Next.js/Node (`@paddle/paddle-node-sdk`); the guidance (idempotency, ordering, retry semantics, sandbox/live table) is language-neutral.
- **MCP servers (3)**: `paddle-docs` → `https://paddlehq.mcp.kapa.ai` (no auth; doc Q&A) · `paddle-sandbox` → `https://sandbox-mcp.paddle.com/mcp` (Bearer = `user_config.sandbox_api_key`) · `paddle-live` → `https://mcp.paddle.com/mcp` (OAuth in browser).
- Token cost: ~760 always-on; 3.5k–7k per skill invocation.

## How we use it
- Navigator sessions: catalog (product/price) and notification destinations via `paddle-sandbox` instead of dashboard clicks; `notifications.logs.list(<notification_setting_id>)` to verify deliveries; simulations; `paddle-docs` for any doc question instead of web digests.
- Design source of truth stays `tasks/TASK-497-paddle-billing-integration.md`; the plugin's skills were cross-checked against it on 2026-09-12 (corroborations recorded there as research fact 13).
- MCP servers load at session start: after `/plugin configure`, start a new session.
