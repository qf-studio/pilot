package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/web"
	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/executor"
)

// --- mockChatAPI: unit-level direct-handler-call tests --------------------

// mockChatAPI implements ChatAPI with configurable func fields, mirroring the
// mockDashboardStore / mockDecisionRecorder convention already used in this
// package's *_test.go files.
type mockChatAPI struct {
	dispatchFunc func(ctx context.Context, req web.DispatchRequest) (int64, error)
	eventsFunc   func(conversationID string, after int64) ([]web.Event, int64)
}

func (m *mockChatAPI) Dispatch(ctx context.Context, req web.DispatchRequest) (int64, error) {
	if m.dispatchFunc != nil {
		return m.dispatchFunc(ctx, req)
	}
	return 0, nil
}

func (m *mockChatAPI) Events(conversationID string, after int64) ([]web.Event, int64) {
	if m.eventsFunc != nil {
		return m.eventsFunc(conversationID, after)
	}
	return nil, 0
}

func newTestServerWithChat(api ChatAPI) *Server {
	s := NewServer(&Config{Host: "127.0.0.1", Port: 0})
	s.SetChatAPI(api)
	s.mu.Lock()
	s.daemonCtx = context.Background()
	s.mu.Unlock()
	return s
}

func jsonBody(t *testing.T, v interface{}) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return bytes.NewBuffer(b)
}

func TestHandleChatMessages_MethodNotAllowed(t *testing.T) {
	s := newTestServerWithChat(&mockChatAPI{})
	req := httpTestRequest(t, http.MethodGet, "/api/v1/chat/messages", nil)
	w := newTestResponseRecorder()
	s.handleChatMessages(w, req)
	if w.status != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.status)
	}
}

func TestHandleChatMessages_NilChatAPI_503(t *testing.T) {
	s := NewServer(&Config{Host: "127.0.0.1", Port: 0})
	s.mu.Lock()
	s.daemonCtx = context.Background()
	s.mu.Unlock()

	req := httpTestRequest(t, http.MethodPost, "/api/v1/chat/messages",
		jsonBody(t, chatMessageRequest{ConversationID: "c1", Text: "hi"}))
	w := newTestResponseRecorder()
	s.handleChatMessages(w, req)
	if w.status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.status)
	}
}

func TestHandleChatMessages_NilDaemonCtx_503(t *testing.T) {
	s := NewServer(&Config{Host: "127.0.0.1", Port: 0})
	s.SetChatAPI(&mockChatAPI{})
	// daemonCtx deliberately left nil: Start() hasn't run yet.

	req := httpTestRequest(t, http.MethodPost, "/api/v1/chat/messages",
		jsonBody(t, chatMessageRequest{ConversationID: "c1", Text: "hi"}))
	w := newTestResponseRecorder()
	s.handleChatMessages(w, req)
	if w.status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.status)
	}
}

func TestHandleChatMessages_InvalidJSON_400(t *testing.T) {
	s := newTestServerWithChat(&mockChatAPI{})
	req := httpTestRequest(t, http.MethodPost, "/api/v1/chat/messages", bytes.NewBufferString("{not json"))
	w := newTestResponseRecorder()
	s.handleChatMessages(w, req)
	if w.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.status)
	}
}

func TestHandleChatMessages_ValidationErrors_400(t *testing.T) {
	cases := []error{
		web.ErrMissingConversationID,
		web.ErrMissingText,
		web.ErrMissingCallbackFields,
		web.ErrApprovalCallback,
	}
	for _, wantErr := range cases {
		api := &mockChatAPI{
			dispatchFunc: func(ctx context.Context, req web.DispatchRequest) (int64, error) {
				return 0, wantErr
			},
		}
		s := newTestServerWithChat(api)
		req := httpTestRequest(t, http.MethodPost, "/api/v1/chat/messages",
			jsonBody(t, chatMessageRequest{ConversationID: "c1", Text: "hi"}))
		w := newTestResponseRecorder()
		s.handleChatMessages(w, req)
		if w.status != http.StatusBadRequest {
			t.Errorf("err=%v status = %d, want 400", wantErr, w.status)
		}
	}
}

func TestHandleChatMessages_OtherDispatchError_500(t *testing.T) {
	api := &mockChatAPI{
		dispatchFunc: func(ctx context.Context, req web.DispatchRequest) (int64, error) {
			return 0, errors.New("boom")
		},
	}
	s := newTestServerWithChat(api)
	req := httpTestRequest(t, http.MethodPost, "/api/v1/chat/messages",
		jsonBody(t, chatMessageRequest{ConversationID: "c1", Text: "hi"}))
	w := newTestResponseRecorder()
	s.handleChatMessages(w, req)
	if w.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.status)
	}
}

