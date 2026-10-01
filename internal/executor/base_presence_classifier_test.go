package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/typesafe"
)

// recordingAsker records every Ask call and returns scripted answers. Unlike
// fakeAsker it can return answers together with an error, so a classifier that
// ignores the error would visibly act on them.
type recordingAsker struct {
	answers map[string]typesafe.Answer
	err     error

	calls     int
	gotState  any
	gotQuests map[string]typesafe.Question
}

func (f *recordingAsker) Ask(_ context.Context, state any, questions map[string]typesafe.Question) (typesafe.Answers, error) {
	f.calls++
	f.gotState = state
	f.gotQuests = questions
	return typesafe.Answers{Answers: f.answers}, f.err
}

const basePresenceBody = "Edit `internal/foo/real.go` to use the new helper.\n" +
	"The task adds `internal/foo/new_file.go` with the helper. Compare `origin/main..HEAD` for the diff.\n" +
	"Load `internal/foo/secret.go` with api_key=fake-secret-value-123 now."

func newTestBasePresenceClassifier(a typesafe.Asker, shadow bool) *jevBasePresenceClassifier {
	return &jevBasePresenceClassifier{asker: a, minConfidence: 0.8, shadow: shadow}
}

func TestBasePresenceClassifier_OneQuestionPerSpanNamesIndexAndSpan(t *testing.T) {
	asker := &recordingAsker{}
	c := newTestBasePresenceClassifier(asker, true)
	paths := []string{"internal/foo/real.go", "internal/foo/new_file.go"}
	c.Classify(context.Background(), basePresenceBody, paths)

	if asker.calls != 1 {
		t.Fatalf("Ask calls = %d, want 1", asker.calls)
	}
	if len(asker.gotQuests) != 2 {
		t.Fatalf("questions = %d, want 2", len(asker.gotQuests))
	}
	insts := map[string]string{}
	for i, p := range paths {
		idx := fmt.Sprintf("%d", i+1)
		q, ok := asker.gotQuests["span_"+idx]
		if !ok {
			t.Fatalf("missing question span_%s", idx)
		}
		in, _ := q.Instructions.(string)
		insts[idx] = in
		if !strings.Contains(in, fmt.Sprintf("span %s", idx)) || !strings.Contains(in, p) {
			t.Errorf("instruction %d must name index and quote span %q: %s", i+1, p, in)
		}
		crit, _ := q.Criteria.(map[string]any)
		for _, opt := range []string{spanExistingPrerequisite, spanToBeCreated, spanNotARepoFile} {
			if _, ok := crit[opt]; !ok {
				t.Errorf("question %d missing option %s", i+1, opt)
			}
		}
	}
	// Mutation pin: a shared instruction constant makes every question
	// byte-identical, and Jev answers them all the same way.
	if insts["1"] == insts["2"] {
		t.Fatalf("instructions for different spans are identical: %s", insts["1"])
	}
	if !strings.Contains(insts["2"], "The task adds") {
		t.Errorf("instruction 2 must carry its context sentence: %s", insts["2"])
	}
	if strings.Contains(insts["2"], "Edit `internal/foo/real.go`") {
		t.Errorf("instruction 2 leaked another sentence: %s", insts["2"])
	}
}

