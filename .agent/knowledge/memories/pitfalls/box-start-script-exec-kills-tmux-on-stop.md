# Pitfall: start-pilot.sh execs the daemon, so stopping Pilot closes the tmux pane and server; relaunch needs tmux new-session

## Summary
On the founder box the daemon runs in tmux session 'pilot' whose only pane was started with ./start-pilot.sh, which ends in exec pilot start. Sending C-c (or pilot stop) therefore terminates the pane's only process, the pane and window close, and the tmux server exits ('no server running on /tmp/tmux-1000/default'). A follow-up tmux send-keys has nowhere to go and the daemon stays down. Relaunch with: tmux new-session -d -s pilot -x 220 -y 50 'cd /home/ec2-user && ./start-pilot.sh' as ec2-user, then verify pgrep and the startup lines. 2026-10-01 13:13Z: daemon down ~2 min this way.

## Context
2026-10-01, Linear Invoices onboarding restart via SSM.

## Details
On the founder box the daemon runs in tmux session 'pilot' whose only pane was started with ./start-pilot.sh, which ends in exec pilot start. Sending C-c (or pilot stop) therefore terminates the pane's only process, the pane and window close, and the tmux server exits ('no server running on /tmp/tmux-1000/default'). A follow-up tmux send-keys has nowhere to go and the daemon stays down. Relaunch with: tmux new-session -d -s pilot -x 220 -y 50 'cd /home/ec2-user && ./start-pilot.sh' as ec2-user, then verify pgrep and the startup lines. 2026-10-01 13:13Z: daemon down ~2 min this way.

## Recommended Approach
Restart sequence from SSM: tmux send-keys -t pilot:0.0 C-c; wait for pgrep to clear; tmux new-session -d -s pilot ... './start-pilot.sh'; wait 30s; grep startup lines. Or run start-pilot.sh under a shell (no exec) so the pane survives.

## Related
- `cmd/pilot/main.go`

---
**Captured**: 2026-10-01
**Confidence**: 90%
**Concepts**: box-operations, restart, tmux
