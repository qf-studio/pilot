package pilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/slack"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/testutil"
)

// fakeSlackApprovalClient is an approval.SlackClient that records the card it
// "posts" so the test can click the exact card the handler sent.
type fakeSlackApprovalClient struct {
	mu     sync.Mutex
	posted int
}

func (c *fakeSlackApprovalClient) PostInteractiveMessage(_ context.Context, msg *approval.SlackInteractiveMessage) (*approval.SlackPostMessageResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.posted++
	return &approval.SlackPostMessageResponse{OK: true, TS: "1700000000.000100", Channel: msg.Channel}, nil
}

func (c *fakeSlackApprovalClient) UpdateInteractiveMessage(context.Context, string, string, []interface{}, string) error {
	return nil
}

func (c *fakeSlackApprovalClient) PostEphemeral(context.Context, string, string) error { return nil }

func (c *fakeSlackApprovalClient) PostEphemeralToUser(context.Context, string, string, string) error {
	return nil
}

func (c *fakeSlackApprovalClient) postedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.posted
}

// recordingStateWriter captures decisions the approval manager persists, which
// is the observable "the request resolved" signal in the autopilot pipeline.
type recordingStateWriter struct {
	mu        sync.Mutex
	decisions map[string]string
}

func (w *recordingStateWriter) SetApprovalDecision(_ context.Context, requestID, decision, _ string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.decisions == nil {
		w.decisions = map[string]string{}
	}
	w.decisions[requestID] = decision
	return nil
}

func (w *recordingStateWriter) get(requestID string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.decisions[requestID]
}

func slackApprovalTestConfig(t *testing.T, port int) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Memory.Path = t.TempDir()
	cfg.Gateway.Port = port
	cfg.Approval = approval.DefaultConfig()
	cfg.Approval.Enabled = true
	cfg.Approval.PreMerge = &approval.StageConfig{Enabled: true}
	cfg.Approval.DefaultTimeout = time.Hour
	cfg.Adapters.Slack = slack.DefaultConfig()
	cfg.Adapters.Slack.Enabled = true
	cfg.Adapters.Slack.BotToken = testutil.FakeSlackBotToken
	cfg.Adapters.Slack.Channel = "#approvals"
	cfg.Adapters.Slack.Approval = &slack.ApprovalConfig{Enabled: true}
	return cfg
}

// TestGatewayMode_SlackClickResolvesRequestFromMainManager is the GH-5758
// regression test. cmd/pilot builds the Slack approval stack the autopilot
// controllers use (main manager + handler); pilot.New used to build a second
// manager and handler and route the interactions webhook to the second one, so
// a click on a card the first handler sent hit an empty pending map and never
// resolved the request. With the main stack injected the click must resolve.
func TestGatewayMode_SlackClickResolvesRequestFromMainManager(t *testing.T) {
	const port = 19088
	cfg := slackApprovalTestConfig(t, port)

	// The stack cmd/pilot's gateway mode builds for the autopilot controllers.
	writer := &recordingStateWriter{}
	mainMgr := approval.NewManager(cfg.Approval).WithStateWriter(writer)
	client := &fakeSlackApprovalClient{}
	mainHdlr := approval.NewSlackHandler(client, "#approvals")
	mainHdlr.WithDecisionRecorder(mainMgr)
	mainMgr.RegisterHandler(mainHdlr)

	p, err := New(cfg, WithApprovalManager(mainMgr), WithSlackApprovalHandler(mainHdlr))
	if err != nil {
		t.Fatalf("pilot.New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	if p.approvalMgr != mainMgr {
		t.Fatal("pilot.New built its own approval manager despite an injected one")
	}
	if p.slackApprovalHdlr != mainHdlr {
		t.Fatal("pilot.New built its own Slack approval handler despite an injected one")
	}

	if err := p.Start(); err != nil {
		t.Fatalf("p.Start: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	// Send the approval card through the main manager.
	req := &approval.Request{
		ID:               "gh5758-req",
		TaskID:           "GH-5758",
		Stage:            approval.StagePreMerge,
		Title:            "merge ask",
		CreatedAt:        time.Now(),
		PreferredChannel: "slack",
	}
	if _, err := mainMgr.SubmitApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("SubmitApprovalRequest: %v", err)
	}
	if got := client.postedCount(); got != 1 {
		t.Fatalf("cards posted = %d, want 1", got)
	}

	// Click Approve through the route the gateway exposes.
	payload, err := json.Marshal(map[string]interface{}{
		"type":    "block_actions",
		"user":    map[string]string{"id": "U1", "username": "alice"},
		"channel": map[string]string{"id": "C1"},
		"actions": []map[string]string{{"action_id": "approve", "value": "approve:" + req.ID}},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	form := url.Values{"payload": {string(payload)}}
	httpReq, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:19088/webhooks/slack/interactions", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(httpReq)
	if err != nil {
		t.Fatalf("POST interactions: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("interactions status = %d, want 200", resp.StatusCode)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && writer.get(req.ID) == "" {
		time.Sleep(20 * time.Millisecond)
	}
	if got := writer.get(req.ID); got != string(approval.DecisionApproved) {
		t.Fatalf("recorded decision for %s = %q, want %q (click did not reach the handler that sent the card)", req.ID, got, approval.DecisionApproved)
	}
}

// TestNew_SlackApprovalWithoutInjection_BuildsSingleStack pins the standalone
// path (gateway mode without polling infra): pilot.New is then the only
// builder, so it still creates one manager + one handler and serves the route.
func TestNew_SlackApprovalWithoutInjection_BuildsSingleStack(t *testing.T) {
	cfg := slackApprovalTestConfig(t, 19087)

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("pilot.New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	if p.approvalMgr == nil || p.slackApprovalHdlr == nil || p.slackInteractionWH == nil {
		t.Fatalf("standalone stack incomplete: mgr=%v hdlr=%v webhook=%v",
			p.approvalMgr != nil, p.slackApprovalHdlr != nil, p.slackInteractionWH != nil)
	}
}

// TestNew_InjectedManagerWithoutSlackHandler_BuildsNoSlackStack covers an
// injected main manager where Slack approvals are not wired (e.g. bot token
// missing): pilot.New must not fall back to building a second handler.
func TestNew_InjectedManagerWithoutSlackHandler_BuildsNoSlackStack(t *testing.T) {
	cfg := slackApprovalTestConfig(t, 19086)
	mainMgr := approval.NewManager(cfg.Approval)

	p, err := New(cfg, WithApprovalManager(mainMgr))
	if err != nil {
		t.Fatalf("pilot.New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	if p.approvalMgr != mainMgr {
		t.Fatal("pilot.New replaced the injected approval manager")
	}
	if p.slackApprovalHdlr != nil || p.slackInteractionWH != nil {
		t.Fatal("pilot.New built a second Slack approval stack next to the injected manager")
	}
}
