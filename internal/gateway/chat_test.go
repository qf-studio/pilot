package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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
	eventsFunc   func(conversationID string, after int64) []web.Event
}

func (m *mockChatAPI) Dispatch(ctx context.Context, req web.DispatchRequest) (int64, error) {
	if m.dispatchFunc != nil {
		return m.dispatchFunc(ctx, req)
	}
	return 0, nil
}

func (m *mockChatAPI) Events(conversationID string, after int64) []web.Event {
	if m.eventsFunc != nil {
		return m.eventsFunc(conversationID, after)
	}
	return nil
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
		eventsFunc: func(conversationID string, after int64) []web.Event {
			gotID = conversationID
			gotAfter = after
			return want
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
// test drive a real *executor.Runner end to end (confirmation -> execution ->
// result) without invoking a real Claude Code subprocess, matching the
// pattern established by runner_integration_test.go's mockIntegrationBackend.
type chatTestBackend struct{}

func (b *chatTestBackend) Name() string      { return "chat-test-backend" }
func (b *chatTestBackend) IsAvailable() bool { return true }
func (b *chatTestBackend) Execute(ctx context.Context, opts executor.ExecuteOptions) (*executor.BackendResult, error) {
	return &executor.BackendResult{Success: true, Output: "chat test task done"}, nil
}

// TestChatAPI_Composed exercises the full GH-4835 acceptance flow on a real
// HTTP listener: auth enforcement, an operational-query text reply, and a
// full task confirmation -> execute -> result flow (with a fake executor
// backend), plus the approvals scope fence (#4748 / GH-4411 / GH-4431).
func TestChatAPI_Composed(t *testing.T) {
	backend := &chatTestBackend{}
	runner := executor.NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)
	runner.SetSkipPreflightChecks(true)

	messenger := web.NewMessenger()
	handler := comms.BuildHandler(comms.HandlerDeps{
		Messenger:    messenger,
		Runner:       runner,
		ProjectPath:  t.TempDir(),
		TaskIDPrefix: "WEBTEST",
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

	// 3. Task-shaped message -> 202, events poll shows a confirmation event.
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

	deadline = time.Now().Add(5 * time.Second)
	var confirmEvents chatEventsResponse
	for time.Now().Before(deadline) {
		confirmEvents = getEvents(t, taskConv, taskAccept.Seq)
		if len(confirmEvents.Events) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(confirmEvents.Events) == 0 {
		t.Fatal("no confirmation event landed")
	}
	confirmEv := confirmEvents.Events[0]
	if confirmEv.Type != web.EventConfirmation {
		t.Fatalf("event type = %q, want confirmation", confirmEv.Type)
	}
	if confirmEv.TaskID == "" || confirmEv.MessageRef != confirmEv.TaskID {
		t.Fatalf("confirmation event = %+v, want TaskID set and MessageRef == TaskID", confirmEv)
	}
	lastSeq := confirmEv.Seq

	// 4. An approval-shaped callback must be rejected with 400 — approvals do
	// not route through the chat API (scope fence, #4748 / GH-4411/GH-4431).
	approvalResp := post(t, `{"conversationId":"`+taskConv+`","isCallback":true,"callbackId":"cb1","actionId":"approve"}`, true)
	_ = approvalResp.Body.Close()
	if approvalResp.StatusCode != http.StatusBadRequest {
		t.Errorf("approval-shaped callback status = %d, want 400", approvalResp.StatusCode)
	}

	// 5. Execute callback -> 202, then poll until a result event lands;
	// assert at least one progress event shares the confirmation's messageRef,
	// and the result carries Success=true with the same messageRef.
	execResp := post(t, `{"conversationId":"`+taskConv+`","isCallback":true,"callbackId":"execute-cb","actionId":"execute"}`, true)
	defer func() { _ = execResp.Body.Close() }()
	if execResp.StatusCode != http.StatusAccepted {
		t.Fatalf("execute callback status = %d, want 202", execResp.StatusCode)
	}

	deadline = time.Now().Add(15 * time.Second)
	var sawProgress bool
	var resultEv *web.Event
	for time.Now().Before(deadline) {
		more := getEvents(t, taskConv, lastSeq)
		for i := range more.Events {
			ev := more.Events[i]
			if ev.Type == web.EventProgress && ev.MessageRef == confirmEv.MessageRef {
				sawProgress = true
			}
			if ev.Type == web.EventResult {
				e := ev
				resultEv = &e
			}
			if ev.Seq > lastSeq {
				lastSeq = ev.Seq
			}
		}
		if resultEv != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if resultEv == nil {
		t.Fatal("no result event landed after execute callback")
	}
	if !sawProgress {
		t.Error("no progress event with the confirmation's messageRef was observed")
	}
	if resultEv.Success == nil || !*resultEv.Success {
		t.Errorf("result event Success = %v, want true", resultEv.Success)
	}
	if resultEv.MessageRef != confirmEv.MessageRef {
		t.Errorf("result MessageRef = %q, want %q", resultEv.MessageRef, confirmEv.MessageRef)
	}
}