func TestBasePresenceClassifier_OnlySpanAndOneSentenceLeave(t *testing.T) {
	asker := &recordingAsker{}
	c := newTestBasePresenceClassifier(asker, true)
	c.Classify(context.Background(), basePresenceBody, []string{"internal/foo/secret.go"})

	var sent strings.Builder
	sent.WriteString(fmt.Sprintf("%v", asker.gotState))
	for _, q := range asker.gotQuests {
		sent.WriteString(q.Instructions.(string))
	}
	out := sent.String()
	if strings.Contains(out, "fake-secret-value-123") {
		t.Errorf("secret leaked: %s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Errorf("expected redaction marker in sent text: %s", out)
	}
	for _, other := range []string{"real.go", "new_file.go", "origin/main"} {
		if strings.Contains(out, other) {
			t.Errorf("body text %q outside the span's sentence leaked: %s", other, out)
		}
	}
}

func TestBasePresenceClassifier_ContextSentenceCapped(t *testing.T) {
	long := strings.Repeat("word ", 400)
	body := long + "`pkg/x/y.go` " + long
	got := spanContextSentence(body, "pkg/x/y.go", maxBasePresenceContextChars)
	if n := len([]rune(got)); n > maxBasePresenceContextChars {
		t.Errorf("context length %d > cap %d", n, maxBasePresenceContextChars)
	}
	if !strings.Contains(got, "pkg/x/y.go") {
		t.Errorf("window lost the span: %q", got)
	}
}

func TestBasePresenceClassifier_ShadowKeepsAllAndCounts(t *testing.T) {
	asker := &recordingAsker{answers: map[string]typesafe.Answer{
		"span_1": choice(spanExistingPrerequisite, 0.95),
		"span_2": choice(spanToBeCreated, 0.95),
		"span_3": choice(spanNotARepoFile, 0.5),
		"span_4": choice("garbage", 0.99),
	}}
	paths := []string{"a/one.go", "a/two.go", "a/three.go", "a/four.go"}
	kept, stats := newTestBasePresenceClassifier(asker, true).Classify(context.Background(), "`a/one.go` `a/two.go`", paths)
	if !reflect.DeepEqual(kept, paths) {
		t.Errorf("shadow kept = %v, want all %v", kept, paths)
	}
	want := BasePresenceClassifyStats{Spans: 4, Shadow: true, Agreed: 1, WouldSkip: 1, LowConfidence: 1, Errors: 1}
	stats.Latency = 0
	if len(stats.Details) != len(paths) {
		t.Errorf("details = %d, want %d", len(stats.Details), len(paths))
	}
	stats.Details = nil
	if !reflect.DeepEqual(stats, want) {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
}

func TestBasePresenceClassifier_LiveRemovesConfidentNonPrerequisites(t *testing.T) {
	tests := []struct {
		name    string
		answer  *typesafe.Answer
		wantKep bool
	}{
		{"to_be_created at min", ptrAnswer(choice(spanToBeCreated, 0.8)), false},
		{"not_a_repo_file above min", ptrAnswer(choice(spanNotARepoFile, 0.95)), false},
		// Mutation pin: deleting the confidence comparison keeps these from
		// being kept.
		{"to_be_created below min", ptrAnswer(choice(spanToBeCreated, 0.79)), true},
		{"not_a_repo_file low", ptrAnswer(choice(spanNotARepoFile, 0.1)), true},
		{"existing_prerequisite", ptrAnswer(choice(spanExistingPrerequisite, 0.99)), true},
		{"invalid choice", ptrAnswer(choice("maybe", 0.99)), true},
		{"missing answer", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			answers := map[string]typesafe.Answer{}
			if tt.answer != nil {
				answers["span_1"] = *tt.answer
			}
			kept, _ := newTestBasePresenceClassifier(&recordingAsker{answers: answers}, false).
				Classify(context.Background(), "`a/b.go`", []string{"a/b.go"})
			if got := len(kept) == 1; got != tt.wantKep {
				t.Errorf("kept=%v, want kept=%v", kept, tt.wantKep)
			}
		})
	}
}

func TestBasePresenceClassifier_AskErrorKeepsAllSpans(t *testing.T) {
	// The fake returns skip-worthy answers alongside the error: only the
	// error fallback keeps the spans (mutation pin).
	asker := &recordingAsker{
		err: errors.New("boom"),
		answers: map[string]typesafe.Answer{
			"span_1": choice(spanNotARepoFile, 0.99),
			"span_2": choice(spanToBeCreated, 0.99),
		},
	}
	var buf bytes.Buffer
	c := newTestBasePresenceClassifier(asker, false)
	c.log = slog.New(slog.NewTextHandler(&buf, nil))
	paths := []string{"a/one.go", "a/two.go"}
	kept, stats := c.Classify(context.Background(), "`a/one.go` `a/two.go`", paths)
	if !reflect.DeepEqual(kept, paths) {
		t.Errorf("kept = %v, want all on Ask error", kept)
	}
	if stats.Errors != 1 || stats.WouldSkip != 0 {
		t.Errorf("stats = %+v, want Errors=1 WouldSkip=0", stats)
	}
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("expected a warn on Ask failure: %s", buf.String())
	}
}

func TestBasePresenceClassifier_NoSpansNoAsk(t *testing.T) {
	asker := &recordingAsker{}
	kept, stats := newTestBasePresenceClassifier(asker, false).Classify(context.Background(), "body", nil)
	if asker.calls != 0 || len(kept) != 0 || stats.Spans != 0 {
		t.Errorf("calls=%d kept=%v stats=%+v, want no ask", asker.calls, kept, stats)
	}
}

func TestClassifyBasePresencePaths_LogsOneInfoLine(t *testing.T) {
	var buf bytes.Buffer
	asker := &recordingAsker{answers: map[string]typesafe.Answer{"span_1": choice(spanToBeCreated, 0.95)}}
	r := &Runner{
		log:                    slog.New(slog.NewJSONHandler(&buf, nil)),
		basePresenceClassifier: newTestBasePresenceClassifier(asker, true),
	}
	paths := []string{"a/b.go"}
	got := r.classifyBasePresencePaths(context.Background(), "GH-9", "`a/b.go`", paths)
	if !reflect.DeepEqual(got, paths) {
		t.Errorf("shadow must not change paths: %v", got)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("log lines = %d, want 1: %s", len(lines), buf.String())
	}
	for _, key := range []string{`"level":"INFO"`, `"task_id":"GH-9"`, `"spans":1`, `"agreed":0`, `"would_skip":1`, `"low_confidence":0`, `"errors":0`, `"latency_ms":`} {
		if !strings.Contains(lines[0], key) {
			t.Errorf("log line missing %s: %s", key, lines[0])
		}
	}
}

