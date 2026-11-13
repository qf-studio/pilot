package executor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	defaultChatTurnMaxTurns = 40
	defaultChatTurnTimeout  = 5 * time.Minute
	chatProgressArgMax      = 80
)

// chatProgressInterval is the minimum gap between two progress lines
// delivered to onProgress. A package variable so tests can disable it.
var chatProgressInterval = 500 * time.Millisecond

// ChatReadOnlyTools is the tool allowlist for a chat turn. Order is part of
// the contract. There is deliberately no Edit, Write, MultiEdit,
// NotebookEdit or unscoped Bash entry.
var ChatReadOnlyTools = []string{
	"Read",
	"Grep",
	"Glob",
	"LS",
	"Task",
	"Agent",
	"Bash(git log:*)",
	"Bash(git show:*)",
	"Bash(git diff:*)",
	"Bash(git blame:*)",
	"Bash(git ls-files:*)",
	"Bash(rg:*)",
	"Bash(ls:*)",
}

// chatDeniedTools is the deny list of a chat turn. GH-5688: ChatReadOnlyTools
// is an auto-approve list and does nothing under --dangerously-skip-permissions;
// a deny list holds there.
var chatDeniedTools = []string{"Write", "Edit", "MultiEdit", "NotebookEdit"}

// ChatTurn describes one read-only chat turn.
type ChatTurn struct {
	ConversationID  string
	ProjectPath     string
	Text            string
	ResumeSessionID string
	// MaxTurns caps agentic turns; 40 when zero.
	MaxTurns int
	// Timeout bounds the whole turn; 5 minutes when zero.
	Timeout time.Duration
	// OnSubagent, when non-nil, is called when a sub-agent (Task/Agent tool)
	// starts and when it finishes; it is not subject to the progress throttle
	// and is called from the backend event goroutine, so it must not block.
	OnSubagent func(SubagentEvent)
}

// Sub-agent phases carried by SubagentEvent.Phase.
const (
	SubagentPhaseRunning = "running"
	SubagentPhaseDone    = "done"
)

// SubagentEvent reports one sub-agent of a chat turn. TaskID is the Task/Agent
// tool_use id; Text is a short label; Phase is SubagentPhaseRunning or
// SubagentPhaseDone; DurationMs is set on done only.
type SubagentEvent struct {
	TaskID     string
	Text       string
	Phase      string
	DurationMs int64
}

// Navigator paths counted by the chat turn's read statistics.
const (
	chatMemoriesDir = ".agent/knowledge/memories/"
	chatAgentDir    = ".agent/"
)

// openSubagent is a sub-agent the chat turn has seen start and not yet finish.
type openSubagent struct {
	text        string
	started     time.Time
	runningSent bool
}

// ChatTurnResult is the outcome of ExecuteChatTurn.
type ChatTurnResult struct {
	Text      string
	SessionID string
	// Partial is true when the backend stopped on MaxTurns or the timeout.
	Partial bool
	// SessionReset is true when the resumed session was not found and the
	// turn ran without resume.
	SessionReset bool
	ToolCalls    int
	// DocsRead and MemoriesRead count the distinct .agent docs and
	// .agent/knowledge/memories files the model read (Read tool_use, main
	// agent or sub-agent) during this turn. A resumed session counts this
	// turn only, not the earlier turns of the session.
	DocsRead     int
	MemoriesRead int
	// BackendRan is true once the backend was invoked, whether the turn then
	// succeeded or failed. False when the turn ended before it (billing
	// preflight, no backend), so DocsRead and MemoriesRead are not a count.
	BackendRan bool

	// Usage of the backend run (GH-5677). Zero when the backend returned no
	// result (timeout, spawn failure).
	TokensInput      int64
	TokensOutput     int64
	TokensCacheRead  int64
	TokensCacheWrite int64
	// EstimatedCostUSD is the list-price estimate, the same estimator the
	// runner uses for executions.
	EstimatedCostUSD float64
	// ReportedCostUSD is the cost the CLI reported; nil when it reported none.
	ReportedCostUSD *float64
	Model           string
	Duration        time.Duration
	// BillingMode is the mode the turn ran in: subscription, api_key or
	// unknown (GH-6016).
	BillingMode string
}

