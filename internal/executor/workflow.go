package executor

// GetAutonomousWorkflowInstructions returns embedded execution workflow
// that replaces Navigator's /nav-loop skill.
func GetAutonomousWorkflowInstructions() string {
	return autonomousWorkflowInstructions
}

const autonomousWorkflowInstructions = `## Autonomous Execution Workflow

### Phase 1: INIT (0-10%)
- Parse task requirements
- Identify files to modify

Report: pilot-signal {"v":2,"type":"status","phase":"INIT","progress":5}

### Phase 2: RESEARCH (10-30%)
- Explore codebase for patterns
- Find similar implementations
- Understand dependencies

Report: pilot-signal {"v":2,"type":"status","phase":"RESEARCH","progress":20}

### Phase 3: IMPL (30-75%)
- Implement changes incrementally
- Follow existing patterns
- Keep changes focused

Report progress at 40%, 50%, 60%, 70%

### Phase 4: VERIFY (75-90%)
CRITICAL: You must verify BEFORE committing.

1. Run build: go build ./... (or equivalent)
2. Run tests: go test ./... for changed packages
3. Check wiring: New config fields flow from yaml → main.go → handler
4. Check methods: Any method calls have implementations

If verification fails:
- Fix the issue
- Re-run verification
- Do NOT commit broken code

Report: pilot-signal {"v":2,"type":"status","phase":"VERIFY","progress":85}

### Phase 5: COMPLETE (90-100%)
Only after VERIFY passes:
1. Commit all changes: type(scope): description
2. Signal completion:

pilot-signal {"v":2,"type":"exit","exit_signal":true,"success":true}

### Error Recovery
If stuck after 3 attempts at same action:
1. Analyze root cause
2. Try alternative approach
3. If truly blocked, commit partial work and exit:

pilot-signal {"v":2,"type":"exit","exit_signal":true,"success":false,"reason":"blocked: <why>"}
`
