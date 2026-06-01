# TASK-349: Telegram alert channel parse_mode/escaping mismatch drops critical alerts (E4)

## Context

`TelegramChannel.Send` calls `client.SendMessage(ctx, chatID, text, "Markdown")`
(`internal/alerts/channels.go:150`) — legacy Markdown parse mode. But `formatMessage` escapes the
title/message with `escapeMarkdown` (`channels.go:160-161`), whose replacer escapes the **MarkdownV2**
metacharacter set: `= ~ > # + - | { } . !` (`channels.go:189-211`). Legacy Markdown only treats
`_ * [ \`` as special and does NOT treat a backslash before any other char as an escape. Result: an
alert like `Daily spend $50.00 exceeds threshold $25.00` renders with literal backslashes (`50\.00`)
at best; at worst Telegram's legacy Markdown rejects the invalid entities and `SendMessage` returns
`can't parse entities` (`client.go:311-312`), so the alert is **never delivered** — a silent failure
for exactly the cost/failure alerts an operator needs. The escaping was written for MarkdownV2 but the
parse mode was never switched.

## Approach

Make parse mode and escaping consistent. Prefer MarkdownV2 (matches the existing `escapeMarkdown` set):
pass `"MarkdownV2"` to `SendMessage`. (Alternative, not preferred: narrow `escapeMarkdown` to only
legacy specials `_ * [ \``.) Add a test that sends a message containing `.`, `-`, `!`, and `_` and
asserts a well-formed payload (no `can't parse entities`).

## Acceptance

- [ ] `TelegramChannel.Send` uses a parse mode consistent with `escapeMarkdown` (MarkdownV2).
- [ ] Test: a message with `. - ! _` produces a well-formed payload and does not error with `can't parse entities`.
- [ ] A representative cost alert (`$50.00 exceeds $25.00`) renders without stray backslashes.
- [ ] `make test` green for `internal/alerts`; `make lint` clean.

## Refs

- Findings ledger: `.agent/tasks/TASK-322-security-audit-findings.md` (E4, medium)
- Kickoff: `.agent/tasks/TASK-342-wave3-kickoff.md`
- File: `internal/alerts/channels.go:150,160-161,189-211`; client `internal/adapters/telegram/client.go:311-312`