// ExecuteChatTurn runs one read-only chat turn through the backend.
//
// Unlike Execute it builds no Task and touches no lifecycle: no task doc, no
// branch or worktree, no quality gates, no PR. It therefore passes a nil task
// to backendExecute, for which the worktree-isolation guard never fires.
func (r *Runner) ExecuteChatTurn(ctx context.Context, turn ChatTurn, onProgress func(message string)) (ChatTurnResult, error) {
	// GH-6016: one credential resolution per chat turn, before the spawn. An
	// api_key mode key that does not resolve fails the turn closed.
	ctx, cred, release, err := r.billingContext(ctx, turn.ConversationID, turn.ProjectPath, "chat")
	if err != nil {
		return ChatTurnResult{BillingMode: string(BillingModeAPIKey)}, err
	}
	defer release()
	result, err := r.executeChatTurn(ctx, turn, onProgress)
	result.BillingMode = cred.MemoryMode()
	return result, err
}

func (r *Runner) executeChatTurn(ctx context.Context, turn ChatTurn, onProgress func(message string)) (ChatTurnResult, error) {
	if r.backend == nil {
		return ChatTurnResult{}, errors.New("chat turn: no backend configured")
	}
	maxTurns := turn.MaxTurns
	if maxTurns == 0 {
		maxTurns = defaultChatTurnMaxTurns
	}
	timeout := turn.Timeout
	if timeout == 0 {
		timeout = defaultChatTurnTimeout
	}

	started := time.Now()
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		mu           sync.Mutex
		texts        []string
		eventSession string
		toolCalls    int
		maxTurnsHit  bool
		// Sub-agent and read tracking; all guarded by mu.
		openAgents  = map[string]*openSubagent{}
		agentOrder  []string
		docsRead    = map[string]struct{}{}
		memsRead    = map[string]struct{}{}
		turnClosed  bool
		projectBase = strings.TrimRight(turn.ProjectPath, "/")
	)
	emitSubagent := func(e SubagentEvent) {
		if turn.OnSubagent != nil {
			turn.OnSubagent(e)
		}
	}
	// finishAgents reports done for every still-open sub-agent so no row stays
	// spinning; mu must be held. A row whose running was never delivered gets
	// it first, so done never precedes running.
	finishAgents := func() {
		for _, id := range agentOrder {
			ag, ok := openAgents[id]
			if !ok {
				continue
			}
			if !ag.runningSent {
				emitSubagent(SubagentEvent{TaskID: id, Text: ag.text, Phase: SubagentPhaseRunning})
			}
			emitSubagent(SubagentEvent{TaskID: id, Text: ag.text, Phase: SubagentPhaseDone, DurationMs: time.Since(ag.started).Milliseconds()})
		}
		openAgents = map[string]*openSubagent{}
		agentOrder = nil
	}
	recordRead := func(input map[string]interface{}) {
		path, _ := input["file_path"].(string)
		path = strings.TrimPrefix(path, projectBase+"/")
		switch {
		case strings.Contains(path, chatMemoriesDir):
			memsRead[path] = struct{}{}
		case strings.HasPrefix(path, chatAgentDir) || strings.Contains(path, "/"+chatAgentDir):
			docsRead[path] = struct{}{}
		}
	}
	progress := newChatProgressThrottle(onProgress, chatProgressInterval)
	defer progress.Close()

	handler := func(ev BackendEvent) {
		mu.Lock()
		defer mu.Unlock()
		if turnClosed {
			return // a backend that outlived the turn must not reopen rows
		}
		if ev.SessionID != "" {
			eventSession = ev.SessionID
		}
		switch ev.Type {
		case EventTypeText:
			if strings.TrimSpace(ev.Message) != "" {
				texts = append(texts, ev.Message)
			}
		case EventTypeToolUse:
			toolCalls++
			progress.Offer(chatProgressLine(ev))
			switch ev.ToolName {
			case "Task", "Agent":
				if ev.ParentToolUseID != "" || ev.ToolUseID == "" || openAgents[ev.ToolUseID] != nil {
					break
				}
				ag := &openSubagent{text: subagentText(ev), started: time.Now()}
				openAgents[ev.ToolUseID] = ag
				agentOrder = append(agentOrder, ev.ToolUseID)
				emitSubagent(SubagentEvent{TaskID: ev.ToolUseID, Text: ag.text, Phase: SubagentPhaseRunning})
				ag.runningSent = true
			case "Read":
				recordRead(ev.ToolInput)
			}
		case EventTypeToolResult:
			if ag := openAgents[ev.ToolUseID]; ag != nil {
				emitSubagent(SubagentEvent{TaskID: ev.ToolUseID, Text: ag.text, Phase: SubagentPhaseDone, DurationMs: time.Since(ag.started).Milliseconds()})
				delete(openAgents, ev.ToolUseID)
			}
		case EventTypeResult:
			if strings.Contains(ev.Raw, "error_max_turns") {
				maxTurnsHit = true
			}
		}
	}

	run := func(resume string) (*BackendResult, error) {
		prompt := turn.Text
		if resume == "" {
			prompt = buildChatPromptWithLearnings(turn.ProjectPath, turn.Text, r.buildConsoleLearningsBlock(turn.ProjectPath))
		}
		opts := ExecuteOptions{
			Prompt:          prompt,
			ProjectPath:     turn.ProjectPath,
			ResumeSessionID: resume,
			AllowedTools:    ChatReadOnlyTools,
			DisallowedTools: chatDeniedTools,
			WriteBoundary:   WriteBoundaryDenyAll,
			MaxTurns:        maxTurns,
			Model:           r.chatTurnModel(),
			EventHandler:    handler,
		}
		type outcome struct {
			res *BackendResult
			err error
		}
		done := make(chan outcome, 1)
		go func() {
			res, err := r.backendExecute(tctx, nil, turn.ProjectPath, opts)
			done <- outcome{res, err}
		}()
		select {
		case o := <-done:
			return o.res, o.err
		case <-tctx.Done():
			// A backend that ignores ctx must not hold the chat open.
			return nil, tctx.Err()
		}
	}

	var sessionReset bool
	res, err := run(turn.ResumeSessionID)
	if err != nil && turn.ResumeSessionID != "" && isSessionNotFound(err, res) {
		sessionReset = true
		// Counts cover one attempt: close the failed attempt's rows and reset.
		mu.Lock()
		finishAgents()
		docsRead = map[string]struct{}{}
		memsRead = map[string]struct{}{}
		toolCalls = 0
		mu.Unlock()
		res, err = run("")
	}

	mu.Lock()
	defer mu.Unlock()
	finishAgents()
	turnClosed = true
	out := ChatTurnResult{
		SessionID:    eventSession,
		SessionReset: sessionReset,
		BackendRan:   true,
		ToolCalls:    toolCalls,
		DocsRead:     len(docsRead),
		MemoriesRead: len(memsRead),
	}
	if res != nil {
		if res.SessionID != "" {
			out.SessionID = res.SessionID
		}
		out.Text = res.Output
		out.TokensInput = res.TokensInput
		out.TokensOutput = res.TokensOutput
		out.TokensCacheRead = res.CacheReadInputTokens
		out.TokensCacheWrite = res.CacheCreationInputTokens
		out.ReportedCostUSD = res.ReportedCostUSD
		out.Model = res.Model
	}
	if out.Model == "" {
		out.Model = r.chatTurnModel()
	}
	var cacheWrite1h int64
	if res != nil {
		cacheWrite1h = res.CacheCreation1hInputTokens
	}
	out.EstimatedCostUSD = estimateCostWithCacheTTL(out.TokensInput, out.TokensOutput, out.TokensCacheWrite, cacheWrite1h, out.TokensCacheRead, out.Model)
	out.Duration = time.Since(started)
	collected := strings.Join(texts, "\n\n")

	timedOut := errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	switch {
	case timedOut || maxTurnsHit:
		out.Partial = true
		if out.Text == "" {
			out.Text = collected
		}
		return out, nil
	case err != nil:
		return out, err
	case res != nil && !res.Success && res.Error != "":
		return out, errors.New(res.Error)
	}
	if out.Text == "" {
		out.Text = collected
	}
	return out, nil
}