func TestHandleChatMessages_Success_202(t *testing.T) {
	var gotCtx context.Context
	var gotReq web.DispatchRequest
	api := &mockChatAPI{
		dispatchFunc: func(ctx context.Context, req web.DispatchRequest) (int64, error) {
			gotCtx = ctx
			gotReq = req
			return 7, nil
		},
	}
	s := newTestServerWithChat(api)
	req := httpTestRequest(t, http.MethodPost, "/api/v1/chat/messages",
		jsonBody(t, chatMessageRequest{ConversationID: "c1", Text: "hello", Sender: "operator"}))
	w := newTestResponseRecorder()
	s.handleChatMessages(w, req)

	if w.status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.status)
	}
	var resp chatMessageResponse
	if err := json.Unmarshal(w.body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp.ConversationID != "c1" || resp.Seq != 7 {
		t.Errorf("resp = %+v, want {c1 7}", resp)
	}
	if gotReq.Text != "hello" || gotReq.Sender != "operator" {
		t.Errorf("dispatched req = %+v", gotReq)
	}
	// Dispatch must be called with the server's daemon context, not a
	// request-derived one — since req has no cancellation wired here, assert
	// identity against the stashed daemonCtx directly.
	s.mu.RLock()
	wantCtx := s.daemonCtx
	s.mu.RUnlock()
	if gotCtx != wantCtx {
		t.Errorf("Dispatch was not called with the daemon context")
	}
}

func TestHandleChatEvents_MethodNotAllowed(t *testing.T) {
	s := newTestServerWithChat(&mockChatAPI{})
	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/chat/conversations/c1/events", nil, "id", "c1")
	w := newTestResponseRecorder()
	s.handleChatEvents(w, req)
	if w.status != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.status)
	}
}

func TestHandleChatEvents_NilChatAPI_503(t *testing.T) {
	s := NewServer(&Config{Host: "127.0.0.1", Port: 0})
	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/chat/conversations/c1/events", nil, "id", "c1")
	w := newTestResponseRecorder()
	s.handleChatEvents(w, req)
	if w.status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.status)
	}
}

func TestHandleChatEvents_MissingID_400(t *testing.T) {
	s := newTestServerWithChat(&mockChatAPI{})
	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/chat/conversations//events", nil, "id", "")
	w := newTestResponseRecorder()
	s.handleChatEvents(w, req)
	if w.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.status)
	}
}

func TestHandleChatEvents_InvalidAfter_400(t *testing.T) {
	s := newTestServerWithChat(&mockChatAPI{})
	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/chat/conversations/c1/events?after=nope", nil, "id", "c1")
	w := newTestResponseRecorder()
	s.handleChatEvents(w, req)
	if w.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.status)
	}
}

func TestHandleChatEvents_Success_200(t *testing.T) {
	want := []web.Event{{Seq: 1, Type: web.EventText, Text: "hi"}}
	var gotID string
	var gotAfter int64
	api := &mockChatAPI{
		eventsFunc: func(conversationID string, after int64) ([]web.Event, int64) {
			gotID = conversationID
			gotAfter = after
			return want, 9
		},
	}
	s := newTestServerWithChat(api)
	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/chat/conversations/c1/events?after=5", nil, "id", "c1")
	w := newTestResponseRecorder()
	s.handleChatEvents(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.status)
	}
	if gotID != "c1" || gotAfter != 5 {
		t.Errorf("Events called with (%q, %d), want (c1, 5)", gotID, gotAfter)
	}
	var resp chatEventsResponse
	if err := json.Unmarshal(w.body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp.ConversationID != "c1" || len(resp.Events) != 1 || resp.Events[0].Text != "hi" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.LatestSeq != 9 {
		t.Errorf("resp.LatestSeq = %d, want 9", resp.LatestSeq)
	}
}

// TestHandleChatEvents_UnknownConversation_EventsIsEmptyArrayNotNull covers
// GH-4843 D3: the wire body for an unknown/expired conversation must contain
// `"events": []`, not `"events": null` — a nil slice serializes to null,
// which forces every client to null-check before iterating.
func TestHandleChatEvents_UnknownConversation_EventsIsEmptyArrayNotNull(t *testing.T) {
	api := &mockChatAPI{
		eventsFunc: func(conversationID string, after int64) ([]web.Event, int64) {
			return nil, 0
		},
	}
	s := newTestServerWithChat(api)
	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/chat/conversations/unknown/events", nil, "id", "unknown")
	w := newTestResponseRecorder()
	s.handleChatEvents(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.status)
	}
	if !bytes.Contains(w.body.Bytes(), []byte(`"events":[]`)) {
		t.Errorf("body = %s, want \"events\":[] present (not null)", w.body.String())
	}
}