func TestClassifyBasePresencePaths_NilClassifierUntouched(t *testing.T) {
	var buf bytes.Buffer
	r := &Runner{log: slog.New(slog.NewJSONHandler(&buf, nil))}
	paths := []string{"a/b.go"}
	if got := r.classifyBasePresencePaths(context.Background(), "GH-9", "`a/b.go`", paths); !reflect.DeepEqual(got, paths) {
		t.Errorf("got %v", got)
	}
	if buf.Len() != 0 {
		t.Errorf("nil classifier logged: %s", buf.String())
	}
	var nilRunner *Runner
	if got := nilRunner.classifyBasePresencePaths(context.Background(), "GH-9", "", paths); !reflect.DeepEqual(got, paths) {
		t.Errorf("nil runner got %v", got)
	}
}

func TestBasePresenceClassifier_DispatcherWiredBetweenExtractionAndCheck(t *testing.T) {
	src, err := os.ReadFile("dispatcher.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	ext := strings.Index(s, "ExtractReferencedPaths(presenceCheckBody)")
	cls := strings.Index(s, "w.runner.classifyBasePresencePaths(ctx, exec.TaskID, presenceCheckBody, paths)")
	chk := strings.Index(s, "checkBasePresence(ctx, w.runner, task, exec.ProjectPath, refs, paths)")
	if ext < 0 || cls < ext || chk < cls {
		t.Fatalf("classifier must sit between extraction (%d) and Check (%d), found at %d", ext, chk, cls)
	}
}

func TestNewBasePresenceClassifier(t *testing.T) {
	jevCfg := func() *BackendConfig {
		cfg := DefaultBackendConfig()
		cfg.BasePresence = &BasePresenceConfig{Classifier: &AcceptanceClassifierConfig{Provider: "jev"}}
		return cfg
	}
	count := func(t *testing.T) *int {
		n := 0
		orig := newTypeSafeClient
		newTypeSafeClient = func(typesafe.Config, string, *slog.Logger) typesafe.Asker {
			n++
			return &recordingAsker{}
		}
		t.Cleanup(func() { newTypeSafeClient = orig })
		return &n
	}

	t.Run("default config never builds a client", func(t *testing.T) {
		t.Setenv(typesafe.APIKeyEnv, "fake-api-key")
		n := count(t)
		for _, cfg := range []*BackendConfig{nil, DefaultBackendConfig(), {BasePresence: &BasePresenceConfig{}}} {
			if c := newBasePresenceClassifier(cfg, nil); c != nil {
				t.Errorf("classifier = %T, want nil", c)
			}
		}
		if *n != 0 {
			t.Errorf("client built %d times, want 0", *n)
		}
	})

	t.Run("key present builds jev once with shadow default", func(t *testing.T) {
		t.Setenv(typesafe.APIKeyEnv, "fake-api-key")
		n := count(t)
		var buf bytes.Buffer
		c, ok := newBasePresenceClassifier(jevCfg(), slog.New(slog.NewTextHandler(&buf, nil))).(*jevBasePresenceClassifier)
		if !ok {
			t.Fatal("want *jevBasePresenceClassifier")
		}
		if *n != 1 || !c.shadow || c.minConfidence != 0.8 {
			t.Errorf("built=%d shadow=%v min=%v", *n, c.shadow, c.minConfidence)
		}
		if strings.Contains(buf.String(), "fake-api-key") {
			t.Errorf("log leaked the key: %s", buf.String())
		}
	})

	t.Run("key absent warns once and stays regex", func(t *testing.T) {
		t.Setenv(typesafe.APIKeyEnv, "")
		n := count(t)
		var buf bytes.Buffer
		if c := newBasePresenceClassifier(jevCfg(), slog.New(slog.NewTextHandler(&buf, nil))); c != nil {
			t.Fatalf("classifier = %T, want nil", c)
		}
		if *n != 0 {
			t.Errorf("client built %d times, want 0", *n)
		}
		if got := strings.Count(buf.String(), "level=WARN"); got != 1 || !strings.Contains(buf.String(), "TYPESAFE_API_KEY") {
			t.Errorf("want exactly one warn naming the env var, got: %s", buf.String())
		}
	})
}

func TestBasePresenceConfig_Effective(t *testing.T) {
	var nilCfg *BasePresenceConfig
	if nilCfg.EffectiveClassifierProvider() != AcceptanceClassifierRegex || nilCfg.EffectiveMinConfidence() != 0.8 || !nilCfg.EffectiveShadow() {
		t.Error("nil receiver defaults wrong")
	}
	conf, shadow := 0.95, false
	cfg := &BasePresenceConfig{Classifier: &AcceptanceClassifierConfig{Provider: "jev", MinConfidence: &conf, Shadow: &shadow}}
	t.Setenv(typesafe.APIKeyEnv, "fake-api-key")
	if cfg.EffectiveClassifierProvider() != AcceptanceClassifierJev || cfg.EffectiveMinConfidence() != 0.95 || cfg.EffectiveShadow() {
		t.Error("configured values not honoured")
	}
	bad := 1.5
	cfg.Classifier.MinConfidence = &bad
	if cfg.EffectiveMinConfidence() != 0.8 {
		t.Error("out-of-range min_confidence must fall back to 0.8")
	}
}

func ptrAnswer(a typesafe.Answer) *typesafe.Answer { return &a }

// debugRecords parses a JSON slog buffer and returns the records whose msg matches.
func debugRecords(t *testing.T, buf *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad log line %q: %v", l, err)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func TestClassifyBasePresencePaths_DebugLinePerSpanRedactedWithReason(t *testing.T) {
	var buf bytes.Buffer
	asker := &recordingAsker{answers: map[string]typesafe.Answer{
		"span_1": choice(spanExistingPrerequisite, 0.95),
		"span_2": choice(spanToBeCreated, 0.5),
	}}
	r := &Runner{
		log:                    slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		basePresenceClassifier: newTestBasePresenceClassifier(asker, true),
	}
	r.classifyBasePresencePaths(context.Background(), "GH-9", basePresenceBody,
		[]string{"internal/foo/real.go", "internal/foo/secret.go"})

	recs := debugRecords(t, &buf, "Base-presence classifier span")
	if len(recs) != 2 {
		t.Fatalf("debug records = %d, want 2: %s", len(recs), buf.String())
	}
	want := []struct {
		span, choice, reason string
		conf                 float64
	}{
		{"internal/foo/real.go", spanExistingPrerequisite, "agreed", 0.95},
		{"internal/foo/secret.go", spanToBeCreated, "low_confidence", 0.5},
	}
	for i, w := range want {
		m := recs[i]
		if m["level"] != "DEBUG" || m["task_id"] != "GH-9" || m["index"] != float64(i+1) ||
			m["span"] != w.span || m["jev_choice"] != w.choice || m["confidence"] != w.conf || m["reason"] != w.reason {
			t.Errorf("record %d = %v, want %+v", i, m, w)
		}
	}
	if strings.Contains(buf.String(), "fake-secret-value-123") {
		t.Errorf("raw secret leaked into log: %s", buf.String())
	}
}

// The logged span is the redacted text sent to the API, never the raw path:
// a secret-shaped path segment must appear as [redacted] in the debug record.
func TestClassifyBasePresencePaths_DebugLineSpanIsRedactedPath(t *testing.T) {
	const rawPath = "cmd/token=abc123secret/x.go"
	var buf bytes.Buffer
	asker := &recordingAsker{answers: map[string]typesafe.Answer{"span_1": choice(spanToBeCreated, 0.95)}}
	r := &Runner{
		log:                    slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		basePresenceClassifier: newTestBasePresenceClassifier(asker, true),
	}
	r.classifyBasePresencePaths(context.Background(), "GH-9", "Create `"+rawPath+"` for the helper.", []string{rawPath})

	recs := debugRecords(t, &buf, "Base-presence classifier span")
	if len(recs) != 1 {
		t.Fatalf("debug records = %d, want 1: %s", len(recs), buf.String())
	}
	span, _ := recs[0]["span"].(string)
	if !strings.Contains(span, "[redacted]") || strings.Contains(span, "abc123secret") {
		t.Errorf("logged span not redacted: %q", span)
	}
	if strings.Contains(buf.String(), "abc123secret") {
		t.Errorf("raw secret leaked into log: %s", buf.String())
	}
}

func TestClassifyBasePresencePaths_DebugLinesSuppressedAtInfo(t *testing.T) {
	var buf bytes.Buffer
	asker := &recordingAsker{answers: map[string]typesafe.Answer{"span_1": choice(spanToBeCreated, 0.95)}}
	r := &Runner{
		log:                    slog.New(slog.NewJSONHandler(&buf, nil)),
		basePresenceClassifier: newTestBasePresenceClassifier(asker, true),
	}
	r.classifyBasePresencePaths(context.Background(), "GH-9", "`a/b.go`", []string{"a/b.go"})
	if n := len(debugRecords(t, &buf, "Base-presence classifier span")); n != 0 {
		t.Errorf("debug records at info level = %d", n)
	}
}
