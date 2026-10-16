package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// NavigatorBootstrapAuto runs the bootstrap once before the first task
	// whose README still carries a placeholder section.
	NavigatorBootstrapAuto = "auto"
	// NavigatorBootstrapManual never runs the bootstrap automatically; only
	// `pilot navigator bootstrap` does. This is the default.
	NavigatorBootstrapManual = "manual"
	// NavigatorBootstrapOff disables the automatic run.
	NavigatorBootstrapOff = "off"

	navigatorBootstrapMaxTurns = 60
	navigatorBootstrapTimeout  = 10 * time.Minute
)

// BootstrapMode returns the normalized navigator.bootstrap value: auto,
// manual or off. Empty, nil or unknown values mean manual.
func (c *NavigatorConfig) BootstrapMode() string {
	if c == nil {
		return NavigatorBootstrapManual
	}
	switch m := strings.ToLower(strings.TrimSpace(c.Bootstrap)); m {
	case NavigatorBootstrapAuto, NavigatorBootstrapOff:
		return m
	default:
		return NavigatorBootstrapManual
	}
}

// NavigatorBootstrapTools is the tool allowlist of the bootstrap session: the
// chat read-only tools plus Write and Edit restricted to the Navigator
// directory. A bare "Write" or "Edit" entry would let the session change
// tracked files and must never appear here.
func NavigatorBootstrapTools() []string {
	tools := append([]string(nil), ChatReadOnlyTools...)
	scope := "(" + navigatorDirName + "/**)"
	return append(tools, "Write"+scope, "Edit"+scope)
}

// NavigatorBootstrapResult is the outcome of ExecuteNavigatorBootstrap.
type NavigatorBootstrapResult struct {
	// TaskID is "bootstrap-<date>"; ExecutionID the executions row (empty
	// without a log store).
	TaskID      string
	ExecutionID string
	SessionID   string
	Model       string

	TokensInput              int64
	TokensOutput             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	// CostUSD is the token-based estimate; ReportedCostUSD the CLI-reported
	// figure when the backend carried one.
	CostUSD         float64
	ReportedCostUSD *float64
	KeySource       string
	Duration        time.Duration

	// FilesWritten are paths relative to the project that the session wrote
	// or edited under the Navigator directory and that exist afterwards.
	FilesWritten []string
	// Partial is true when the session stopped on max turns or the timeout.
	Partial bool
	// PlaceholdersRemain is true when a context section still carries
	// placeholder text after the session.
	PlaceholdersRemain bool
	// StrayWrites are paths relative to the project that the session changed
	// outside the Navigator directory and that were reverted or deleted
	// after the session.
	StrayWrites []string
}