// TestHandleChatEvents_LatestSeqSignalsReset covers GH-4843 D2: a client
// whose `after` cursor exceeds the response's latestSeq (server buffer
// reset — daemon restart or conversation-expiry eviction) must be able to
// detect that from the GET response alone, with no separate signal needed.
func TestHandleChatEvents_LatestSeqSignalsReset(t *testing.T) {
	api := &mockChatAPI{
		eventsFunc: func(conversationID string, after int64) ([]web.Event, int64) {
			// Fresh post-restart buffer: no events, latestSeq back at 0,
			// while the client polls with a stale cursor from before restart.
			return nil, 0
		},
	}
	s := newTestServerWithChat(api)
	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/chat/conversations/c1/events?after=100", nil, "id", "c1")
	w := newTestResponseRecorder()
	s.handleChatEvents(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.status)
	}
	var resp chatEventsResponse
	if err := json.Unmarshal(w.body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	const clientCursor = 100
	if resp.LatestSeq >= clientCursor {
		t.Fatalf("resp.LatestSeq = %d, want < %d so the client can detect a reset", resp.LatestSeq, clientCursor)
	}
}

// --- composed / integration tests: real store + real mux + real routing ---

// TestChatAPI_RoutesAbsentWhenDisabled asserts that when SetChatAPI is never
// called (adapters.chat.enabled: false, the config-level default), the chat
// routes are not registered at all — a request 404s like any unknown path
// rather than 503, so "disabled" and "misconfigured" are distinguishable at
// the routing layer (see chat.go's SetChatAPI doc comment).
func TestChatAPI_RoutesAbsentWhenDisabled(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 19096}
	authConfig := &AuthConfig{Type: AuthTypeAPIToken, Token: "chat-disabled-secret"}
	server := NewServerWithAuth(config, authConfig)
	// SetChatAPI intentionally not called.

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://127.0.0.1:19096"

	postReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/chat/messages",
		bytes.NewBufferString(`{"conversationId":"c1","text":"hi"}`))
	postReq.Header.Set("Authorization", "Bearer chat-disabled-secret")
	postResp, err := client.Do(postReq)
	if err != nil {
		t.Fatalf("POST chat/messages: %v", err)
	}
	_ = postResp.Body.Close()
	if postResp.StatusCode != http.StatusNotFound {
		t.Errorf("POST status = %d, want 404 (route not registered)", postResp.StatusCode)
	}

	getReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/chat/conversations/c1/events", nil)
	getReq.Header.Set("Authorization", "Bearer chat-disabled-secret")
	getResp, err := client.Do(getReq)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("GET status = %d, want 404 (route not registered)", getResp.StatusCode)
	}
}

// chatTestBackend is a fake executor.Backend (GH-4835): it lets the composed
// test drive a real *executor.Runner end to end without invoking a real Claude
// Code subprocess. A prompt containing "file an issue" replays one Read tool
// call and answers with a valid pilot-issue block (GH-5638).
type chatTestBackend struct{}

const chatBackendOutput = "chat test task done"

const chatIssueOutput = "Here is the draft.\n\n```pilot-issue\n" +
	"title: \"feat(chat): composed test issue\"\nlabels: [pilot]\nbody: |\n" +
	"  ## Context\n  Composed test.\n  ## Scope\n  One file.\n  ## Acceptance Criteria\n  Passes.\n" +
	"  ## Out of scope\n  Everything else.\n  ## Refs\n  None.\n```"

func (b *chatTestBackend) Name() string      { return "chat-test-backend" }
func (b *chatTestBackend) IsAvailable() bool { return true }
func (b *chatTestBackend) Execute(ctx context.Context, opts executor.ExecuteOptions) (*executor.BackendResult, error) {
	if strings.Contains(opts.Prompt, "file an issue") {
		opts.EventHandler(executor.BackendEvent{
			Type:      executor.EventTypeToolUse,
			ToolName:  "Read",
			ToolInput: map[string]interface{}{"file_path": "README.md"},
		})
		return &executor.BackendResult{Success: true, Output: chatIssueOutput}, nil
	}
	return &executor.BackendResult{Success: true, Output: chatBackendOutput}, nil
}

