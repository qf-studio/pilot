package comms

import (
	"os"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/intent"
	"github.com/qf-studio/pilot/internal/memory"
)

// ClassifierConfig is the adapter-agnostic config for LLM intent classification.
// Each adapter maps its own per-adapter YAML config into this struct before
// calling BuildHandler, so the bootstrap logic lives in exactly one place.
type ClassifierConfig struct {
	Enabled     bool
	APIKey      string
	HistorySize int
	HistoryTTL  time.Duration
}

// BuildClassifier bootstraps an intent classifier and conversation store.
// Returns (nil, nil) when disabled or no API key is available (env fallback included).
// Call sites do not need to guard for nil — comms.Handler handles nil classifiers gracefully.
func BuildClassifier(cfg *ClassifierConfig, executorBackend *executor.BackendConfig) (intent.Classifier, *intent.ConversationStore) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}

	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return nil, nil
	}

	client := intent.NewAnthropicClient(apiKey)
	if executorBackend != nil {
		if executorBackend.DefaultModel != "" {
			client.SetModel(executorBackend.DefaultModel)
		}
		if executorBackend.APIBaseURL != "" {
			client.SetAPIURL(executorBackend.APIBaseURL + "/v1/messages")
		}
	}

	historySize := 10
	if cfg.HistorySize > 0 {
		historySize = cfg.HistorySize
	}
	historyTTL := 30 * time.Minute
	if cfg.HistoryTTL > 0 {
		historyTTL = cfg.HistoryTTL
	}

	return client, intent.NewConversationStore(historySize, historyTTL)
}

// HandlerDeps holds the per-adapter inputs needed to build a comms.Handler.
// Pass this to BuildHandler — the only place HandlerConfig is assembled.
type HandlerDeps struct {
	Messenger      Messenger
	Runner         *executor.Runner
	Projects       ProjectSource
	ProjectPath    string
	RateLimit      *RateLimitConfig
	Classifier     *ClassifierConfig
	MemberResolver MemberResolver
	Store          *memory.Store
	TaskIDPrefix   string
	// ExecutorBackend is used by BuildClassifier to override the Anthropic model and URL.
	// May be nil — the factory handles that gracefully.
	ExecutorBackend *executor.BackendConfig
}

// BuildHandler creates a Handler from adapter deps.
// This is the single assembly point for HandlerConfig; all 5 adapter call sites
// route through here so no field can be silently omitted per-adapter.
func BuildHandler(deps HandlerDeps) *Handler {
	classifier, convStore := BuildClassifier(deps.Classifier, deps.ExecutorBackend)
	return NewHandler(&HandlerConfig{
		Messenger:      deps.Messenger,
		Runner:         deps.Runner,
		Projects:       deps.Projects,
		ProjectPath:    deps.ProjectPath,
		RateLimit:      deps.RateLimit,
		LLMClassifier:  classifier,
		ConvStore:      convStore,
		MemberResolver: deps.MemberResolver,
		Store:          deps.Store,
		TaskIDPrefix:   deps.TaskIDPrefix,
	})
}