// ExecuteNavigatorBootstrap runs one bootstrap session on projectPath through
// the backend chokepoint and records it as an executions row with task id
// bootstrap-<date>. It scaffolds the Navigator directory first when missing.
func (r *Runner) ExecuteNavigatorBootstrap(ctx context.Context, projectPath string) (*NavigatorBootstrapResult, error) {
	if r.backend == nil {
		return nil, errors.New("navigator bootstrap: no backend configured")
	}
	if err := ensureNavigatorScaffold(r, projectPath); err != nil {
		return nil, err
	}

	now := time.Now()
	date := now.Format("2006-01-02")
	out := &NavigatorBootstrapResult{
		TaskID:    "bootstrap-" + date,
		KeySource: KeySourceFromEnv(),
	}

	lifecycle := NewExecutionLifecycle(r.logStore)
	lifecycle.SetAlertProcessor(r.AlertProcessor())
	task := &Task{ID: out.TaskID, Title: "Navigator bootstrap", ProjectPath: projectPath}
	out.ExecutionID = r.beginBootstrapExecution(lifecycle, task)

	tctx, cancel := context.WithTimeout(ctx, navigatorBootstrapTimeout)
	defer cancel()

	var (
		mu          sync.Mutex
		touched     = map[string]struct{}{}
		maxTurnsHit bool
	)
	handler := func(ev BackendEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Type {
		case EventTypeToolUse:
			switch ev.ToolName {
			case "Write", "Edit", "MultiEdit":
				if p, ok := ev.ToolInput["file_path"].(string); ok {
					touched[p] = struct{}{}
				}
			}
		case EventTypeResult:
			if strings.Contains(ev.Raw, "error_max_turns") {
				maxTurnsHit = true
			}
		}
	}

	opts := ExecuteOptions{
		Prompt:       BuildNavigatorBootstrapPrompt(date),
		TaskID:       out.TaskID,
		ProjectPath:  projectPath,
		AllowedTools: NavigatorBootstrapTools(),
		// GH-5688: AllowedTools is inert under --dangerously-skip-permissions;
		// the guard hook and the post-session sweep are the real boundary.
		WriteBoundary: navigatorDirName,
		MaxTurns:      navigatorBootstrapMaxTurns,
		Model:         r.chatTurnModel(),
		EventHandler:  handler,
	}
	baseline := snapshotStrayBaseline(tctx, projectPath)

	type outcome struct {
		res *BackendResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := r.backendExecute(tctx, nil, projectPath, opts)
		done <- outcome{res, err}
	}()
	var (
		res *BackendResult
		err error
	)
	select {
	case o := <-done:
		res, err = o.res, o.err
	case <-tctx.Done():
		// A backend that ignores ctx must not hold the bootstrap open.
		err = tctx.Err()
	}
	out.Duration = time.Since(now)

	// Sweep on a fresh context: tctx may already be expired on a timeout.
	sweepCtx, sweepCancel := context.WithTimeout(context.Background(), 30*time.Second)
	out.StrayWrites = baseline.sweepStrayWrites(sweepCtx, r.log)
	sweepCancel()

	timedOut := errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	mu.Lock()
	out.Partial = timedOut || maxTurnsHit
	paths := make([]string, 0, len(touched))
	for p := range touched {
		paths = append(paths, p)
	}
	mu.Unlock()

	var model string

	if res != nil {
		out.SessionID = res.SessionID
		out.TokensInput = res.TokensInput
		out.TokensOutput = res.TokensOutput
		out.CacheCreationInputTokens = res.CacheCreationInputTokens
		out.CacheReadInputTokens = res.CacheReadInputTokens
		out.ReportedCostUSD = res.ReportedCostUSD
		if res.Model != "" {
			model = res.Model
		}
	}
	if model == "" {
		model = r.fallbackModelName()
	}
	out.Model = model
	out.CostUSD = estimateCostWithCache(out.TokensInput, out.TokensOutput, out.CacheCreationInputTokens, out.CacheReadInputTokens, model)
	out.FilesWritten = navigatorFilesWritten(projectPath, paths)
	out.PlaceholdersRemain = navigatorReadmeHasPlaceholder(projectPath)

	var runErr error
	switch {
	case out.Partial:
		// Partial work is kept; the flag tells the caller.
	case err != nil:
		runErr = err
	case res != nil && !res.Success && res.Error != "":
		runErr = errors.New(res.Error)
	}

	execResult := &ExecutionResult{
		TaskID:                   out.TaskID,
		Success:                  runErr == nil,
		TokensInput:              out.TokensInput,
		TokensOutput:             out.TokensOutput,
		TokensTotal:              out.TokensInput + out.TokensOutput,
		CacheCreationInputTokens: out.CacheCreationInputTokens,
		CacheReadInputTokens:     out.CacheReadInputTokens,
		EstimatedCostUSD:         out.CostUSD,
		ReportedCostUSD:          out.ReportedCostUSD,
		ModelName:                out.Model,
		FilesChanged:             len(out.FilesWritten),
		Duration:                 out.Duration,
	}
	if runErr != nil {
		execResult.Error = runErr.Error()
	}
	if out.ExecutionID != "" {
		if _, ferr := lifecycle.Finish(out.ExecutionID, execResult, runErr, out.Duration); ferr != nil {
			r.log.Warn("Failed to persist navigator bootstrap execution", slog.String("execution_id", out.ExecutionID), slog.Any("error", ferr))
		}
	}
	return out, runErr
}

// beginBootstrapExecution creates the executions row (running) with key_source
// like any execution. A repeat run on the same day claims the next
// generation. Returns "" when there is no store or the claim was lost.
func (r *Runner) beginBootstrapExecution(lifecycle *ExecutionLifecycle, task *Task) string {
	if r.logStore == nil {
		return ""
	}
	gen := 0
	if last, _, found, err := r.logStore.LatestClaimGeneration(task.ID, task.ProjectPath); err == nil && found {
		gen = last + 1
	}
	id, err := lifecycle.Begin(task, ExecStatusRunning, gen)
	if err != nil {
		r.log.Warn("Failed to record navigator bootstrap execution", slog.Any("error", err))
		return ""
	}
	return id
}