// chatIssueCreator is a stub comms.IssueCreator recording its calls.
type chatIssueCreator struct {
	calls []comms.IssueDraft
	err   error
}

func (c *chatIssueCreator) CreateIssue(_ context.Context, _ string, d comms.IssueDraft) (string, error) {
	c.calls = append(c.calls, d)
	return "https://github.com/qf-studio/pilot/issues/1", c.err
}

// TestChatAPI_Composed exercises the full GH-4835 acceptance flow on a real
// HTTP listener: auth enforcement, an operational-query text reply, and a
// chat-turn text reply, and the pilot-issue flow (progress -> confirmation ->
// execute -> filed URL) with a fake executor backend and issue creator, plus
// the approvals scope fence (#4748 / GH-4411 / GH-4431).
func TestChatAPI_Composed(t *testing.T) {
	backend := &chatTestBackend{}
	runner := executor.NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)
	runner.SetSkipPreflightChecks(true)

	creator := &chatIssueCreator{}
	messenger := web.NewMessenger()
	handler := comms.BuildHandler(comms.HandlerDeps{
		Messenger:    messenger,
		Runner:       runner,
		ProjectPath:  t.TempDir(),
		TaskIDPrefix: "WEBTEST",
		IssueCreator: creator,
	})
	chatAPI := web.NewAPI(handler, messenger)

	config := &Config{Host: "127.0.0.1", Port: 19097}
	authConfig := &AuthConfig{Type: AuthTypeAPIToken, Token: "chat-composed-secret"}
	server := NewServerWithAuth(config, authConfig)
	server.SetChatAPI(chatAPI)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	client := &http.Client{Timeout: 10 * time.Second}
	baseURL := "http://127.0.0.1:19097"

	post := func(t *testing.T, body string, auth bool) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/chat/messages", bytes.NewBufferString(body))
		if auth {
			req.Header.Set("Authorization", "Bearer chat-composed-secret")
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		return resp
	}

	getEvents := func(t *testing.T, conversationID string, after int64) chatEventsResponse {
		t.Helper()
		url := baseURL + "/api/v1/chat/conversations/" + conversationID + "/events"
		if after > 0 {
			url += "?after=" + strconv.FormatInt(after, 10)
		}
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer chat-composed-secret")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET events: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET events status = %d, want 200", resp.StatusCode)
		}
		var out chatEventsResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode events: %v", err)
		}
		return out
	}

	// 1. Auth rejected without a bearer token, on both endpoints.
	noAuthPost := post(t, `{"conversationId":"c1","text":"hi"}`, false)
	_ = noAuthPost.Body.Close()
	if noAuthPost.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST no-auth status = %d, want 401", noAuthPost.StatusCode)
	}
	noAuthGetReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/chat/conversations/c1/events", nil)
	noAuthGetResp, err := client.Do(noAuthGetReq)
	if err != nil {
		t.Fatalf("GET no-auth: %v", err)
	}
	_ = noAuthGetResp.Body.Close()
	if noAuthGetResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET no-auth status = %d, want 401", noAuthGetResp.StatusCode)
	}

	// 2. Operational-query text message -> 202, and the events poll shows a
	// text reply (handleOperational -> SendText via formatQueueSummary).
	opResp := post(t, `{"conversationId":"status-conv","text":"queue status"}`, true)
	defer func() { _ = opResp.Body.Close() }()
	if opResp.StatusCode != http.StatusAccepted {
		t.Fatalf("operational POST status = %d, want 202", opResp.StatusCode)
	}
	var opAccept chatMessageResponse
	if err := json.NewDecoder(opResp.Body).Decode(&opAccept); err != nil {
		t.Fatalf("decode operational accept body: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var opEvents chatEventsResponse
	for time.Now().Before(deadline) {
		opEvents = getEvents(t, "status-conv", opAccept.Seq)
		if len(opEvents.Events) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(opEvents.Events) == 0 {
		t.Fatal("no event landed for operational query")
	}
	if opEvents.Events[0].Type != web.EventText {
		t.Errorf("operational reply type = %q, want text", opEvents.Events[0].Type)
	}

	// pollEvents polls a conversation until done reports true for the events
	// seen after the given seq, or fails after the timeout.
	pollEvents := func(t *testing.T, conv string, after int64, done func([]web.Event) bool) []web.Event {
		t.Helper()
		var evs []web.Event
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			evs = getEvents(t, conv, after).Events
			if done(evs) {
				return evs
			}
		}
		t.Fatalf("conversation %s: expected events never landed; got %+v", conv, evs)
		return nil
	}

	// 3. Task-shaped message -> 202, and the chat turn answers with a text
	// event carrying the backend output (no confirmation card).
	taskConv := "task-conv"
	taskResp := post(t, `{"conversationId":"`+taskConv+`","text":"build the project"}`, true)
	defer func() { _ = taskResp.Body.Close() }()
	if taskResp.StatusCode != http.StatusAccepted {
		t.Fatalf("task POST status = %d, want 202", taskResp.StatusCode)
	}
	var taskAccept chatMessageResponse
	if err := json.NewDecoder(taskResp.Body).Decode(&taskAccept); err != nil {
		t.Fatalf("decode task accept body: %v", err)
	}
	textEvents := pollEvents(t, taskConv, taskAccept.Seq, func(evs []web.Event) bool { return len(evs) > 0 })
	if textEvents[0].Type != web.EventText || textEvents[0].Text != chatBackendOutput {
		t.Fatalf("first event = %+v, want text event %q", textEvents[0], chatBackendOutput)
	}

	// 4. An approval-shaped callback must be rejected with 400 — approvals do
	// not route through the chat API (scope fence, #4748 / GH-4411/GH-4431).
	approvalResp := post(t, `{"conversationId":"`+taskConv+`","isCallback":true,"callbackId":"cb1","actionId":"approve"}`, true)
	_ = approvalResp.Body.Close()
	if approvalResp.StatusCode != http.StatusBadRequest {
		t.Errorf("approval-shaped callback status = %d, want 400", approvalResp.StatusCode)
	}

	// 5. Pilot-issue flow: a chat turn reads a file (progress), drafts a
	// pilot-issue block (confirmation card), and the execute callback files it.
	issueConv := "task-conv"
	issueResp := post(t, `{"conversationId":"`+issueConv+`","text":"please file an issue"}`, true)
	defer func() { _ = issueResp.Body.Close() }()
	if issueResp.StatusCode != http.StatusAccepted {
		t.Fatalf("issue POST status = %d, want 202", issueResp.StatusCode)
	}
	var issueAccept chatMessageResponse
	if err := json.NewDecoder(issueResp.Body).Decode(&issueAccept); err != nil {
		t.Fatalf("decode issue accept body: %v", err)
	}
	var progressEv, cardEv *web.Event
	for _, ev := range pollEvents(t, issueConv, issueAccept.Seq, func(evs []web.Event) bool {
		for _, ev := range evs {
			if ev.Type == web.EventConfirmation {
				return true
			}
		}
		return false
	}) {
		e := ev
		switch {
		case e.Type == web.EventProgress && e.TaskID == "chat-task-conv" && progressEv == nil:
			progressEv = &e
		case e.Type == web.EventConfirmation:
			cardEv = &e
		}
	}
	if progressEv == nil {
		t.Fatal("no progress event with TaskID chat-task-conv")
	}
	if !strings.HasPrefix(cardEv.TaskID, "issue-") || cardEv.MessageRef != cardEv.TaskID {
		t.Fatalf("confirmation event = %+v, want issue- TaskID and MessageRef == TaskID", cardEv)
	}
	if progressEv.Seq >= cardEv.Seq {
		t.Errorf("progress seq %d not before confirmation seq %d", progressEv.Seq, cardEv.Seq)
	}

	execResp := post(t, `{"conversationId":"`+issueConv+`","isCallback":true,"callbackId":"execute-cb","actionId":"execute"}`, true)
	defer func() { _ = execResp.Body.Close() }()
	if execResp.StatusCode != http.StatusAccepted {
		t.Fatalf("execute callback status = %d, want 202", execResp.StatusCode)
	}
	const issueURL = "https://github.com/qf-studio/pilot/issues/1"
	pollEvents(t, issueConv, cardEv.Seq, func(evs []web.Event) bool {
		for _, ev := range evs {
			if ev.Type == web.EventText && strings.Contains(ev.Text, issueURL) {
				return true
			}
		}
		return false
	})
	if len(creator.calls) != 1 {
		t.Fatalf("CreateIssue calls = %d, want 1", len(creator.calls))
	}
	var hasPilot bool
	for _, l := range creator.calls[0].Labels {
		hasPilot = hasPilot || l == "pilot"
	}
	if !hasPilot {
		t.Errorf("CreateIssue labels = %v, want pilot", creator.calls[0].Labels)
	}
}
