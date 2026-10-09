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
	"Bash(git log:*)",
	"Bash(git show:*)",
	"Bash(git diff:*)",
	"Bash(git blame:*)",
	"Bash(git ls-files:*)",
	"Bash(rg:*)",
	"Bash(ls:*)",
}

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
}

// ExecuteChatTurn runs one read-only chat turn through the backend.
//
// Unlike Execute it builds no Task and touches no lifecycle: no task doc, no
// branch or worktree, no quality gates, no PR. It therefore passes a nil task
// to backendExecute, for which the worktree-isolation guard never fires.
func (r *Runner) ExecuteChatTurn(ctx context.Context, turn ChatTurn, onProgress func(message string)) (ChatTurnResult, error) {
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

	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		mu           sync.Mutex
		texts        []string
		eventSession string
		toolCalls    int
		maxTurnsHit  bool
	)
	progress := newChatProgressThrottle(onProgress, chatProgressInterval)
	defer progress.Close()

	handler := func(ev BackendEvent) {
		mu.Lock()
		defer mu.Unlock()
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
		case EventTypeResult:
			if strings.Contains(ev.Raw, "error_max_turns") {
				maxTurnsHit = true
			}
		}
	}

	run := func(resume string) (*BackendResult, error) {
		prompt := turn.Text
		if resume == "" {
			prompt = BuildChatPrompt(turn.ProjectPath, turn.Text)
		}
		opts := ExecuteOptions{
			Prompt:          prompt,
			ProjectPath:     turn.ProjectPath,
			ResumeSessionID: resume,
			AllowedTools:    ChatReadOnlyTools,
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
		res, err = run("")
	}

	mu.Lock()
	defer mu.Unlock()
	out := ChatTurnResult{
		SessionID:    eventSession,
		SessionReset: sessionReset,
		ToolCalls:    toolCalls,
	}
	if res != nil {
		if res.SessionID != "" {
			out.SessionID = res.SessionID
		}
		out.Text = res.Output
	}
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
	case "Task":
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
