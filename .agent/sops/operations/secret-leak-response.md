# SOP: respond to a leaked secret (token, API key, password)

**Owner**: Navigator session (interactive). **Source**: TASK-650 incidents 2026-11-09 (Telegram token in the box log and on GitHub since 2026-04-07; Slack and Anthropic keys in an agent recording).

## Rules that apply at every step

- **Rotate first.** A rotated secret makes every copy useless: logs, recordings, backups, forks, SHA URLs, transcripts. Masking without rotation is not a fix.
- **Never print a value.** Compare secrets by hash: `printf %s "$v" | shasum -a 256 | cut -c1-8`. When you print command output, mask every known shape, not only the one you hunt (`sk-ant-`, `xox?-`, `xapp-`, `gh?_`, `github_pat_`, `AKIA`, `lin_api_`, `<digits>:AA…` for Telegram).
- **Never pass a secret through `aws ssm send-command` parameters or chat.** SSM keeps command parameters in its history, and you cannot delete them. Put the value in SSM Parameter Store as a `SecureString` from your own terminal, then let the box read it; or edit the file in an interactive `aws ssm start-session`.
- **Never ask an executor ticket to diagnose the live box.** An agent on the box takes it as licence to read metrics, logs and config (GH-4669, 2026-08-03). Box diagnosis runs in a Navigator session over SSM.

## Procedure

1. **Scope.** List every place that holds the value, by hash:
   - box: `~/.pilot/config.yaml` and its `config.yaml.bak-*` files, `~/.pilot/logs/`, `~/.pilot/recordings/**` (`stream.jsonl`, `summary.md`), `~/.pilot/data/pilot.db*`;
   - GitHub: secret-scanning alerts (`gh api repos/<owner>/<repo>/secret-scanning/alerts`), PR bodies and comments, issue bodies and comments;
   - Slack channels that receive alerts (search for the shape);
   - laptop archive `~/.pilot/config.yaml`.
2. **Rotate** at the provider: Telegram BotFather `/revoke`; Slack app settings (bot token: reinstall or rotate; app-level token: create new, revoke old); Anthropic Console (create new key, delete old); GitHub (revoke the PAT or rotate the App key).
3. **Apply** the new value on the box (`aws ssm start-session`, `sudo su - ec2-user`, edit `~/.pilot/config.yaml`), then restart per `safe-daemon-restart.md`. Check the startup line `adapter verified adapter=<name>` and the hash of the stored value.
4. **Mask the sinks in place.** For a live log, overwrite with same-length masking through a file opened `r+` (same inode); `sed -i` creates a new inode and the daemon keeps writing to the unlinked file. Set log files to 0600 and recordings to 0700/0600.
5. **Close the GitHub alert** as `revoked` with a comment that names the rotation date: `gh api -X PATCH repos/<owner>/<repo>/secret-scanning/alerts/<n> -f state=resolved -f resolution=revoked -f resolution_comment="…"`. A history rewrite does not remove a pushed commit: GitHub serves it by SHA and forks keep it.
6. **Find the cause** (which code path or which agent action wrote the value) and file the fix as a `pilot` issue. Write or update a Navigator memory (mem-300 is the reference pitfall).
7. **Record** the incident in the task doc and the context marker: what leaked, where, for how long, rotated when.

## Prevention in place (TASK-650)

v2.317.0: Telegram client URL redaction, redacting log writer, alert redaction, Telegram pattern, log files 0600 (#6244, #6246). Dispatched: value-based redaction in every sink (L1), leak scan (L2), adapter canary tests (L3), read guard (L4), push/PR/comment secret gate (L5), GitHub secret-scanning alert watcher (L6). Held: separate agent uid with secrets resolved from SSM (H1). GitHub repo settings: secret scanning, push protection, validity checks and non-provider patterns enabled (2026-11-10).

## Refs

- `.agent/tasks/TASK-650-secret-exposure-hardening.md` · memory mem-300 · `.agent/sops/operations/safe-daemon-restart.md` · `.agent/sops/operations/box-claude-oauth-token-rotation.md`
