# Roadmap v3.0: Self-Improvement System

**Created**: 2026-03-04 | **Status**: Active | **Priority**: P0-P2

## Context

Competitive research (OB-1, Claude Code, Copilot Agent, Cursor, Factory, Devin) revealed Pilot's self-improvement system is architecturally sound but has critical wiring gaps. The learning infrastructure exists (pattern extraction, knowledge graph, confidence scoring, prompt injection) but the execution layer doesn't fully use it.

Nobody has shipped the full self-improving loop for a production coding pipeline. Pilot is closest — 20 learning mechanisms already implemented. This roadmap closes the gaps.

---

## Phase 1: Fix Broken Wiring (W1 — Mar 4-7)

### ROAD-01: Fix Anti-Pattern Injection Bug
- **File**: `internal/memory/query.go:195-211`
- **Bug**: `IncludeAnti: true` in Query() but filter excludes anti-patterns before injection
- **Fix**: Separate query for anti-patterns or fix filter predicate
- **Verify**: `--dry-run` shows anti-patterns in execution prompt

### ROAD-02: Wire Patterns Into Self-Review
- **File**: `internal/executor/prompt_builder.go:260-341`
- **Gap**: 8 static checks, zero learned pattern validation
- **Add**: 9th check — "Validate against project patterns" from PatternContext
- **Verify**: Self-review flags code violating known pattern

### ROAD-03: Add Database Indexes
- **File**: `internal/memory/store.go`
- **Add**: Indexes on title, description, scope, updated_at in cross_patterns
- **Verify**: EXPLAIN QUERY PLAN shows index usage

---

## Phase 2: Close Feedback Loops (W2 — Mar 10-14)

### ROAD-04: Extract Patterns from Self-Review
- **File**: `internal/executor/runner.go:2778-2830`
- **Gap**: Self-review findings never feed back to learning system
- **Add**: `extractor.ExtractFromSelfReview(output)` after self-review
- **New method**: `ExtractFromSelfReview()` in extractor.go
- **Verify**: 3 tasks → self-review findings in cross_patterns table

### ROAD-05: Learn from CI Failure Logs
- **File**: `internal/autopilot/feedback_loop.go:48-75`
- **Gap**: CI logs captured in fix issues but never analyzed for patterns
- **Add**: `extractor.ExtractErrorPatterns(ciLogs)` in CreateFailureIssue()
- **Verify**: CI failure → error pattern stored with correct type

### ROAD-06: Acceptance Criteria Verification
- **File**: `internal/executor/prompt_builder.go:73-80`
- **Gap**: ACs in prompt but not verified in self-review
- **Add**: 10th check — structured AC checklist, each criterion confirmed
- **Verify**: Task with 3 ACs → self-review addresses each

### ROAD-07: Update Feature Matrix to v2.44.0
- **File**: `.agent/system/FEATURE-MATRIX.md`
- **Gap**: Stuck at v1.39.0 (137 features), current v2.44.0 (250+)
- **Add**: ~50 missing v2.x features

---

## Phase 3: Smarter Routing + Extraction (W3-4 — Mar 17-28)

### ROAD-08: Outcome-Based Model Routing
- **Files**: `internal/executor/model_routing.go`, `complexity_classifier.go`
- **Gap**: Static complexity → model. No learning from outcomes.
- **Add**: `ModelOutcomeTracker` SQLite table {task_type, model, outcome, tokens}
- **Add**: Auto-escalate model when failure rate > 30% for task_type+model
- **Verify**: 5 same-type tasks, 3 fail with Haiku → 6th uses Sonnet

### ROAD-09: Expand Pattern Extractors
- **File**: `internal/memory/extractor.go:183-244`
- **Gap**: 5 matchers only (context, errors, tests, logging, validation)
- **Add**: API design, concurrency, config wiring, test patterns, performance, security
- **Verify**: Concurrency code → concurrency pattern extracted

---

## Phase 4: Knowledge Graph + Temporal (Apr W1-2)

### ROAD-10: Activate Knowledge Graph
- **Files**: `internal/memory/graph.go:196-217`, `internal/executor/context.go`
- **Gap**: Initialized but never queried — dead code
- **Wire**: `graph.GetRelated(taskKeywords)` into PatternContext.InjectPatterns()
- **Populate**: graph.AddLearning() in recordExecutionForLearning()
- **Verify**: Add learning, execute related task, learning in prompt

### ROAD-11: Temporal Pattern Relevance
- **File**: `internal/memory/feedback.go`
- **Gap**: No task-type or project specificity in confidence
- **Add**: PatternPerformance table {pattern_id, project_id, task_type, model, success, fail}
- **Verify**: Same pattern, different projects → different confidence in prompt

---

## Phase 5: Eval Suite Generation (Apr W3-4)

### ROAD-12: PR-to-Eval Task Extractor
- **New file**: `internal/memory/eval.go`
- After PR merge: extract {issue_text, base_commit, pass_criteria, files_changed, complexity}
- Store in SQLite eval_tasks table

### ROAD-13: Eval Runner
- **New file**: `internal/memory/eval_runner.go`
- Checkout base commit in worktree, execute, run pass criteria
- Track pass@1 and pass^k metrics

### ROAD-14: Regression Detection
- Run eval suite before model/prompt changes
- Block changes that drop pass rate
- Surface in dashboard + alerts

---

## Phase 6: Competitive + Navigator (May+)

### ROAD-15: Security Scanning in Self-Review
### ROAD-16: Navigator `/nav-eval` Skill
### ROAD-17: Navigator `/nav-learn` Skill
### ROAD-18: SOP Auto-Generation from Diffs
### ROAD-19: Pre-Execution Validation Skill
### ROAD-20: Plugin/Skill Ecosystem

---

## Key Files Reference

| File | Changes |
|------|---------|
| `internal/memory/query.go` | Fix anti-pattern filter (ROAD-01) |
| `internal/memory/extractor.go` | +6 matcher categories, +ExtractFromSelfReview (ROAD-04, ROAD-09) |
| `internal/memory/feedback.go` | +PatternPerformance tracking (ROAD-11) |
| `internal/memory/graph.go` | Wire into execution (ROAD-10) |
| `internal/memory/store.go` | +indexes, +eval_tasks table (ROAD-03, ROAD-12) |
| `internal/memory/patterns.go` | +outcome tracking (ROAD-08) |
| `internal/executor/context.go` | +knowledge graph query (ROAD-10) |
| `internal/executor/prompt_builder.go` | +pattern/AC checks in self-review (ROAD-02, ROAD-06) |
| `internal/executor/runner.go` | +self-review → learning (ROAD-04) |
| `internal/executor/model_routing.go` | +outcome-based routing (ROAD-08) |
| `internal/autopilot/feedback_loop.go` | +error pattern extraction (ROAD-05) |

---

## Success Metrics

| Metric | Before | Target |
|--------|--------|--------|
| Anti-patterns injected per task | ~0 (bug) | 2-3 avg |
| Self-review pattern checks | 0 | 5+ per review |
| CI failure patterns learned | 0 | Auto-extracted |
| Model routing adaptations | 0 (static) | Outcome-aware |
| Pattern categories | 5 | 11 |
| Knowledge graph queries/task | 0 (dead) | 3-5 |
| Eval tasks accumulated | 0 | 1 per merged PR |
| Feature matrix accuracy | v1.39.0 | v2.44.0+ |