// chatTurnModel mirrors resolveSelectedModel without a Task: Claude Code
// keeps its own model configuration, other backends use the runner default.
func (r *Runner) chatTurnModel() string {
	if r.config == nil || r.config.Type == BackendTypeClaudeCode {
		return ""
	}
	return r.config.DefaultModel
}

func isSessionNotFound(err error, res *BackendResult) bool {
	var be BackendError
	if errors.As(err, &be) && be.ErrorType() == string(ErrorTypeSessionNotFound) {
		return true
	}
	return res != nil && res.ErrorType == string(ErrorTypeSessionNotFound)
}

// subagentText labels a sub-agent row: the tool input description, else the
// first chatProgressArgMax runes of its prompt, else the tool name.
func subagentText(ev BackendEvent) string {
	if d, _ := ev.ToolInput["description"].(string); strings.TrimSpace(d) != "" {
		return strings.TrimSpace(d)
	}
	if p, _ := ev.ToolInput["prompt"].(string); strings.TrimSpace(p) != "" {
		rs := []rune(strings.TrimSpace(strings.ReplaceAll(p, "\n", " ")))
		if len(rs) > chatProgressArgMax {
			rs = rs[:chatProgressArgMax]
		}
		return string(rs)
	}
	return ev.ToolName
}

// chatProgressLine renders a tool_use event as "<tool> <first argument>".
func chatProgressLine(ev BackendEvent) string {
	key := ""
	switch ev.ToolName {
	case "Read":
		key = "file_path"
	case "Grep", "Glob":
		key = "pattern"
	case "Bash":
		key = "command"
	case "Task", "Agent":
		key = "description"
	case "LS":
		key = "path"
	}
	arg := ""
	if key != "" {
		if s, ok := ev.ToolInput[key].(string); ok {
			arg = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
		}
	}
	if ev.ToolName == "Bash" {
		if rs := []rune(arg); len(rs) > chatProgressArgMax {
			arg = string(rs[:chatProgressArgMax])
		}
	}
	if arg == "" {
		return ev.ToolName
	}
	return ev.ToolName + " " + arg
}

