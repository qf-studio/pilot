---
name: StructuredOutput required by stop hook
description: Stop hook fires at conversation end requiring StructuredOutput tool call with branch, commit, files, summary
type: feedback
originSessionId: 912cf3ff-5e72-46c0-a3f9-8d918f0b62d6
---
Always call `StructuredOutput` tool before ending a response. The stop hook requires it.

**Why:** A configured stop hook validates that StructuredOutput was called; missing it triggers a hook error.

**How to apply:** After completing any user request, call `StructuredOutput` with `branch_name`, `commit_sha`, `files_changed`, and `summary` derived from the current git state.
