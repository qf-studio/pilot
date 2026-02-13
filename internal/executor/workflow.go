package executor

// GetAutonomousWorkflowInstructions returns embedded execution workflow
// that replaces Navigator's /nav-loop skill.
func GetAutonomousWorkflowInstructions() string {
	return workflowEnforcement + "\n" + autonomousWorkflowInstructions
}

const workflowEnforcement = `## WORKFLOW CHECK (Mandatory)

Before starting, confirm execution mode:

┌────────────────────────────────────────┐
│ WORKFLOW CHECK                         │
├────────────────────────────────────────┤
│ Loop trigger: [YES if autonomous]      │
│ Complexity:   [TRIVIAL/SIMPLE/MEDIUM/COMPLEX] │
│ Mode:         [LOOP/TASK/DIRECT]       │
└────────────────────────────────────────┘

**Loop triggers** (auto-detect):
- "run until done"
- "keep going until complete"
- "iterate until finished"
- "do all of these"
- "finish this"

**Mode selection**:
- LOOP: Autonomous iteration with EXIT_SIGNAL
- TASK: Structured phases (INIT→RESEARCH→IMPL→VERIFY→COMPLETE)
- DIRECT: Simple changes, no overhead

Output this block at start of execution to confirm mode.
`

const autonomousWorkflowInstructions = `## Autonomous Execution Workflow

### Phase 1: INIT (0-10%)

**Do**:
- Read the full task description
- Identify acceptance criteria
- Note any constraints mentioned

**Don't**:
- Start coding immediately
- Skip reading requirements
- Make assumptions

**Example signal**:
` + "```" + `pilot-signal
{"v":2,"type":"status","phase":"INIT","progress":5}
` + "```" + `

---

### Phase 2: RESEARCH (10-30%)

**Do**:
- Find similar implementations in codebase
- Check existing patterns (naming, structure)
- Identify dependencies

**Don't**:
- Reinvent existing utilities
- Ignore project conventions
- Start implementing yet

**Example**: If adding auth, check existing auth code:
` + "```" + `bash
grep -r "func.*Auth" internal/
` + "```" + `

---

### Phase 3: IMPL (30-75%)

**Do**:
- Follow existing code patterns
- Keep changes focused on task
- Commit incrementally if large change

**Don't**:
- Refactor unrelated code
- Add features not requested
- Skip error handling

**Common anti-patterns**:
- Adding TODOs instead of implementing
- Changing code style in unrelated files
- Creating new utilities when existing ones work

**Progress signals**: Report at 40%, 50%, 60%, 70%

---

### Phase 4: VERIFY (75-90%)

**CRITICAL**: Run these BEFORE committing:

1. **Build check**:
` + "```" + `bash
go build ./...  # Must pass with zero errors
` + "```" + `

2. **Test check**:
` + "```" + `bash
go test ./internal/path/to/changed/package/...
` + "```" + `

3. **Wiring check** (for new config/struct fields):
   - Field defined in struct? ✓
   - Field assigned in constructor/factory? ✓
   - Field used somewhere? ✓

4. **Method check** (for new method calls):
   - Method exists on type? ✓
   - Correct signature? ✓

**If any check fails**:
- Fix the issue
- Re-run the check
- Do NOT proceed until all pass

---

### Phase 5: COMPLETE (90-100%)

**Commit format**:
` + "```" + `
type(scope): description

Examples:
- feat(auth): add OAuth provider integration
- fix(api): handle nil response in webhook handler
- refactor(executor): extract signal parsing to separate file
` + "```" + `

**Exit signal** (REQUIRED):
` + "```" + `pilot-signal
{"v":2,"type":"exit","exit_signal":true,"success":true}
` + "```" + `

---

### Error Recovery

**Stuck after 3 attempts?**

1. Stop and analyze:
   - What's the actual error?
   - Is there a simpler approach?
   - Am I missing context?

2. Try alternative:
   - Different algorithm
   - Different library
   - Simpler implementation

3. If truly blocked:
` + "```" + `pilot-signal
{"v":2,"type":"exit","exit_signal":true,"success":false,"reason":"blocked: [specific reason]"}
` + "```" + `

**Never**:
- Loop infinitely on same error
- Commit broken code
- Give up without EXIT_SIGNAL
`
