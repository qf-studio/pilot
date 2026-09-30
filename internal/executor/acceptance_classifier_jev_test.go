package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/typesafe"
)

// fakeAsker is a scripted typesafe.Asker; no test here touches the network.
type fakeAsker struct {
	answers map[string]typesafe.Answer
	err     error

	calls     int
	gotState  any
	gotQuests map[string]typesafe.Question
}

func (f *fakeAsker) Ask(_ context.Context, state any, questions map[string]typesafe.Question) (typesafe.Answers, error) {
	f.calls++
	f.gotState = state
	f.gotQuests = questions
	if f.err != nil {
		return typesafe.Answers{}, f.err
	}
	return typesafe.Answers{Answers: f.answers}, nil
}

func choice(c string, conf float64) typesafe.Answer {
	return typesafe.Answer{Type: "choice", Choice: c, Confidence: conf}
}

const (
	pasteItemText    = "paste the output of `go vet ./...`"
	guardItemText    = "After replacing the guard in foo.go, TestGuard must fail"
	mutationItemText = "delete line 42 in foo.go -> TestFoo fails"
)

func TestAcceptanceClassifier_JevMerge(t *testing.T) {
	tests := []struct {
		name       string
		criteria   string
		answers    map[string]typesafe.Answer
		shadow     bool
		wantKind   AcceptanceItemKind
		wantTarget string
		wantReason typesafe.Reason
		wantWould  typesafe.Reason
	}{
		{
			name:       "override at 0.9",
			criteria:   pasteItemText,
			answers:    map[string]typesafe.Answer{"kind_1": choice("other", 0.9)},
			wantKind:   AcceptanceItemOther,
			wantReason: typesafe.ReasonOverrode,
			wantWould:  typesafe.ReasonOverrode,
		},
		{
			name:       "keep regex at 0.5",
			criteria:   pasteItemText,
			answers:    map[string]typesafe.Answer{"kind_1": choice("other", 0.5)},
			wantKind:   AcceptanceItemPasteOutput,
			wantReason: typesafe.ReasonLowConfidence,
			wantWould:  typesafe.ReasonLowConfidence,
		},
		{
			name:       "equal kinds agree",
			criteria:   pasteItemText,
			answers:    map[string]typesafe.Answer{"kind_1": choice("paste_output", 0.95)},
			wantKind:   AcceptanceItemPasteOutput,
			wantReason: typesafe.ReasonAgreed,
			wantWould:  typesafe.ReasonAgreed,
		},
		{
			name:       "shadow keeps regex and records would-be reason",
			criteria:   pasteItemText,
			answers:    map[string]typesafe.Answer{"kind_1": choice("other", 0.9)},
			shadow:     true,
			wantKind:   AcceptanceItemPasteOutput,
			wantReason: typesafe.ReasonShadow,
			wantWould:  typesafe.ReasonOverrode,
		},
		{
			name:       "shadow with agreement still reports agreed",
			criteria:   pasteItemText,
			answers:    map[string]typesafe.Answer{"kind_1": choice("paste_output", 0.95)},
			shadow:     true,
			wantKind:   AcceptanceItemPasteOutput,
			wantReason: typesafe.ReasonAgreed,
			wantWould:  typesafe.ReasonAgreed,
		},
		{
			name:     "mutation with target none keeps regex whole",
			criteria: guardItemText,
			answers: map[string]typesafe.Answer{
				"kind_1":   choice("mutation", 0.9),
				"target_1": choice("none", 0.95),
			},
			wantKind:   AcceptanceItemOther,
			wantReason: typesafe.ReasonLowConfidence,
			wantWould:  typesafe.ReasonLowConfidence,
		},
		{
			name:     "mutation with target below min_confidence keeps regex whole",
			criteria: guardItemText,
			answers: map[string]typesafe.Answer{
				"kind_1":   choice("mutation", 0.9),
				"target_1": choice("TestGuard", 0.5),
			},
			wantKind:   AcceptanceItemOther,
			wantReason: typesafe.ReasonLowConfidence,
			wantWould:  typesafe.ReasonLowConfidence,
		},
		{
			name:     "mutation with target outside the offered tokens keeps regex whole",
			criteria: guardItemText,
			answers: map[string]typesafe.Answer{
				"kind_1":   choice("mutation", 0.9),
				"target_1": choice("TestInvented", 0.99),
			},
			wantKind:   AcceptanceItemOther,
			wantReason: typesafe.ReasonLowConfidence,
			wantWould:  typesafe.ReasonLowConfidence,
		},
		{
			name:       "mutation on an item with no test token keeps regex whole",
			criteria:   "the guard in foo.go is replaced and the suite notices",
			answers:    map[string]typesafe.Answer{"kind_1": choice("mutation", 0.9)},
			wantKind:   AcceptanceItemOther,
			wantReason: typesafe.ReasonLowConfidence,
			wantWould:  typesafe.ReasonLowConfidence,
		},
		{
			name:     "valid mutation target sets MutationTarget byte-for-byte",
			criteria: guardItemText,
			answers: map[string]typesafe.Answer{
				"kind_1":   choice("mutation", 0.9),
				"target_1": choice("TestGuard", 0.9),
			},
			wantKind:   AcceptanceItemMutation,
			wantTarget: "TestGuard",
			wantReason: typesafe.ReasonOverrode,
			wantWould:  typesafe.ReasonOverrode,
		},
		{
			name:     "shadow never applies a mutation override",
			criteria: guardItemText,
			answers: map[string]typesafe.Answer{
				"kind_1":   choice("mutation", 0.9),
				"target_1": choice("TestGuard", 0.9),
			},
			shadow:     true,
			wantKind:   AcceptanceItemOther,
			wantReason: typesafe.ReasonShadow,
			wantWould:  typesafe.ReasonOverrode,
		},
		{
			name:       "paste_output without commands keeps regex whole",
			criteria:   "run the whole suite and show what happens",
			answers:    map[string]typesafe.Answer{"kind_1": choice("paste_output", 0.9)},
			wantKind:   AcceptanceItemOther,
			wantReason: typesafe.ReasonLowConfidence,
			wantWould:  typesafe.ReasonLowConfidence,
		},
		{
			name:       "paste_output takes commands from backtick spans",
			criteria:   "Make sure `go build ./...` is fine",
			answers:    map[string]typesafe.Answer{"kind_1": choice("paste_output", 0.9)},
			wantKind:   AcceptanceItemPasteOutput,
			wantReason: typesafe.ReasonOverrode,
			wantWould:  typesafe.ReasonOverrode,
		},
		{
			name:       "regex mutation shape is never overridden to paste_output (#5486)",
			criteria:   "delete the output line in `foo.go` line 42 -> TestFoo fails",
			answers:    map[string]typesafe.Answer{"kind_1": choice("paste_output", 0.99)},
			wantKind:   AcceptanceItemMutation,
			wantTarget: "TestFoo",
			wantReason: typesafe.ReasonLowConfidence,
			wantWould:  typesafe.ReasonLowConfidence,
		},
		{
			name:       "regex mutation with a backtick span stays mutation with no commands at high-confidence paste_output (#5486)",
			criteria:   "delete the nil check in `internal/executor/lifecycle.go` -> TestX fails",
			answers:    map[string]typesafe.Answer{"kind_1": choice("paste_output", 0.99)},
			wantKind:   AcceptanceItemMutation,
			wantTarget: "TestX",
			wantReason: typesafe.ReasonLowConfidence,
			wantWould:  typesafe.ReasonLowConfidence,
		},
		{
			name:       "invalid choice is an error and keeps regex",
			criteria:   pasteItemText,
			answers:    map[string]typesafe.Answer{"kind_1": choice("banana", 0.99)},
			wantKind:   AcceptanceItemPasteOutput,
			wantReason: typesafe.ReasonError,
			wantWould:  typesafe.ReasonError,
		},
		{
			name:       "missing answer is an error and keeps regex",
			criteria:   mutationItemText,
			answers:    map[string]typesafe.Answer{},
			wantKind:   AcceptanceItemMutation,
			wantTarget: "TestFoo",
			wantReason: typesafe.ReasonError,
			wantWould:  typesafe.ReasonError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asker := &fakeAsker{answers: tt.answers}
			c := &jevAcceptanceClassifier{asker: asker, minConfidence: 0.8, shadow: tt.shadow}
			items, stats := c.Classify(context.Background(), []string{tt.criteria})

			if asker.calls != 1 {
				t.Fatalf("Ask calls = %d, want 1", asker.calls)
			}
			if len(items) != 1 {
				t.Fatalf("items = %d, want 1", len(items))
			}
			got := items[0]
			if got.Kind != tt.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tt.wantKind)
			}
			if got.MutationTarget != tt.wantTarget {
				t.Errorf("MutationTarget = %q, want %q", got.MutationTarget, tt.wantTarget)
			}
			if tt.wantKind == AcceptanceItemMutation && len(got.Commands) != 0 {
				t.Errorf("Commands = %v, want none for a mutation item", got.Commands)
			}
			if stats.Reasons[0] != tt.wantReason {
				t.Errorf("Reason = %q, want %q", stats.Reasons[0], tt.wantReason)
			}
			if stats.WouldBe[0] != tt.wantWould {
				t.Errorf("WouldBe = %q, want %q", stats.WouldBe[0], tt.wantWould)
			}
			if stats.Shadow != tt.shadow || stats.Classifier != AcceptanceClassifierJev || stats.Items != 1 {
				t.Errorf("stats header = %+v", stats)
			}
			if tt.wantWould == typesafe.ReasonOverrode && stats.Overrode != 1 {
				t.Errorf("Overrode = %d, want 1 (would-be overrides count in shadow)", stats.Overrode)
			}
		})
	}
}

