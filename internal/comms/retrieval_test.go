package comms

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/intent"
)

// --- retrieve() tests ---

func TestRetrieve_Disabled(t *testing.T) {
	_, tooBroad := retrieve("/some/path", "how does intent work?", RetrievalConfig{Enabled: false})
	if !tooBroad {
		t.Error("disabled retrieval must return tooBroad=true")
	}
}

func TestRetrieve_EmptyProjectPath(t *testing.T) {
	_, tooBroad := retrieve("", "how does intent work?", RetrievalConfig{Enabled: true})
	if !tooBroad {
		t.Error("empty projectPath must return tooBroad=true")
	}
}

func TestRetrieve_BroadQuestion(t *testing.T) {
	dir := t.TempDir()
	_, tooBroad := retrieve(dir, "explain the whole repo", RetrievalConfig{Enabled: true})
	if !tooBroad {
		t.Error("broad question must return tooBroad=true")
	}
}

func TestRetrieve_NoKeywords(t *testing.T) {
	dir := t.TempDir()
	// All words are stop words or too short.
	_, tooBroad := retrieve(dir, "how is it?", RetrievalConfig{Enabled: true})
	if !tooBroad {
		t.Error("question with no useful keywords must return tooBroad=true")
	}
}

func TestRetrieve_NoMatchingFiles(t *testing.T) {
	dir := t.TempDir()
	// Create a file that won't match the keyword.
	if err := os.WriteFile(filepath.Join(dir, "handler.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, tooBroad := retrieve(dir, "how does retrieval work?", RetrievalConfig{Enabled: true})
	if !tooBroad {
		t.Error("no matching files must return tooBroad=true")
	}
}

func TestRetrieve_MatchingFile(t *testing.T) {
	dir := t.TempDir()
	content := "package comms\n\nfunc Retrieve() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "retrieval.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	block, tooBroad := retrieve(dir, "how does retrieval work?", RetrievalConfig{
		Enabled:  true,
		MaxFiles: 8,
		MaxBytes: 24000,
	})

	if tooBroad {
		t.Error("expected tooBroad=false for a specific keyword match")
	}
	if !strings.Contains(block, "retrieval.go") {
		t.Errorf("context block should cite retrieval.go; got:\n%s", block)
	}
	if !strings.Contains(block, "package comms") {
		t.Errorf("context block should contain file content; got:\n%s", block)
	}
}

func TestRetrieve_MaxFilesCap(t *testing.T) {
	dir := t.TempDir()
	// Create 10 files all matching the keyword "intent".
	for i := 0; i < 10; i++ {
		name := strings.Repeat("intent", 1) + strings.Repeat("a", i) + ".go"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("// intent file"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	block, tooBroad := retrieve(dir, "how does intent work?", RetrievalConfig{
		Enabled:  true,
		MaxFiles: 3,
		MaxBytes: 24000,
	})

	// 10 files match but threshold is MaxFiles*4=12 so NOT too broad.
	if tooBroad {
		t.Errorf("10 matches with threshold 12 should not be too broad; block=%q", block)
	}
	// Should include at most MaxFiles=3 files.
	count := strings.Count(block, "// File:")
	if count > 3 {
		t.Errorf("expected at most 3 files in context block, got %d", count)
	}
}

func TestRetrieve_TooManyMatches(t *testing.T) {
	dir := t.TempDir()
	// Create 33 matching files (> MaxFiles*4=8*4=32).
	for i := 0; i < 33; i++ {
		name := strings.Repeat("intent", 1) + strings.Repeat("x", i) + ".go"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("content"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, tooBroad := retrieve(dir, "how does intent work?", RetrievalConfig{
		Enabled:  true,
		MaxFiles: 8,
		MaxBytes: 24000,
	})

	if !tooBroad {
		t.Error("33 matches with threshold 32 must return tooBroad=true")
	}
}

func TestRetrieve_MaxBytes(t *testing.T) {
	dir := t.TempDir()
	// Create a file with content larger than maxBytes.
	bigContent := strings.Repeat("x", 500)
	if err := os.WriteFile(filepath.Join(dir, "retrieval.go"), []byte(bigContent), 0o644); err != nil {
		t.Fatal(err)
	}

	block, tooBroad := retrieve(dir, "how does retrieval work?", RetrievalConfig{
		Enabled:  true,
		MaxFiles: 8,
		MaxBytes: 100, // cap at 100 bytes
	})

	if tooBroad {
		t.Error("expected tooBroad=false")
	}
	// The file header "// File: retrieval.go\n" is ~22 bytes, so content portion ≤ 78 bytes.
	if len(block) > 200 {
		t.Errorf("block exceeds expected max size; len=%d", len(block))
	}
}

func TestRetrieve_SkipsHiddenDirs(t *testing.T) {
	dir := t.TempDir()
	hidden := filepath.Join(dir, ".git")
	if err := os.Mkdir(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "retrieval.go"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, tooBroad := retrieve(dir, "how does retrieval work?", RetrievalConfig{
		Enabled:  true,
		MaxFiles: 8,
		MaxBytes: 24000,
	})
	// No visible files match → tooBroad (no candidates).
	if !tooBroad {
		t.Error("should return tooBroad=true when only hidden-dir files match")
	}
}

// --- isBroadQuestion tests ---

func TestIsBroadQuestion(t *testing.T) {
	cases := []struct {
		q    string
		want bool
	}{
		{"explain the whole repo", true},
		{"describe everything", true},
		{"entire codebase overview", true},
		{"how does intent classification work?", false},
		{"what does the retrieval function do?", false},
		{"explain the whole flow in detail", true},
	}
	for _, tc := range cases {
		if got := isBroadQuestion(tc.q); got != tc.want {
			t.Errorf("isBroadQuestion(%q) = %v, want %v", tc.q, got, tc.want)
		}
	}
}

// --- extractKeywords tests ---

func TestExtractKeywords(t *testing.T) {
	kw := extractKeywords("how does intent classification work?")
	found := map[string]bool{}
	for _, k := range kw {
		found[k] = true
	}
	if !found["intent"] {
		t.Errorf("expected 'intent' in keywords; got %v", kw)
	}
	if !found["classification"] {
		t.Errorf("expected 'classification' in keywords; got %v", kw)
	}
	// Stop words should be filtered.
	if found["how"] || found["does"] {
		t.Errorf("stop words should not appear in keywords; got %v", kw)
	}
}

// --- Responder.Answer tests ---

func TestResponderAnswer_TooBroad_Disabled(t *testing.T) {
	r, _ := newMockResponder("some reply", "")
	// retrieval is zero-valued (Enabled=false) → tooBroad=true
	answer, tooBroad, err := r.Answer(context.Background(), nil, "how does intent work?", "/tmp/proj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tooBroad {
		t.Error("expected tooBroad=true when retrieval is disabled")
	}
	if answer != "" {
		t.Errorf("expected empty answer on tooBroad, got %q", answer)
	}
}

func TestResponderAnswer_TooBroad_BroadQuestion(t *testing.T) {
	dir := t.TempDir()
	r := &Responder{
		client:      &mockAnswerer{reply: "LLM answer"},
		answerModel: "claude-haiku-4-5-20251001",
		retrieval:   RetrievalConfig{Enabled: true, MaxFiles: 8, MaxBytes: 24000},
	}
	answer, tooBroad, err := r.Answer(context.Background(), nil, "explain the whole repo", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tooBroad {
		t.Error("expected tooBroad=true for broad question")
	}
	if answer != "" {
		t.Errorf("expected empty answer on tooBroad, got %q", answer)
	}
}

func TestResponderAnswer_Success(t *testing.T) {
	dir := t.TempDir()
	// Create a matching file so retrieval succeeds.
	if err := os.WriteFile(filepath.Join(dir, "intent.go"), []byte("package comms\n// intent logic"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockAnswerer{reply: "Intent classification routes messages."}
	r := &Responder{
		client:      mock,
		answerModel: "claude-sonnet-4-6",
		retrieval:   RetrievalConfig{Enabled: true, MaxFiles: 8, MaxBytes: 24000},
	}

	answer, tooBroad, err := r.Answer(context.Background(), nil, "how does intent work?", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tooBroad {
		t.Error("expected tooBroad=false for a specific match")
	}
	if answer != "Intent classification routes messages." {
		t.Errorf("unexpected answer: %q", answer)
	}
	// Verify the system prompt includes the context block.
	if len(mock.calls) == 0 {
		t.Fatal("expected at least one LLM call")
	}
	if !strings.Contains(mock.calls[0].system, "codebase_context") {
		t.Errorf("system prompt should include codebase_context; got %q", mock.calls[0].system)
	}
	if !strings.Contains(mock.calls[0].system, "intent.go") {
		t.Errorf("system prompt should cite intent.go; got %q", mock.calls[0].system)
	}
}

func TestResponderAnswer_LLMError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "intent.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockAnswerer{err: errTest}
	r := &Responder{
		client:      mock,
		answerModel: "claude-sonnet-4-6",
		retrieval:   RetrievalConfig{Enabled: true, MaxFiles: 8, MaxBytes: 24000},
	}

	_, tooBroad, err := r.Answer(context.Background(), nil, "how does intent work?", dir)
	if err == nil {
		t.Error("expected error from LLM to propagate")
	}
	if tooBroad {
		t.Error("LLM error should not be reported as tooBroad")
	}
}

func TestResponderAnswer_WithHistory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "intent.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockAnswerer{reply: "Answer with context."}
	r := &Responder{
		client:      mock,
		answerModel: "claude-sonnet-4-6",
		retrieval:   RetrievalConfig{Enabled: true, MaxFiles: 8, MaxBytes: 24000},
	}

	history := []intent.ConversationMessage{
		{Role: "user", Content: "earlier question"},
		{Role: "assistant", Content: "earlier answer"},
	}

	_, _, err := r.Answer(context.Background(), history, "how does intent work?", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.calls) == 0 {
		t.Fatal("expected LLM call")
	}
	if len(mock.calls[0].history) != 2 {
		t.Errorf("expected 2 history entries forwarded; got %d", len(mock.calls[0].history))
	}
}

// --- handleQuestion seam tests ---

func TestHandleQuestion_ResponderPath_SkipsRunner(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "intent.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &handlerMock{}
	mock := &mockAnswerer{reply: "Intent routes messages via detectIntent."}
	r := &Responder{
		client:      mock,
		answerModel: "claude-sonnet-4-6",
		retrieval:   RetrievalConfig{Enabled: true, MaxFiles: 8, MaxBytes: 24000},
	}
	h := NewHandler(&HandlerConfig{
		Messenger:   m,
		Responder:   r,
		ProjectPath: dir,
		TaskIDPrefix: "TEST",
	})

	h.handleQuestion(context.Background(), "ch1", "", "how does intent work?")

	// Should have sent the preamble + a chunked answer.
	texts := m.getTexts()
	foundPreamble := false
	for _, st := range texts {
		if strings.Contains(st.text, "Looking into") {
			foundPreamble = true
		}
	}
	if !foundPreamble {
		t.Error("expected '🔍 Looking into that...' preamble")
	}
	if len(m.chunks) == 0 {
		t.Error("expected a chunked answer via responder path")
	}
	if m.chunks[0].content != "Intent routes messages via detectIntent." {
		t.Errorf("unexpected chunk content: %q", m.chunks[0].content)
	}
	// No runner involved → no executor panics (runner is nil on this handler).
}

func TestHandleQuestion_TooBroad_FallsBackToRunner(t *testing.T) {
	m := &handlerMock{}
	mock := &mockAnswerer{reply: "unreachable"} // won't be called
	r := &Responder{
		client:      mock,
		answerModel: "claude-sonnet-4-6",
		// retrieval disabled → always tooBroad
		retrieval: RetrievalConfig{Enabled: false},
	}
	h := NewHandler(&HandlerConfig{
		Messenger:    m,
		Responder:    r,
		TaskIDPrefix: "TEST",
		// runner is nil → falls through to "I couldn't answer" message
	})

	h.handleQuestion(context.Background(), "ch1", "", "explain the whole repo")

	texts := m.getTexts()
	foundFallback := false
	for _, st := range texts {
		if strings.Contains(st.text, "couldn't answer") {
			foundFallback = true
		}
	}
	if !foundFallback {
		t.Errorf("expected fallback message when runner is nil; texts: %v", texts)
	}
}

func TestHandleQuestion_NilResponder_ExecutorPath(t *testing.T) {
	m := &handlerMock{}
	h := newTestHandler(m)
	// No responder, no runner — executor path with nil runner → recoverable panic.
	func() {
		defer func() { recover() }() //nolint:errcheck
		h.handleQuestion(context.Background(), "ch1", "", "how does intent work?")
	}()

	texts := m.getTexts()
	foundPreamble := false
	for _, st := range texts {
		if strings.Contains(st.text, "Looking into") {
			foundPreamble = true
		}
	}
	if !foundPreamble {
		t.Error("expected preamble even with nil responder")
	}
}

// errTest is a sentinel error for tests.
var errTest = fmt.Errorf("mock LLM error")