// chatProgressThrottle drops consecutive duplicates and delivers at most one
// line per interval; while throttled the latest pending line replaces older
// ones and is flushed when the interval elapses.
type chatProgressThrottle struct {
	mu       sync.Mutex
	deliver  func(string)
	interval time.Duration
	last     string // last line offered, for dedupe
	lastSent time.Time
	pending  string
	hasPend  bool
	timer    *time.Timer
	closed   bool
}

func newChatProgressThrottle(deliver func(string), interval time.Duration) *chatProgressThrottle {
	return &chatProgressThrottle{deliver: deliver, interval: interval}
}

func (p *chatProgressThrottle) Offer(line string) {
	if p.deliver == nil || line == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || line == p.last {
		return
	}
	p.last = line
	wait := p.interval - time.Since(p.lastSent)
	if p.lastSent.IsZero() || wait <= 0 {
		p.sendLocked(line)
		return
	}
	p.pending, p.hasPend = line, true
	if p.timer == nil {
		p.timer = time.AfterFunc(wait, p.flush)
	}
}

func (p *chatProgressThrottle) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.timer = nil
	if p.closed || !p.hasPend {
		return
	}
	line := p.pending
	p.pending, p.hasPend = "", false
	p.sendLocked(line)
}

func (p *chatProgressThrottle) sendLocked(line string) {
	p.lastSent = time.Now()
	p.deliver(line)
}

// Close stops delivery; a still-pending line is dropped because the answer
// supersedes it.
func (p *chatProgressThrottle) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
}
