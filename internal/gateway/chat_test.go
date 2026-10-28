package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
	projects     []web.ProjectSummary
}

func (m *mockChatAPI) Projects() []web.ProjectSummary { return m.projects }

func (m *mockChatAPI) ActiveProjectID(string) string { return "" }

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
//
// It also tracks its own lifetime (GH-5923): the chat API runs each task on a
// goroutine nobody joins, so the test needs a way to know the Runner is done
// writing into the project directory before t.TempDir's cleanup removes it.
type chatTestBackend struct {
	// hold keeps Execute running this long, ignoring ctx, so a test can prove
	// that cleanup really waits for an in-flight task.
	hold time.Duration
	// entered, when non-nil, receives one value as each Execute call starts.
	entered chan struct{}

	mu       sync.Mutex
	started  int
	finished int
	taskIDs  []string
}

const chatBackendOutput = "chat test task done"

const chatIssueOutput = "Here is the draft.\n\n```pilot-issue\n" +
	"title: \"feat(chat): composed test issue\"\nlabels: [pilot]\nbody: |\n" +
	"  ## Context\n  Composed test.\n  ## Scope\n  One file.\n  ## Acceptance Criteria\n  Passes.\n" +
	"  ## Out of scope\n  Everything else.\n  ## Refs\n  None.\n```"

func (b *chatTestBackend) Name() string      { return "chat-test-backend" }
func (b *chatTestBackend) IsAvailable() bool { return true }
func (b *chatTestBackend) Execute(ctx context.Context, opts executor.ExecuteOptions) (*executor.BackendResult, error) {
	b.mu.Lock()
	b.started++
	b.taskIDs = append(b.taskIDs, opts.TaskID)
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.finished++
		b.mu.Unlock()
	}()
	select {
	case b.entered <- struct{}{}:
	default:
	}
	if b.hold > 0 {
		time.Sleep(b.hold)
	}
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

// idle reports whether every Execute call has returned and the Runner has
// left executeWithOptions for each task it ran (its bookkeeping and Navigator
// copy-back happen after the backend returns).
func (b *chatTestBackend) idle(runner *executor.Runner) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finished != b.started {
		return false
	}
	for _, id := range b.taskIDs {
		if runner.IsRunning(id) {
			return false
		}
	}
	return true
}

// waitIdle blocks until idle reports true or the timeout expires.
func (b *chatTestBackend) waitIdle(runner *executor.Runner, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for !b.idle(runner) {
		if time.Now().After(deadline) {
			b.mu.Lock()
			defer b.mu.Unlock()
			return fmt.Errorf("backend not idle after %s: %d started, %d finished", timeout, b.started, b.finished)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
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

// chatProjectSource is a one-project comms.ProjectSource for the composed test.
type chatProjectSource struct{ p *comms.ProjectInfo }

func (c chatProjectSource) GetProjectByName(n string) *comms.ProjectInfo {
	return map[string]*comms.ProjectInfo{c.p.Name: c.p}[n]
}

func (c chatProjectSource) GetProjectByPath(string) *comms.ProjectInfo { return c.p }

func (c chatProjectSource) GetDefaultProject() *comms.ProjectInfo { return c.p }
func (c chatProjectSource) ListProjects() []*comms.ProjectInfo    { return []*comms.ProjectInfo{c.p} }

// freeLoopbackPort returns a port that was free on 127.0.0.1 a moment ago.
func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// startTestServer runs server.Start in the background and blocks until /ping
// answers, so a request can't race the listener coming up. A cleanup cancels
// the server context and waits for Start to return, instead of leaving the
// goroutine to the end of the process.
func startTestServer(t *testing.T, server *Server, port int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = server.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			t.Error("gateway server did not stop within 10s")
		}
	})

	pingURL := "http://127.0.0.1:" + strconv.Itoa(port) + "/ping"
	client := &http.Client{Timeout: time.Second}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		resp, err := client.Get(pingURL)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return
		}
	}
	t.Fatalf("gateway server not ready at %s within 5s", pingURL)
}

// chatHarness is a gateway server wired to a real Runner over a
// chatTestBackend and a project directory under t.TempDir().
type chatHarness struct {
	baseURL    string
	token      string
	backend    *chatTestBackend
	runner     *executor.Runner
	creator    *chatIssueCreator
	projectDir string
}

// newChatHarness builds the harness. The project directory is created first
// and the cleanups are registered after it, so they run before TempDir's
// RemoveAll (cleanups run last-in-first-out): the server stops, then the
// background task finishes writing, then the directory is removed (GH-5923).
func newChatHarness(t *testing.T, backend *chatTestBackend) *chatHarness {
	t.Helper()
	projectDir := t.TempDir()

	runner := executor.NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)
	runner.SetSkipPreflightChecks(true)

	creator := &chatIssueCreator{}
	messenger := web.NewMessenger()
	handler := comms.BuildHandler(comms.HandlerDeps{
		Messenger:    messenger,
		Runner:       runner,
		ProjectPath:  projectDir,
		Projects:     chatProjectSource{p: &comms.ProjectInfo{Name: "alpha", Path: projectDir}},
		TaskIDPrefix: "WEBTEST",
		IssueCreator: creator,
	})

	const token = "chat-composed-secret"
	port := freeLoopbackPort(t)
	server := NewServerWithAuth(&Config{Host: "127.0.0.1", Port: port}, &AuthConfig{Type: AuthTypeAPIToken, Token: token})
	server.SetChatAPI(web.NewAPI(handler, messenger))

	t.Cleanup(func() {
		if err := backend.waitIdle(runner, 15*time.Second); err != nil {
			t.Errorf("background task outlived the test: %v", err)
		}
	})
	startTestServer(t, server, port)

	return &chatHarness{
		baseURL:    "http://127.0.0.1:" + strconv.Itoa(port),
		token:      token,
		backend:    backend,
		runner:     runner,
		creator:    creator,
		projectDir: projectDir,
	}
}

