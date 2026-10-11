package pilot

import (
	"context"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/web"
	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

type stubChatIssueCreator struct{}

func (stubChatIssueCreator) CreateIssue(context.Context, string, comms.IssueDraft) (string, error) {
	return "https://example.test/issues/1", nil
}

func TestWithChatIssueCreator_ReachesHandlerDeps(t *testing.T) {
	runner := executor.NewRunnerWithBackend(&chatOnlyTestBackend{})
	runner.SetRecordingEnabled(false)
	runner.SetSkipPreflightChecks(true)

	cfg := config.DefaultConfig()
	cfg.Memory.Path = t.TempDir()
	cfg.Gateway.Port = 19098
	cfg.Adapters.Chat = &web.Config{Enabled: true}

	ic := &stubChatIssueCreator{}
	p, err := New(cfg, WithChatHandler(runner, t.TempDir()), WithChatIssueCreator(ic))
	if err != nil {
		t.Fatalf("pilot.New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	if p.chatIssueCreator != comms.IssueCreator(ic) {
		t.Fatal("WithChatIssueCreator did not store the creator on Pilot")
	}
	deps := p.chatHandlerDeps(web.NewMessenger(), "")
	if deps.IssueCreator != comms.IssueCreator(ic) {
		t.Fatal("gateway-mode HandlerDeps.IssueCreator is not the creator passed to WithChatIssueCreator")
	}

	// Without the option the deps carry no creator.
	p2, err := New(cfg, WithChatHandler(runner, t.TempDir()))
	if err != nil {
		t.Fatalf("pilot.New (no option): %v", err)
	}
	defer func() { _ = p2.Stop() }()
	if got := p2.chatHandlerDeps(web.NewMessenger(), "").IssueCreator; got != nil {
		t.Fatalf("IssueCreator = %v, want nil without the option", got)
	}
}
