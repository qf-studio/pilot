# TASK-28: Speed Optimization

**Status**: Backlog
**Priority**: High
**Created**: 2026-01-26

---

## Overview

Optimize Pilot execution speed through parallel subagents, model routing, caching, and Navigator overhead reduction. Target: 2-3x faster task completion.

---

## Current Bottlenecks

1. **Single Claude instance** - sequential execution
2. **Navigator overhead** - ~30s workflow check for simple tasks
3. **Cold start** - 5s+ initialization per task
4. **Full context load** - loads everything even for trivial tasks

---

## Optimization Strategies

### 1. Parallel Subagents (High Impact)

Spawn multiple Claude instances for independent work:

```
Task Start
├── Agent 1: Research (read files)      ─┐
├── Agent 2: Analyze patterns           ─┼── Parallel
└── Agent 3: Check dependencies         ─┘
    ↓ (merge context)
Implementation (single agent)
    ↓
Testing (can parallelize test suites)
```

**Implementation:**
```go
type ParallelResearch struct {
    agents    int
    timeout   time.Duration
    mergeFunc func([]Result) Context
}

func (r *Runner) parallelResearch(task *Task) Context {
    results := make(chan Result, 3)

    go r.spawnAgent("research-files", results)
    go r.spawnAgent("analyze-patterns", results)
    go r.spawnAgent("check-deps", results)

    return mergeResults(collect(results, 3))
}
```

### 2. Model Routing (Medium Impact)

Route tasks to appropriate model:

| Task Complexity | Model | Speed | Cost |
|----------------|-------|-------|------|
| Trivial (typo, log) | Haiku | 5x | $0.001 |
| Simple (small fix) | Sonnet | 1x | $0.01 |
| Medium (feature) | Sonnet | 1x | $0.05 |
| Complex (architecture) | Opus | 0.5x | $0.50 |

**Detection heuristics:**
```go
func detectComplexity(task *Task) string {
    desc := strings.ToLower(task.Description)

    // Trivial patterns
    if containsAny(desc, "typo", "fix typo", "add log", "rename") {
        return "haiku"
    }

    // Complex patterns
    if containsAny(desc, "refactor", "architecture", "system", "redesign") {
        return "opus"
    }

    return "sonnet" // default
}
```

### 3. Skip Navigator for Simple Tasks (Quick Win)

Bypass Navigator workflow for trivial tasks:

```go
func (r *Runner) BuildPrompt(task *Task) string {
    complexity := detectComplexity(task)

    if complexity == "trivial" {
        // Direct execution, no Navigator
        return fmt.Sprintf("Quick task: %s\n\nJust do it. Commit when done.",
            task.Description)
    }

    // Full Navigator workflow for complex tasks
    return r.buildNavigatorPrompt(task)
}
```

**Savings:** ~30 seconds per trivial task

### 4. Warm Sessions (Medium Impact)

Keep Claude connection alive between tasks:

```go
type SessionPool struct {
    sessions map[string]*ClaudeSession
    maxIdle  time.Duration
}

func (p *SessionPool) Get(project string) *ClaudeSession {
    if s, ok := p.sessions[project]; ok && !s.Expired() {
        return s
    }
    return p.create(project)
}
```

**Benefits:**
- Skip CLAUDE.md reload
- Skip Navigator init
- Reduce cold start 5s → <1s

### 5. Context Caching (Medium Impact)

Cache expensive computations:

```go
type ContextCache struct {
    codebaseStructure map[string]CachedStructure  // invalidate on git
    navigatorDocs     map[string]CachedDocs       // TTL 5 min
    taskFiles         map[string]CachedTask       // invalidate on file change
}

func (c *ContextCache) GetCodebaseStructure(project string) Structure {
    if cached, ok := c.codebaseStructure[project]; ok {
        if !c.gitChanged(project, cached.GitSHA) {
            return cached.Structure
        }
    }
    return c.analyze(project)
}
```

### 6. Chunked Parallelism (High Impact, Complex)

Break large tasks into parallel subtasks:

```
TASK-25: Telegram Commands
    ↓ (decompose)
├── Subtask A: /tasks command  → Agent 1  ─┐
├── Subtask B: /run command    → Agent 2  ─┼── Parallel
├── Subtask C: /stop command   → Agent 3  ─┘
    ↓ (merge)
Single commit with all changes
```

**Challenges:**
- Merge conflicts
- Shared dependencies
- Coordinated testing

---

## Implementation Priority

| Strategy | Impact | Effort | Priority |
|----------|--------|--------|----------|
| Skip Navigator (simple) | Medium | Low | 1 - Quick win |
| Model routing | Medium | Low | 2 - Quick win |
| Parallel research | High | Medium | 3 |
| Warm sessions | Medium | Medium | 4 |
| Context caching | Medium | Medium | 5 |
| Chunked parallelism | High | High | 6 - Future |

---

## Metrics to Track

- Task completion time (p50, p95)
- Cold start time
- Navigator overhead time
- Token usage per task
- Cost per task

---

## Acceptance Criteria

- [ ] Simple tasks complete 2x faster
- [ ] Model routing reduces cost 30%+
- [ ] Parallel research cuts research phase 50%
- [ ] Cold start under 2 seconds
- [ ] Metrics dashboard shows improvements

---

## Dependencies

- TASK-13: Execution Metrics (for tracking)
- TASK-26: Version Management (for model selection)