// ensureNavigatorScaffold creates the Navigator directory when missing so the
// README sections the session fills exist.
func ensureNavigatorScaffold(r *Runner, projectPath string) error {
	initializer, err := NewNavigatorInitializer(r.log)
	if err != nil {
		return fmt.Errorf("navigator bootstrap: %w", err)
	}
	if initializer.IsInitialized(projectPath) {
		return nil
	}
	if err := initializer.Initialize(projectPath); err != nil {
		return fmt.Errorf("navigator bootstrap: scaffold: %w", err)
	}
	return nil
}

// navigatorFilesWritten keeps the touched paths that lie under the Navigator
// directory and exist on disk. A path the CLI denied shows up as a tool_use
// event too, but leaves no file behind, so existence is the filter.
func navigatorFilesWritten(projectPath string, touched []string) []string {
	var files []string
	for _, p := range touched {
		rel := p
		if filepath.IsAbs(p) {
			var err error
			if rel, err = filepath.Rel(projectPath, p); err != nil {
				continue
			}
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		if !strings.HasPrefix(rel, navigatorDirName+"/") {
			continue
		}
		if _, err := os.Stat(filepath.Join(projectPath, filepath.FromSlash(rel))); err != nil {
			continue
		}
		files = append(files, rel)
	}
	sort.Strings(files)
	return files
}

// navigatorPlaceholderPhrases are the skeleton phrases the scaffold writes into
// the three context sections (contextInjectionSections). Compared lowercase
// with whitespace collapsed.
var navigatorPlaceholderPhrases = []string{
	"fill in during the first task session",
	"fill in as the project grows",
	"fill in the real source tree",
}

// navigatorReadmeHasPlaceholder reports whether any of the three context
// sections of the scaffold's DEVELOPMENT-README.md still carries skeleton text
// or is empty. A missing README or a missing section header is not a
// placeholder: only a scaffolded README is bootstrapped automatically.
func navigatorReadmeHasPlaceholder(projectPath string) bool {
	content, err := os.ReadFile(filepath.Join(projectPath, navigatorDirName, "DEVELOPMENT-README.md"))
	if err != nil {
		return false
	}
	text := string(content)
	for _, section := range contextInjectionSections {
		body, ok := readmeSectionBody(text, section.header)
		if !ok {
			continue
		}
		if strings.TrimSpace(body) == "" {
			return true
		}
		flat := strings.ToLower(strings.Join(strings.Fields(body), " "))
		for _, phrase := range navigatorPlaceholderPhrases {
			if strings.Contains(flat, phrase) {
				return true
			}
		}
	}
	return false
}

// readmeSectionBody returns the text under header up to the next heading of the
// same or a higher level.
func readmeSectionBody(text, header string) (string, bool) {
	level := len(header) - len(strings.TrimLeft(header, "#"))
	loc := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(header) + `[ \t]*$`).FindStringIndex(text)
	if loc == nil {
		return "", false
	}
	rest := text[loc[1]:]
	next := regexp.MustCompile(`(?m)^#{1,` + fmt.Sprint(level) + `} `).FindStringIndex(rest)
	if next != nil {
		rest = rest[:next[0]]
	}
	return rest, true
}

// maybeBootstrapNavigator runs the bootstrap once per project path before the
// first task when navigator.bootstrap is auto and the README still carries a
// placeholder section. Failures degrade gracefully: the task continues.
func (r *Runner) maybeBootstrapNavigator(ctx context.Context, projectPath string) {
	if r.config == nil || r.config.Navigator.BootstrapMode() != NavigatorBootstrapAuto {
		return
	}
	// GH-5732: `navigator: false` projects never get a bootstrap session.
	if r.navigatorEnabledLookup != nil && !r.navigatorEnabledLookup(projectPath) {
		r.log.Debug("Navigator bootstrap skipped: navigator disabled for project",
			slog.String("project_path", projectPath),
		)
		return
	}
	if !navigatorReadmeHasPlaceholder(projectPath) {
		return
	}
	// A failed or partial run must not retry on every task of this daemon.
	if _, loaded := r.navBootstrapAttempted.LoadOrStore(projectPath, struct{}{}); loaded {
		return
	}
	res, err := r.ExecuteNavigatorBootstrap(ctx, projectPath)
	if err != nil {
		r.log.Warn("Navigator bootstrap failed", slog.String("path", projectPath), slog.Any("error", err))
		return
	}
	r.log.Info("Navigator bootstrap finished",
		slog.String("path", projectPath),
		slog.Int("files_written", len(res.FilesWritten)),
		slog.Bool("partial", res.Partial),
		slog.Float64("cost_usd", res.CostUSD),
	)
}
