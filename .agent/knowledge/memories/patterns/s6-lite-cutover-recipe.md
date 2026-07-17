# Pattern: S6-lite daemon cutover recipe — move a stateful per-path-keyed daemon between machines with zero DB surgery

## Summary
Executed 2026-07-16 (TASK-409, laptop → EC2 t3.xlarge, ~4h plan-to-verified). Load-bearing tricks:
1. **Path shim beats data migration**: ledger + execution_claims key on absolute paths (`/Users/aleks.petrov/Projects/...`). On Linux, `mkdir -p /Users/...` + symlink → identical paths → zero UPDATE surgery, zero re-pick storms.
2. **Verbatim config, one-way gate**: config copies unchanged; dual-serve made structurally impossible (remote runs projects:[] + adapters-off until local verified dead; claims are per-DB — NO cross-machine guard exists).
3. **tmux owns the TTY**: TUI daemon in a detached tmux session; attach via SSM interactive command. Token via 700-mode wrapper script (`export GITHUB_TOKEN=$(gh auth token)` inside) — never in argv (ps leaks) or tmux command strings.
4. **SSM file shipping = base64** — inline heredocs with \n escapes silently corrupt through SSM JSON (bit us twice).
5. **Non-portable auth is the human step**: macOS Keychain (claude, gh) doesn't travel — one device-flow session on the box.
6. **Post-cutover expectations**: dead-owner queued rows block claims (GH-4392); cold-start rescans burn the user rate pool (GH-4391) → expect ~1 quiet hour; monitoring kit: pilot-board / pilot-dash / pilot-tunnel + pilot-aws skill.

## Related
- Task: TASK-409 (full plan + rollback) · Skill: .claude/skills/pilot-aws · Pitfall: github-user-aggregate-rate-pool