func TestAcceptanceClassifier_JevAskErrorKeepsRegex(t *testing.T) {
	asker := &fakeAsker{err: errors.New("typesafe: status 529")}
	c := &jevAcceptanceClassifier{asker: asker, minConfidence: 0.8}
	criteria := []string{pasteItemText, mutationItemText, "nothing to see"}

	items, stats := c.Classify(context.Background(), criteria)

	want := ParseAcceptanceItems(criteria)
	if len(items) != len(want) {
		t.Fatalf("items = %d, want %d", len(items), len(want))
	}
	for i := range want {
		if items[i].Kind != want[i].Kind || items[i].MutationTarget != want[i].MutationTarget {
			t.Errorf("item %d = %+v, want regex %+v", i, items[i], want[i])
		}
		if stats.Reasons[i] != typesafe.ReasonError {
			t.Errorf("reason %d = %q, want error", i, stats.Reasons[i])
		}
	}
	if stats.Errors != 1 {
		t.Errorf("Errors = %d, want 1", stats.Errors)
	}
}

func TestAcceptanceClassifier_JevQuestionsAndRedactedState(t *testing.T) {
	asker := &fakeAsker{answers: map[string]typesafe.Answer{}}
	c := &jevAcceptanceClassifier{asker: asker, minConfidence: 0.8, shadow: true}
	long := strings.Repeat("x", 2*maxAcceptanceItemChars)
	criteria := []string{
		"pass token=abc123secret and TestA, TestB, TestA must fail",
		long,
		"no test names here",
	}
	c.Classify(context.Background(), criteria)

	if asker.calls != 1 {
		t.Fatalf("Ask calls = %d, want 1 per task", asker.calls)
	}
	state, ok := asker.gotState.(map[string]any)
	if !ok {
		t.Fatalf("state type = %T", asker.gotState)
	}
	texts, ok := state["items"].(map[string]string)
	if !ok {
		t.Fatalf("state[items] type = %T", state["items"])
	}
	if strings.Contains(texts["1"], "abc123secret") || !strings.Contains(texts["1"], "[redacted]") {
		t.Errorf("item 1 not redacted: %q", texts["1"])
	}
	if got := len([]rune(texts["2"])); got != maxAcceptanceItemChars {
		t.Errorf("item 2 length = %d, want cap %d", got, maxAcceptanceItemChars)
	}

	for _, key := range []string{"kind_1", "kind_2", "kind_3", "target_1"} {
		if _, ok := asker.gotQuests[key]; !ok {
			t.Errorf("missing question %s", key)
		}
	}
	for _, key := range []string{"target_2", "target_3"} {
		if _, ok := asker.gotQuests[key]; ok {
			t.Errorf("unexpected question %s", key)
		}
	}
	kind, _ := asker.gotQuests["kind_1"].Criteria.(map[string]any)
	for _, opt := range []string{"mutation", "paste_output", "other"} {
		if _, ok := kind[opt]; !ok {
			t.Errorf("kind_1 missing option %q", opt)
		}
	}
	target, _ := asker.gotQuests["target_1"].Criteria.(map[string]any)
	if len(target) != 3 { // TestA, TestB (deduplicated), none
		t.Errorf("target_1 options = %v", target)
	}
	for _, opt := range []string{"TestA", "TestB", "none"} {
		if _, ok := target[opt]; !ok {
			t.Errorf("target_1 missing option %q", opt)
		}
	}
}

