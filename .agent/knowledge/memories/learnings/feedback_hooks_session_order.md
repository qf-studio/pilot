---
name: Stale hooks error — session ordering
description: Claude Code caches hooks on start; stale hook errors mean CC started before Pilot cleaned up
type: feedback
---

"No such file or directory" for pilot-stop-gate.sh or pilot-bash-guard.sh means Claude Code loaded stale hooks.

**Why:** Claude Code reads `.claude/settings.json` once on startup and caches hooks in memory. Clearing the file mid-session doesn't help.

**How to apply:** If this error appears, restart Claude Code (not Pilot). Pilot's startup cleanup already removes dead hook entries — the issue is CC session started before Pilot ran cleanup. Don't chase Pilot code bugs for this.
