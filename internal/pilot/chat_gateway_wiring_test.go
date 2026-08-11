package pilot

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/web"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

// chatOnlyTestBackend is a fake executor.Backend so this test can construct a
// real *executor.Runner without invoking a real Claude Code subprocess —
// mirrors internal/gateway/chat_test.go's chatTestBackend. The greeting path
// exercised below never actually reaches the backend (comms.Handler answers
// "hello" directly), but WithChatHandler requires a non-nil runner.
type chatOnlyTestBackend struct{}

func (b *chatOnlyTestBackend) Name() string      { return "chat-only-gateway-test-backend" }
func (b *chatOnlyTestBackend) IsAvailable() bool { return true }
func (b *chatOnlyTestBackend) Execute(ctx context.Context, opts executor.ExecuteOptions) (*executor.BackendResult, error) {
	return &executor.BackendResult{Success: true, Output: "chat-only gateway test task done"}, nil
}

// TestGatewayMode_ChatEnabledNoPollingAdapters_RoutesServe is the GH-4843 D1
// regression test: a gateway-mode daemon with the chat API enabled and ZERO
// polling adapters configured must still serve POST /api/v1/chat/messages
// and GET .../events. Before the fix, cmd/pilot/main.go's needsPollingInfra
// gated gwRunner construction and omitted chat entirely, so a chat-only
// deployment passed WithChatHandler(nil, ...) here — the guard at the top of
// this block (p.chatRunner != nil) then skipped SetChatAPI, leaving the
// routes unregistered (a 404) despite the startup log claiming "Chat API
// enabled in gateway mode". See gatewayChatNeedsOwnRunner in
// cmd/pilot/main.go for the fix to the caller-side decision; this test
// exercises the callee side (pilot.New + the real gateway HTTP routes) with
// the non-nil runner a correctly-wired caller now always supplies.
func TestGatewayMode_ChatEnabledNoPollingAdapters_RoutesServe(t *testing.T) {
	backend := &chatOnlyTestBackend{}
	runner := executor.NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)
	runner.SetSkipPreflightChecks(true)

	cfg := config.DefaultConfig()
	cfg.Memory.Path = t.TempDir()
	cfg.Gateway.Port = 19099 // distinct from other packages' fixed test ports (19199 collides with cmd/pilot/adapter_preflight_test.go)
	cfg.Adapters.Chat = &web.Config{Enabled: true}
	// Deliberately no Telegram/Slack/GitHub polling config — this is the
	// exact "webhooks-only / chat-only gateway daemon" shape D1 broke.

	projectPath := t.TempDir()
	p, err := New(cfg, WithChatHandler(runner, projectPath))
	if err != nil {
		t.Fatalf("pilot.New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	if err := p.Start(); err != nil {
		t.Fatalf("p.Start: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	client := &http.Client{Timeout: 10 * time.Second}
	baseURL := "http://127.0.0.1:19099"

	postResp, err := client.Post(baseURL+"/api/v1/chat/messages", "application/json",
		bytes.NewBufferString(`{"conversationId":"gh4843-conv","text":"hello"}`))
	if err != nil {
		t.Fatalf("POST chat/messages: %v", err)
	}
	defer func() { _ = postResp.Body.Close() }()
	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status = %d, want 202 (was 404 pre-GH-4843 fix)", postResp.StatusCode)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		getResp, getErr := client.Get(baseURL + "/api/v1/chat/conversations/gh4843-conv/events")
		if getErr != nil {
			t.Fatalf("GET events: %v", getErr)
		}
		if getResp.StatusCode != http.StatusOK {
			_ = getResp.Body.Close()
			t.Fatalf("GET status = %d, want 200 (was 404 pre-GH-4843 fix)", getResp.StatusCode)
		}
		var body struct {
			Events    []web.Event `json:"events"`
			LatestSeq int64       `json:"latestSeq"`
		}
		decodeErr := json.NewDecoder(getResp.Body).Decode(&body)
		_ = getResp.Body.Close()
		if decodeErr != nil {
			t.Fatalf("decode events body: %v", decodeErr)
		}
		if len(body.Events) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no chat event drained — chat routes did not actually wire through in gateway mode")
}
