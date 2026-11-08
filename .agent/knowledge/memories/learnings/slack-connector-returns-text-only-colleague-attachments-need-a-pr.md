---
name: slack-connector-returns-text-only-colleague-attachments-need-a-pr
description: The Code-session Slack connector returns message text only — a colleague's "attached" file is invisible to slack_read_thread and to file search, and the browser pane needs a Slack sign-in; ask the author to push the file as a PR to the target repo instead
type: learning
---

# Slack connector = text only; "attached" files need a PR, not a download

**What happened (2026-10-19, TASK-515):** the infra contractor posted "Deploy workflow for
pilotcloud-site is ready, attached as .github/workflows/deploy-quantflow-aws.yml"
in `#infrastructure` (thread `1791555910.941859`, reply `1792435192.787699`).
`slack_read_thread` returned the text and no file; `slack_search_public_and_private`
with `content_types: files` / `has:file` from her returned nothing since 10-15;
the built-in browser pane opened the permalink to a Slack sign-in page.
GitHub held nothing either (every ref, PR head, fork, event). Forty minutes
of back-and-forth with the founder ("which attachment?") before the obvious
move: she is a collaborator (`the-infra-contractor`) on the repo.

## Rules
- Treat Slack "attached" as unreachable from a Code session. Do not ask the
  founder to hunt for the file; reply in-thread asking the author to push it
  as a PR to the target repo (check `gh api repos/<r>/collaborators` first).
- A file we cannot see may also not exist — the same reply covers both cases.
- Related: `ccd-slack-connector-off-by-default-enable-per-session`.
