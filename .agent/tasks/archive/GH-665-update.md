# GH-665 Update: LLM-Based Complexity Detection

**Run this when GitHub API is available:**

```bash
gh issue edit 665 --title "feat(executor): LLM-based complexity detection replacing word-count heuristic"
```

Then update the body with the content below via GitHub UI or CLI.

## Updated Issue Body

### Problem
Word count is a terrible proxy for complexity. 500-word issue can be trivial, 30-word issue can be complex. Current thresholds (50 words = complex) cascade into wrong model routing, false decomposition, wasted tokens.

### Solution
LLM classification with Haiku — same pattern as intent detection:
- Fast path: regex for obvious trivial/epic cases
- LLM path: Haiku structured JSON for ambiguous cases (~$0.001/call)
- Fallback: raised word count thresholds (Simple <20, Medium <150, Complex 150+)

### Key addition: `ShouldDecompose` field
LLM decides whether task should be decomposed — distinguishes "detailed instructions for one task" from "independent work items needing separate PRs".

### Files
- NEW: `internal/executor/complexity_classifier.go` — Haiku-based classifier
- MOD: `internal/executor/complexity.go` — add `DetectComplexityWithLLM()`, raise fallback thresholds
- MOD: `internal/executor/decompose.go` — use `ShouldDecompose` from classification
- MOD: `internal/executor/runner.go` — wire classifier
- Config: `executor.complexity_classifier.enabled/model/timeout`