func TestAcceptanceClassifier_Regex(t *testing.T) {
	items, stats := regexAcceptanceClassifier{}.Classify(context.Background(), []string{pasteItemText, "other thing"})
	if len(items) != 2 || stats.Classifier != "regex" || stats.RegexOnly != 2 {
		t.Fatalf("items=%d stats=%+v", len(items), stats)
	}
	for i, r := range stats.Reasons {
		if r != typesafe.ReasonRegexOnly {
			t.Errorf("reason %d = %q", i, r)
		}
	}
}

func TestAcceptanceClassifier_DefaultConfigNeverBuildsClient(t *testing.T) {
	t.Setenv(typesafe.APIKeyEnv, "fake-api-key")
	built := 0
	orig := newTypeSafeClient
	newTypeSafeClient = func(typesafe.Config, string, *slog.Logger) typesafe.Asker {
		built++
		return &fakeAsker{}
	}
	t.Cleanup(func() { newTypeSafeClient = orig })

	c := newAcceptanceClassifier(DefaultBackendConfig(), nil)
	if _, ok := c.(regexAcceptanceClassifier); !ok {
		t.Fatalf("default config classifier = %T, want regexAcceptanceClassifier", c)
	}
	if built != 0 {
		t.Fatalf("client constructed %d times under default config, want 0", built)
	}
}

