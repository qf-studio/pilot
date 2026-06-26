# Pilot Conversational Bot — Client Demo Runbook

**Goal:** show that you talk to Pilot like a teammate in Slack, and it (1) answers
instantly, (2) knows your codebase, and (3) turns a sentence into a shipped PR.

**Validated:** 2026-06-26 on v2.200.2, Slack + github adapters, demo repo = `qf-studio/pilot`.

---

## Pre-flight (do 5 min before, once)

1. **Daemon** on **v2.200.2+** (`pilot version`), running `pilot start --github --slack --env stage --dashboard`.
2. **Config sanity** (`~/.pilot/config.yaml`) — all three must point at the **demo repo**:
   - `default_project: pilot`         ← Q&A / retrieval target
   - `adapters.github.repo: qf-studio/pilot` + `project_path: …/pilot` ← intake target + poll source
   - `adapters.github.project_board.enabled: false` ← use `pilot`-label polling, not a board
   - `bot.enabled: true`, `adapters.slack.llm_classifier.enabled: true`
   - **Re-verify after ANY restart** — the active project is in-memory and resets.
3. **Pre-stage a finished PR** from an identical intake prompt (Beat 3 takes ~3–10 min) so you can reveal a real PR instantly if live execution is slow.
4. Slack channel open, bot invited, dashboard visible on screen.

---

## The arc — 3 beats (~5 min)

> Staging tip: **fire Beat 3 first**, then do Beats 1–2 while the daemon writes the code, and reveal the PR at the end. (Or show the pre-staged PR.)

### Beat 1 — Instant answer (the latency win)
- **Type:** `@Pilot what do you think about <topic the client cares about>?`
- **Expect:** persona reply in ~1–2 s.
- **Say:** *"No 'working on it' for minutes — it answers like a teammate. That used to spin up a full coding session; now it just talks."*

### Beat 2 — Grounded codebase Q&A
- **Type:** `@Pilot how does intent routing work in this repo?`  *(or)*  `@Pilot draw the schema of the intent flow in ASCII`
- **Expect:** a few seconds → answer that **cites real files** / an ASCII diagram built from the actual code.
- **Say:** *"It read your files — not a generic answer. It's grounded in this codebase."*

### Beat 3 — Talk → ticket → PR (the money shot)
- **Type:** `@Pilot create an issue to add a /ping health endpoint`
- **Expect:** drafts a structured `pilot` issue (conventional-commit title + body), returns the URL.
- **Say:** *"I described it in one sentence. It filed a structured ticket."*
- **Then:** point at the dashboard → the daemon picks it up (`in-progress`) → opens a PR.
- **Say:** *"And now it's writing the code. Here's the PR — from a Slack sentence to a reviewable change, no ticketing tool, no context switch."*

---

## Talking points (the pitch)

- **Speed:** feels instant, not a batch job — the original problem we set out to kill.
- **The loop:** conversation in → working PR out. One surface (Slack), no jumping to Jira/GitHub.
- **Grounded:** answers and diagrams come from the real repo, not a generic model.
- **Same engine ships it:** the bot is the front door to the autonomous executor that already opens PRs.

---

## Gotchas (don't let these bite live)

- **Wrong-repo trap:** intake follows `adapters.github.repo`; Q&A follows the active/default project. If they diverge, the issue lands on the wrong repo. Keep both on the demo repo. Re-check after every restart.
- **Natural phrasing is fine** — the Haiku classifier is on, so "draw/outline/show me" route to answers, not tasks. (With it off, those misroute to a code-task.)
- **If a reply errors** ("Sorry, I couldn't process that"): almost always the binary auto-upgraded to a pre-fix release. Check `pilot version` ≥ v2.200.2; `pilot upgrade` + restart if not.
- **PR latency:** Beat 3's PR is minutes, not seconds — stage it (fire first / pre-stage).

---

## One-line health check before going live
```bash
pilot version   # >= 2.200.2
gh issue list --repo qf-studio/pilot --label pilot --state open   # expect empty (no stale pickups)
```
