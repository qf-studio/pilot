---
name: the daemon auto-upgrades to the latest RELEASE, so a dev binary cp'd into ~/.local/bin/pilot gets reverted within ~20min — ship fixes as releases, not local builds
description: When testing a fix in the live daemon, building a dev binary and cp-ing it to ~/.local/bin/pilot then restarting does NOT stick. Pilot's hot-upgrade pulls the latest GitHub RELEASE and re-execs (pid preserved, so `ps lstart` looks unchanged while the on-disk binary mtime/size/version change underneath). Observed 2026-06-26: installed a 29MB dev build of the bot max_tokens fix at 12:32; by 12:52 ~/.local/bin/pilot was back to the 20.6MB v2.200.0 RELEASE (pre-fix), and the bot kept failing. The fix (#3700) was on main but in NO release yet (autopilot's shouldTriggerRelease gap), so the daemon had nothing newer to pull. Durable path: cut a release (tag-driven, mem-022) that contains the fix, then `pilot upgrade` → restart. v2.200.1 fixed it for good because it became the latest release.
type: pitfall
---
**Symptom:** you build a fix, `cp` it to `~/.local/bin/pilot`, restart the daemon, test
— still broken. Repeat — still broken. The dev binary silently reverts.

**Cause:** the daemon **hot-upgrades to the latest GitHub _release_** and re-execs itself.
The re-exec preserves the pid (and `ps -o lstart` keeps showing the *original* start time),
so the process *looks* unchanged while the binary it's running was swapped underneath. The
on-disk `~/.local/bin/pilot` flips from your dev build back to the latest release:
- dev `go build` → ~29MB (with symbols), `pilot version` = `1.0.0`
- release (GoReleaser, stripped) → ~20.6MB, `pilot version` = `2.200.x`

Because a fix merged to `main` is **not in a release until one is cut** (and autopilot's
`shouldTriggerRelease()` doesn't reliably fire — known P1 gap), the daemon's auto-upgrade
keeps pulling the last *release*, which predates the fix. So a `main`-correct fix can still
be absent from the running daemon.

**How to confirm which binary is actually running:**
```bash
PID=$(pgrep -f 'pilot start' | head -1)
lsof -p "$PID" | awk '$4=="txt"{print $NF}'      # the executable file
stat -f '%Sm %z' ~/.local/bin/pilot               # mtime + size (release ≈20MB, dev ≈29MB)
~/.local/bin/pilot version                         # 1.0.0 = dev, 2.x = release
```
A start time *earlier* than the binary's mtime means the process predates the current
on-disk binary — it's holding an older image (restart needed), OR it re-exec'd to a newer
one. Cross-check `version` + size, don't trust `lstart` alone.

**How to apply — ship fixes as releases, not local builds:**
1. Merge the fix to `main`.
2. Cut a release that contains it — tag-driven (see [[learning_pilot_release_and_binary_path]]
   / mem-022): `git tag vX.Y.Z origin/main && git push origin vX.Y.Z` → GoReleaser publishes
   GitHub release + Homebrew. (Don't wait on autopilot's auto-release; it may not fire.)
3. `pilot upgrade` (it prompts `[y/N]` — pipe `printf 'y\n' |` in non-interactive shells)
   → updates `~/.local/bin/pilot` to the new release.
4. **Restart the daemon** — `pilot upgrade` swaps the file but the running process keeps the
   old image until restart (or its next hot-upgrade re-exec).
5. The fix is now durable: it's the *latest* release, so the auto-upgrade has nothing older
   to revert to.

A dev-binary `cp` is fine for a 30-second smoke test, but expect it to be reverted within
~20min by the next hot-upgrade. Relates to [[learn_restart_vs_rebuild_stale_binary]] (mem-025)
and [[learning_pilot_release_and_binary_path]] (mem-020) — same binary-path family.