func TestNewAcceptanceClassifier_Jev(t *testing.T) {
	jevCfg := func() *BackendConfig {
		cfg := DefaultBackendConfig()
		cfg.AcceptanceEvidence = &AcceptanceEvidenceConfig{Classifier: &AcceptanceClassifierConfig{Provider: "jev"}}
		return cfg
	}
	count := func(t *testing.T) *int {
		n := 0
		orig := newTypeSafeClient
		newTypeSafeClient = func(typesafe.Config, string, *slog.Logger) typesafe.Asker {
			n++
			return &fakeAsker{}
		}
		t.Cleanup(func() { newTypeSafeClient = orig })
		return &n
	}

	t.Run("key present builds jev once and logs auth_source", func(t *testing.T) {
		t.Setenv(typesafe.APIKeyEnv, "fake-api-key")
		n := count(t)
		var buf bytes.Buffer
		c := newAcceptanceClassifier(jevCfg(), slog.New(slog.NewTextHandler(&buf, nil)))
		j, ok := c.(*jevAcceptanceClassifier)
		if !ok {
			t.Fatalf("classifier = %T, want *jevAcceptanceClassifier", c)
		}
		if *n != 1 {
			t.Errorf("client built %d times, want 1", *n)
		}
		if !j.shadow || j.minConfidence != 0.8 {
			t.Errorf("defaults: shadow=%v min=%v", j.shadow, j.minConfidence)
		}
		out := buf.String()
		if !strings.Contains(out, "auth_source=env:TYPESAFE_API_KEY") {
			t.Errorf("missing auth_source in log: %s", out)
		}
		if strings.Contains(out, "fake-api-key") {
			t.Errorf("log leaked the key: %s", out)
		}
	})

	t.Run("key absent warns and falls back to regex", func(t *testing.T) {
		t.Setenv(typesafe.APIKeyEnv, "")
		n := count(t)
		var buf bytes.Buffer
		c := newAcceptanceClassifier(jevCfg(), slog.New(slog.NewTextHandler(&buf, nil)))
		if _, ok := c.(regexAcceptanceClassifier); !ok {
			t.Fatalf("classifier = %T, want regex", c)
		}
		if *n != 0 {
			t.Errorf("client built %d times, want 0", *n)
		}
		if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "TYPESAFE_API_KEY") {
			t.Errorf("expected one warn naming the env var, got: %s", buf.String())
		}
	})
}

func TestAppendAcceptanceEvidence_LogsClassificationLine(t *testing.T) {
	var buf bytes.Buffer
	asker := &fakeAsker{answers: map[string]typesafe.Answer{"kind_1": choice("other", 0.9)}}
	r := &Runner{
		config:               DefaultBackendConfig(),
		log:                  slog.New(slog.NewJSONHandler(&buf, nil)),
		acceptanceRunner:     &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{"go vet ./...": {output: "ok"}}},
		acceptanceClassifier: &jevAcceptanceClassifier{asker: asker, minConfidence: 0.8, shadow: true},
	}
	task := &Task{ID: "GH-9", AcceptanceCriteria: []string{pasteItemText}}

	got := r.appendAcceptanceEvidence(context.Background(), task, t.TempDir(), "## Summary")
	if !strings.Contains(got, "go vet ./...") {
		t.Fatalf("shadow mode must keep the regex paste_output item and run it, got %q", got)
	}

	var line map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad log line %q: %v", l, err)
		}
		if m["msg"] == "Acceptance classification" {
			if line != nil {
				t.Fatal("more than one classification line per task")
			}
			line = m
		}
	}
	if line == nil {
		t.Fatalf("no classification line in %s", buf.String())
	}
	if line["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", line["level"])
	}
	for _, f := range []string{"task_id", "classifier", "items", "regex_only", "agreed", "overrode", "low_confidence", "errors", "shadow", "latency_ms"} {
		if _, ok := line[f]; !ok {
			t.Errorf("missing field %q in %v", f, line)
		}
	}
	if line["task_id"] != "GH-9" || line["classifier"] != "jev" || line["overrode"] != float64(1) || line["shadow"] != true {
		t.Errorf("unexpected field values: %v", line)
	}
}
