package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-5759: with a TelegramNotifier installed, one externally merged PR
// produces exactly one Telegram sendMessage, even across repeated ticks.
func TestController_TelegramNotifier_MergedSendsExactlyOneNotice(t *testing.T) {
	var tgSends atomic.Int32
	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tgSends.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer tgServer.Close()

	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls/42" {
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number: 42, State: "closed", Merged: true,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ghServer.Close()

	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	c := NewController(cfg, github.NewClientWithBaseURL(testutil.FakeGitHubToken, ghServer.URL), nil, "owner", "repo")
	c.SetNotifier(NewTelegramNotifier(telegram.NewClientWithBaseURL(testutil.FakeTelegramBotToken, tgServer.URL), "123", 7))

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")
	c.processAllPRs(context.Background())
	c.processAllPRs(context.Background())

	if got := tgSends.Load(); got != 1 {
		t.Errorf("Telegram sends = %d, want exactly 1", got)
	}
}

func TestController_HasNotifier(t *testing.T) {
	c := NewController(DefaultConfig(), nil, nil, "owner", "repo")
	if c.HasNotifier() {
		t.Error("new controller must have no notifier")
	}
	c.SetNotifier(NewTelegramNotifier(nil, "1", 0))
	if !c.HasNotifier() {
		t.Error("HasNotifier = false after SetNotifier")
	}
}
