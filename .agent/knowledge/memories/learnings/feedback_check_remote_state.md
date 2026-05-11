---
name: Check remote state when planning Pilot issues
description: Use gh api to check remote file contents, not local files which may be stale — caused 2 redundant issues
type: feedback
---

When planning Pilot issues that involve file changes, check remote state via `gh api repos/.../contents/...` instead of reading local files.

**Why:** Local main can be behind origin. Session created 2 redundant issues (#2190, #2191) because local files showed old content that was already fixed on remote. Pilot found nothing to change → "no code changes" failure.

**How to apply:** Before creating any Pilot issue that says "change X in file Y", verify the current remote content of that file. Run `git pull origin main --rebase` at session start, and double-check with `gh api` if unsure.
