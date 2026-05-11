---
name: Never execute code directly in Navigator planning session
description: In interactive Navigator planning sessions, code changes go through Pilot. Does NOT apply to Pilot-executor sessions spawned on the pilot repo itself.
type: feedback
originSessionId: 3cf86f8a-006c-4029-8023-ab5f81749dfc
---
In **interactive Navigator planning sessions** against the Pilot repo,
NEVER write code, commit, or tag releases directly. Plan via `/nav-task`
and hand off to Pilot via a GitHub issue with the `pilot` label.

**Scope — when this rule DOES NOT apply:**

- **Pilot-executor sessions** spawned by `pilot start` that run Claude
  Code against this repo to implement a specific issue. In those
  sessions, the prompt begins with `GitHub Issue #NNN:` or `Task:` and
  the CWD is inside a pilot worktree. The executor's whole job is to
  write code, commit, and push — Pilot is self-hosting, which means
  Claude Code IS Pilot for the duration of that task.
- Operational/read-only work in interactive sessions (checking status,
  reviewing PRs, filing issues, editing memory/docs).
- Explicit user instruction to edit / commit / fix something inline
  during an interactive session.

**Why (for interactive planning sessions):** Direct execution without
Navigator loop mode skips research, docs, tests, and quality gates. A
"quick fix" of 1 task creates 6 follow-up problems. The whole point of
the Navigator+Pilot pipeline is to build correctly — research codebase
first, update docs, write tests, then implement.

**How to apply (interactive sessions):**
1. Use `/nav-task` to plan the solution
2. Create a GitHub issue with `pilot` label containing the plan
3. Let Pilot execute with Navigator loop mode (proper research, docs, tests)
4. Review the PR, request changes if needed
5. NEVER bypass this even for "simple" fixes — they cascade

**Pilot is self-evolving:** Each execution builds the knowledge graph —
patterns from PR reviews, SOPs from solved problems, codebase context
from Navigator research. Direct execution in a planning session
bypasses the learning loop entirely. The learnings are lost, and Pilot
doesn't get smarter.

**Incident (2026-04-03):** "Quick fix" for GH-2177 (sub-issue worktree
path) turned into 3 releases (v2.87.5-7), each revealing another bug.
No Navigator research, no proper docs, no loop mode. Created more
problems than value. Zero knowledge captured.

**Follow-up incident (2026-04-17, GH-2305/2324/2325):** The unqualified
rule above caused Pilot-spawned Claude Code sessions to refuse every
task — Pilot was running in its own repo, Claude read this memory,
and declined with "NEVER write code in this session." Three issues
failed dozens of times before it was diagnosed. Hence the scope
clarification: Pilot executor sessions are the ONE exception.