// TestChatProjectsRoute covers GET /api/v1/chat/projects: bearer auth, the
// {"projects":[...]} body and method gating.
func TestChatProjectsRoute(t *testing.T) {
	api := &mockChatAPI{projects: []web.ProjectSummary{
		{ID: "alpha", Name: "alpha", Path: "/work/alpha", Repo: "acme/alpha", Default: true},
	}}
	port := freeLoopbackPort(t)
	server := NewServerWithAuth(&Config{Host: "127.0.0.1", Port: port}, &AuthConfig{Type: AuthTypeAPIToken, Token: "chat-projects-secret"})
	server.SetChatAPI(api)
	startTestServer(t, server, port)

	client := &http.Client{Timeout: 5 * time.Second}
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/api/v1/chat/projects"
	do := func(method string, auth bool) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, url, nil)
		if auth {
			req.Header.Set("Authorization", "Bearer chat-projects-secret")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return resp
	}

	if noAuth := do(http.MethodGet, false); noAuth.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-bearer status = %d, want 401", noAuth.StatusCode)
	}

	ok := do(http.MethodGet, true)
	defer func() { _ = ok.Body.Close() }()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", ok.StatusCode)
	}
	var body struct {
		Projects []web.ProjectSummary `json:"projects"`
	}
	if err := json.NewDecoder(ok.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Projects) != 1 || body.Projects[0] != api.projects[0] {
		t.Errorf("projects = %+v, want %+v", body.Projects, api.projects)
	}

	if post := do(http.MethodPost, true); post.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", post.StatusCode)
	}
}

// TestChatAPI_Composed exercises the full GH-4835 acceptance flow on a real
// HTTP listener: auth enforcement, an operational-query text reply, and a
// chat-turn text reply, and the pilot-issue flow (progress -> confirmation ->
// execute -> filed URL) with a fake executor backend and issue creator, plus
// the approvals scope fence (#4748 / GH-4411 / GH-4431).
func TestChatAPI_Composed(t *testing.T) {
	h := newChatHarness(t, &chatTestBackend{})
	creator := h.creator
	client := &http.Client{Timeout: 10 * time.Second}
	baseURL := h.baseURL

	post := func(t *testing.T, body string, auth bool) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/chat/messages", bytes.NewBufferString(body))
		if auth {
			req.Header.Set("Authorization", "Bearer "+h.token)
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
		req.Header.Set("Authorization", "Bearer "+h.token)
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

	// 3b. Project selection on the wire: an unknown projectId is a 400 whose
	// body starts with "unknown project:"; a known one is accepted and the
	// events response reports it for the conversation.
	badProj := post(t, `{"conversationId":"proj-conv","text":"hi","projectId":"nope"}`, true)
	badBody, _ := io.ReadAll(badProj.Body)
	_ = badProj.Body.Close()
	if badProj.StatusCode != http.StatusBadRequest || !strings.HasPrefix(string(badBody), "unknown project:") {
		t.Errorf("unknown projectId: status = %d, body = %q; want 400 \"unknown project: ...\"", badProj.StatusCode, badBody)
	}
	goodProj := post(t, `{"conversationId":"proj-conv","text":"build the project","projectId":"alpha"}`, true)
	_ = goodProj.Body.Close()
	if goodProj.StatusCode != http.StatusAccepted {
		t.Fatalf("known projectId status = %d, want 202", goodProj.StatusCode)
	}
	if got := getEvents(t, "proj-conv", 0).ProjectID; got != "alpha" {
		t.Errorf("events projectId = %q, want alpha", got)
	}
	// Wait for that turn's reply so its task is finished, not still running
	// against the project directory when the test returns (GH-5923).
	pollEvents(t, "proj-conv", 0, func(evs []web.Event) bool {
		for _, ev := range evs {
			if ev.Type == web.EventText && ev.Text == chatBackendOutput {
				return true
			}
		}
		return false
	})

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

// TestChatAPI_Composed_RunnerDoneBeforeCleanup pins GH-5923: a task the chat
// API started in the background must be finished, with the Runner out of it,
// by the time the test's cleanups have run, i.e. before t.TempDir's RemoveAll
// can race its writes. The subtest returns while the backend is still inside
// Execute (it ignores ctx and holds for hold), so only the harness cleanup
// waiting for the backend can make the check after t.Run pass.
func TestChatAPI_Composed_RunnerDoneBeforeCleanup(t *testing.T) {
	backend := &chatTestBackend{hold: 300 * time.Millisecond, entered: make(chan struct{}, 4)}
	var runner *executor.Runner

	t.Run("task in flight when the subtest returns", func(t *testing.T) {
		h := newChatHarness(t, backend)
		runner = h.runner

		req, err := http.NewRequest(http.MethodPost, h.baseURL+"/api/v1/chat/messages",
			bytes.NewBufferString(`{"conversationId":"slow-conv","text":"build the project"}`))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("POST status = %d, want 202", resp.StatusCode)
		}

		select {
		case <-backend.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("backend Execute never started")
		}
		// Return with the task still running: the harness cleanup must wait.
	})

	if !backend.idle(runner) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		t.Fatalf("task still running after the subtest's cleanups ran: %d started, %d finished",
			backend.started, backend.finished)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.started != 1 {
		t.Errorf("backend Execute calls = %d, want 1", backend.started)
	}
}
