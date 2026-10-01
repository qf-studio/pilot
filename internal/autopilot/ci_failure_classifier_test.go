package autopilot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/typesafe"
)

// ciFailureFakeAsker is a scripted typesafe.Asker; no test here touches the
// network. answers are returned even when err is set, so a missing Ask-error
// fallback would visibly act on them.
type ciFailureFakeAsker struct {
	answers map[string]typesafe.Answer
	err     error

	calls     int
	gotState  any
	gotQuests map[string]typesafe.Question
}

func (f *ciFailureFakeAsker) Ask(_ context.Context, state any, questions map[string]typesafe.Question) (typesafe.Answers, error) {
	f.calls++
	f.gotState = state
	f.gotQuests = questions
	return typesafe.Answers{Answers: f.answers}, f.err
}

func ciAnswer(c string, conf float64) typesafe.Answer {
	return typesafe.Answer{Type: "choice", Choice: c, Confidence: conf}
}

// ciCodeCheck is classified `code` by the regex floor (no signal either way).
func ciCodeCheck(name string) FailedCheckLog {
	return FailedCheckLog{CheckName: name, Logs: "some failure output with no known signature"}
}

// ciInfraCheck is classified `infra` by the regex floor (structural signal).
func ciInfraCheck(name string) FailedCheckLog {
	return FailedCheckLog{CheckName: name, Conclusion: conclusionStartupFailure}
}

func newTestCIFailureClassifier(asker typesafe.Asker, shadow bool, log *slog.Logger) *ciFailureClassifier {
	return &ciFailureClassifier{asker: asker, minConfidence: 0.8, shadow: shadow, log: log}
}

func bufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestCIFailureClassifier_QuestionsPerCheck(t *testing.T) {
	longLog := "HEAD-MARKER " + strings.Repeat("setup noise line\n", 2000) + "password=hunter2\nTAIL-MARKER compile failed"
	checks := []FailedCheckLog{
		{CheckName: "build-linux", Logs: longLog, AnnotationText: "annotation text one"},
		{CheckName: "lint-fmt", Logs: "second log", AnnotationText: ""},
	}
	asker := &ciFailureFakeAsker{answers: map[string]typesafe.Answer{}}
	c := newTestCIFailureClassifier(asker, true, nil)
	c.classify(context.Background(), checks, "o/r")

	if asker.calls != 1 {
		t.Fatalf("Ask calls = %d, want exactly 1 per classification", asker.calls)
	}
	if len(asker.gotQuests) != 2 {
		t.Fatalf("questions = %d, want one per failed check", len(asker.gotQuests))
	}
	q1, ok1 := asker.gotQuests["check_1"]
	q2, ok2 := asker.gotQuests["check_2"]
	if !ok1 || !ok2 {
		t.Fatalf("question keys = %v, want check_1 and check_2", asker.gotQuests)
	}
	ins1, _ := q1.Instructions.(string)
	ins2, _ := q2.Instructions.(string)
	if ins1 == ins2 {
		t.Fatalf("instructions are byte-identical across checks; Jev would return one distribution: %q", ins1)
	}
	for _, want := range []string{"check 1", "build-linux", "annotation text one", "TAIL-MARKER"} {
		if !strings.Contains(ins1, want) {
			t.Errorf("instruction 1 missing %q: %s", want, ins1)
		}
	}
	for _, want := range []string{"check 2", "lint-fmt", "second log", "(none)"} {
		if !strings.Contains(ins2, want) {
			t.Errorf("instruction 2 missing %q: %s", want, ins2)
		}
	}
	if strings.Contains(ins1, "HEAD-MARKER") {
		t.Error("instruction quotes the log head; only the tail may be sent")
	}
	if strings.Contains(ins1, "hunter2") {
		t.Error("secret-shaped text was not redacted")
	}
	if !strings.Contains(ins1, "[redacted]") {
		t.Error("expected the redaction placeholder in the excerpt")
	}

	for _, key := range []string{string(FailureClassCode), string(FailureClassInfra), string(FailureClassInfraBilling), string(FailureClassUnknown)} {
		crit, _ := q1.Criteria.(map[string]any)
		if _, ok := crit[key]; !ok {
			t.Errorf("choice %q missing from criteria %v", key, crit)
		}
	}
	if q1.Type != "choice" {
		t.Errorf("question type = %q, want choice", q1.Type)
	}

	state, _ := asker.gotState.(map[string]any)
	sent, _ := state["checks"].(map[string]string)
	if len(sent) != 2 {
		t.Fatalf("state.checks = %v, want one excerpt per check", state)
	}
	if n := len([]rune(sent["1"])); n > len("annotation: ")+ciFailureAnnotationChars+len("\nlog tail: ")+ciFailureLogTailChars {
		t.Errorf("excerpt is %d runes, exceeds annotation+tail caps", n)
	}
}

func TestCIFailureExcerpt_TailOnlyAndCapped(t *testing.T) {
	log := strings.Repeat("x", 20000) + "END"
	ex := ciFailureExcerpt(FailedCheckLog{Logs: log, AnnotationText: strings.Repeat("a", 5000)})
	parts := strings.SplitN(ex, "\nlog tail: ", 2)
	if len(parts) != 2 {
		t.Fatalf("excerpt shape: %q", ex[:60])
	}
	if got := len([]rune(parts[1])); got != ciFailureLogTailChars {
		t.Errorf("log tail = %d runes, want %d", got, ciFailureLogTailChars)
	}
	if !strings.HasSuffix(parts[1], "END") {
		t.Error("tail must keep the end of the log")
	}
	if got := len([]rune(strings.TrimPrefix(parts[0], "annotation: "))); got != ciFailureAnnotationChars {
		t.Errorf("annotation = %d runes, want cap %d", got, ciFailureAnnotationChars)
	}
	empty := ciFailureExcerpt(FailedCheckLog{})
	if empty != "annotation: (none)\nlog tail: (none)" {
		t.Errorf("empty excerpt = %q", empty)
	}
}

func TestCIFailureClassifier_Decisions(t *testing.T) {
	tests := []struct {
		name         string
		check        FailedCheckLog
		answer       *typesafe.Answer
		shadow       bool
		want         FailureClass
		wantSignal   classificationSignal
		wantOverride int
		wantAgreed   int
		wantLow      int
		wantVetoed   int
		wantErrors   int
	}{
		{name: "live code to infra", check: ciCodeCheck("test"), answer: ptr(ciAnswer("infra", 0.95)),
			want: FailureClassInfra, wantSignal: signalJev, wantOverride: 1},
		{name: "live code to billing", check: ciCodeCheck("test"), answer: ptr(ciAnswer("infra_billing", 0.9)),
			want: FailureClassInfraBilling, wantSignal: signalJev, wantOverride: 1},
		{name: "at threshold overrides", check: ciCodeCheck("test"), answer: ptr(ciAnswer("infra", 0.8)),
			want: FailureClassInfra, wantSignal: signalJev, wantOverride: 1},
		{name: "shadow keeps regex but counts would-override", check: ciCodeCheck("test"), answer: ptr(ciAnswer("infra", 0.95)), shadow: true,
			want: FailureClassCode, wantSignal: signalNone, wantOverride: 1},
		{name: "low confidence keeps regex", check: ciCodeCheck("test"), answer: ptr(ciAnswer("infra", 0.79)),
			want: FailureClassCode, wantSignal: signalNone, wantLow: 1},
		{name: "unknown keeps regex", check: ciCodeCheck("test"), answer: ptr(ciAnswer("unknown", 0.99)),
			want: FailureClassCode, wantSignal: signalNone, wantVetoed: 1},
		{name: "infra to code never", check: ciInfraCheck("test"), answer: ptr(ciAnswer("code", 0.99)),
			want: FailureClassInfra, wantSignal: signalStructural, wantVetoed: 1},
		{name: "infra to unknown never", check: ciInfraCheck("test"), answer: ptr(ciAnswer("unknown", 0.99)),
			want: FailureClassInfra, wantSignal: signalStructural, wantVetoed: 1},
		{name: "infra to billing keeps regex", check: ciInfraCheck("test"), answer: ptr(ciAnswer("infra_billing", 0.99)),
			want: FailureClassInfra, wantSignal: signalStructural, wantVetoed: 1},
		{name: "agreement code", check: ciCodeCheck("test"), answer: ptr(ciAnswer("code", 0.9)),
			want: FailureClassCode, wantSignal: signalNone, wantAgreed: 1},
		{name: "agreement infra", check: ciInfraCheck("test"), answer: ptr(ciAnswer("infra", 0.9)),
			want: FailureClassInfra, wantSignal: signalStructural, wantAgreed: 1},
		{name: "invalid choice is an error", check: ciCodeCheck("test"), answer: ptr(ciAnswer("flaky", 0.99)),
			want: FailureClassCode, wantSignal: signalNone, wantErrors: 1},
		{name: "missing answer is an error", check: ciCodeCheck("test"), answer: nil,
			want: FailureClassCode, wantSignal: signalNone, wantErrors: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			answers := map[string]typesafe.Answer{}
			if tt.answer != nil {
				answers["check_1"] = *tt.answer
			}
			c := newTestCIFailureClassifier(&ciFailureFakeAsker{answers: answers}, tt.shadow, nil)
			per, stats := c.classify(context.Background(), []FailedCheckLog{tt.check}, "o/r")
			if per[0].class != tt.want || per[0].signal != tt.wantSignal {
				t.Errorf("verdict = %s/%s, want %s/%s", per[0].class, per[0].signal, tt.want, tt.wantSignal)
			}
			if stats.WouldOverride != tt.wantOverride || stats.Agreed != tt.wantAgreed || stats.LowConfidence != tt.wantLow ||
				stats.Vetoed != tt.wantVetoed || stats.Errors != tt.wantErrors {
				t.Errorf("stats = %+v, want override=%d agreed=%d low=%d vetoed=%d errors=%d",
					stats, tt.wantOverride, tt.wantAgreed, tt.wantLow, tt.wantVetoed, tt.wantErrors)
			}
			wantApplied := 0
			if !tt.shadow {
				wantApplied = tt.wantOverride
			}
			if stats.Overridden != wantApplied {
				t.Errorf("Overridden = %d, want %d", stats.Overridden, wantApplied)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// TestCIFailureClassifier_AskErrorKeepsRegexVerdict pins the Ask-error
// fallback: the fake returns confident infra answers alongside the error, so a
// classifier that processed them anyway would override.
func TestCIFailureClassifier_AskErrorKeepsRegexVerdict(t *testing.T) {
	var buf bytes.Buffer
	asker := &ciFailureFakeAsker{
		answers: map[string]typesafe.Answer{"check_1": ciAnswer("infra", 0.99), "check_2": ciAnswer("infra", 0.99)},
		err:     errors.New("typesafe: status 529"),
	}
	c := newTestCIFailureClassifier(asker, false, bufLogger(&buf))
	checks := []FailedCheckLog{ciCodeCheck("a"), ciCodeCheck("b")}
	per, stats := c.classify(context.Background(), checks, "o/r")
	for i, v := range per {
		if v.class != FailureClassCode || v.signal == signalJev {
			t.Errorf("check %d = %s/%s after Ask error, want regex code", i+1, v.class, v.signal)
		}
	}
	if stats.Errors != 1 || stats.Overridden != 0 || stats.WouldOverride != 0 {
		t.Errorf("stats = %+v, want exactly one error and no override", stats)
	}
	out := buf.String()
	if !strings.Contains(out, "CI failure classifier Ask failed") || !strings.Contains(out, "level=WARN") {
		t.Errorf("expected a WARN line for the Ask failure, got: %s", out)
	}
}

func TestCIFailureClassifier_LogLines(t *testing.T) {
	var buf bytes.Buffer
	asker := &ciFailureFakeAsker{answers: map[string]typesafe.Answer{
		"check_1": ciAnswer("infra", 0.95),
		"check_2": ciAnswer("code", 0.9),
	}}
	c := newTestCIFailureClassifier(asker, true, bufLogger(&buf))
	c.classify(context.Background(), []FailedCheckLog{
		ciCodeCheck("build"),
		ciCodeCheck("test"),
	}, "o/r")

	var info []string
	checkLines := 0
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		switch {
		case strings.Contains(line, "level=INFO") && strings.Contains(line, `msg="CI failure classifier"`):
			info = append(info, line)
		case strings.Contains(line, "level=INFO") && strings.Contains(line, `msg="CI failure classifier check"`):
			checkLines++
			for _, key := range []string{"excerpt_head=", "jev_choice=", "confidence=", "reason="} {
				if !strings.Contains(line, key) {
					t.Errorf("check line missing %s: %s", key, line)
				}
			}
		}
	}
	if len(info) != 1 {
		t.Fatalf("info lines = %d, want exactly one per classification:\n%s", len(info), buf.String())
	}
	for _, key := range []string{"checks=2", "agreed=1", "would_override=1", "low_confidence=0", "errors=0", "latency_ms="} {
		if !strings.Contains(info[0], key) {
			t.Errorf("info line missing %s: %s", key, info[0])
		}
	}
	if checkLines != 2 {
		t.Errorf("check lines = %d, want one per check", checkLines)
	}
}

// TestCIFailureClassifier_CheckLineEmittedAtInfo pins the per-check shadow line
// at INFO: the box logs at INFO and never writes DEBUG, so a Debug call would
// leave gate 3 with no flip data (same defect as #5556 for gates 1 and 2).
func TestCIFailureClassifier_CheckLineEmittedAtInfo(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	asker := &ciFailureFakeAsker{answers: map[string]typesafe.Answer{"check_1": ciAnswer("infra", 0.95)}}
	c := newTestCIFailureClassifier(asker, true, log)
	c.classify(context.Background(), []FailedCheckLog{ciCodeCheck("build")}, "o/r")

	var line string
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(l, `msg="CI failure classifier check"`) {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("per-check line not captured by an Info-level handler:\n%s", buf.String())
	}
	if !strings.Contains(line, "level=INFO") {
		t.Errorf("per-check line level is not INFO: %s", line)
	}
	for _, want := range []string{"check=build", "jev_choice=infra", "confidence=0.95", "outcome="} {
		if !strings.Contains(line, want) {
			t.Errorf("per-check line missing %s: %s", want, line)
		}
	}
}

func TestCIFailureClassifier_NoChecksNoAsk(t *testing.T) {
	asker := &ciFailureFakeAsker{}
	ctrl := &Controller{owner: "o", repo: "r", ciFailureClassifier: newTestCIFailureClassifier(asker, false, nil)}
	got := ctrl.classifyCIFailure(context.Background(), nil)
	if asker.calls != 0 {
		t.Errorf("Ask called %d times for zero checks", asker.calls)
	}
	if got.Class != FailureClassUnknown || got.Verdict.AuthorizesDestructive() {
		t.Errorf("zero checks = %s authorizes=%v, want unknown / no destructive", got.Class, got.Verdict.AuthorizesDestructive())
	}
}

// TestCIFailureClassifier_ControllerSeam exercises classifyCIFailure, the one
// seam both controller call sites use.
func TestCIFailureClassifier_ControllerSeam(t *testing.T) {
	two := []FailedCheckLog{ciCodeCheck("build"), ciCodeCheck("test")}

	tests := []struct {
		name       string
		checks     []FailedCheckLog
		classifier *ciFailureClassifier
		want       FailureClass
		wantSource string
		wantEvid   string
	}{
		{name: "no classifier equals classifyPRFailure", checks: two, classifier: nil,
			want: classifyPRFailure(two), wantSource: "classifyPRFailure", wantEvid: "build:code(none); test:code(none)"},
		{name: "live: one of two moved stays code", checks: two,
			classifier: newTestCIFailureClassifier(&ciFailureFakeAsker{answers: map[string]typesafe.Answer{
				"check_1": ciAnswer("infra", 0.95), "check_2": ciAnswer("code", 0.95)}}, false, nil),
			want: FailureClassCode, wantSource: ciFailureJevSource, wantEvid: "test:code(none)"},
		{name: "live: all moved becomes infra and names the classifier", checks: two,
			classifier: newTestCIFailureClassifier(&ciFailureFakeAsker{answers: map[string]typesafe.Answer{
				"check_1": ciAnswer("infra", 0.95), "check_2": ciAnswer("infra", 0.95)}}, false, nil),
			want: FailureClassInfra, wantSource: ciFailureJevSource, wantEvid: "build:infra(jev); test:infra(jev)"},
		{name: "live: billing wins the aggregate", checks: two,
			classifier: newTestCIFailureClassifier(&ciFailureFakeAsker{answers: map[string]typesafe.Answer{
				"check_1": ciAnswer("infra", 0.95), "check_2": ciAnswer("infra_billing", 0.95)}}, false, nil),
			want: FailureClassInfraBilling, wantSource: ciFailureJevSource, wantEvid: "build:infra(jev); test:infra_billing(jev)"},
		{name: "shadow never changes the verdict or source", checks: two,
			classifier: newTestCIFailureClassifier(&ciFailureFakeAsker{answers: map[string]typesafe.Answer{
				"check_1": ciAnswer("infra", 0.99), "check_2": ciAnswer("infra", 0.99)}}, true, nil),
			want: FailureClassCode, wantSource: "classifyPRFailure", wantEvid: "build:code(none); test:code(none)"},
		{name: "live agreement keeps the regex source", checks: two,
			classifier: newTestCIFailureClassifier(&ciFailureFakeAsker{answers: map[string]typesafe.Answer{
				"check_1": ciAnswer("code", 0.95), "check_2": ciAnswer("code", 0.95)}}, false, nil),
			want: FailureClassCode, wantSource: "classifyPRFailure", wantEvid: "build:code(none); test:code(none)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := &Controller{owner: "o", repo: "r", ciFailureClassifier: tt.classifier}
			got := ctrl.classifyCIFailure(context.Background(), tt.checks)
			if got.Class != tt.want || got.Verdict.Class() != tt.want {
				t.Errorf("class = %s / verdict %s, want %s", got.Class, got.Verdict.Class(), tt.want)
			}
			if got.Verdict.Source() != tt.wantSource {
				t.Errorf("source = %q, want %q", got.Verdict.Source(), tt.wantSource)
			}
			if got.Verdict.Evidence() != tt.wantEvid {
				t.Errorf("evidence = %q, want %q", got.Verdict.Evidence(), tt.wantEvid)
			}
			if got.Verdict.Scope() != "o/r" {
				t.Errorf("scope = %q", got.Verdict.Scope())
			}
		})
	}
}

func TestCIFailureClassifier_Constructor(t *testing.T) {
	jevCfg := func(shadow *bool) *Config {
		return &Config{CIFailure: &CIFailureConfig{Classifier: &CIFailureClassifierConfig{Provider: "jev", Shadow: shadow}}}
	}
	count := 0
	var gotCfg typesafe.Config
	var gotKey string
	orig := newCIFailureTypeSafeClient
	newCIFailureTypeSafeClient = func(cfg typesafe.Config, key string, _ *slog.Logger) typesafe.Asker {
		count++
		gotCfg, gotKey = cfg, key
		return &ciFailureFakeAsker{}
	}
	t.Cleanup(func() { newCIFailureTypeSafeClient = orig })

	t.Run("default config never constructs a client", func(t *testing.T) {
		t.Setenv(typesafe.APIKeyEnv, "test-key")
		count = 0
		for _, cfg := range []*Config{nil, {}, {CIFailure: &CIFailureConfig{}},
			{CIFailure: &CIFailureConfig{Classifier: &CIFailureClassifierConfig{}}},
			{CIFailure: &CIFailureConfig{Classifier: &CIFailureClassifierConfig{Provider: "regex"}}}} {
			if c := newCIFailureClassifier(cfg, nil); c != nil {
				t.Errorf("classifier built for %+v", cfg)
			}
		}
		if count != 0 {
			t.Errorf("client constructed %d times", count)
		}
	})

	t.Run("jev without key warns once and stays regex", func(t *testing.T) {
		t.Setenv(typesafe.APIKeyEnv, "")
		count = 0
		var buf bytes.Buffer
		if c := newCIFailureClassifier(jevCfg(nil), bufLogger(&buf)); c != nil {
			t.Error("classifier built without an API key")
		}
		if count != 0 {
			t.Errorf("client constructed %d times without a key", count)
		}
		if n := strings.Count(buf.String(), "level=WARN"); n != 1 || !strings.Contains(buf.String(), typesafe.APIKeyEnv) {
			t.Errorf("want exactly one WARN naming %s, got: %s", typesafe.APIKeyEnv, buf.String())
		}
	})

	t.Run("jev with key builds one client with the shared block, shadow default", func(t *testing.T) {
		t.Setenv(typesafe.APIKeyEnv, "test-key")
		count = 0
		cfg := jevCfg(nil)
		cfg.TypeSafe = &typesafe.Config{Model: "jev-test"}
		c := newCIFailureClassifier(cfg, nil)
		if c == nil || count != 1 {
			t.Fatalf("classifier=%v constructions=%d, want one", c, count)
		}
		if !c.shadow || c.minConfidence != 0.8 {
			t.Errorf("defaults: shadow=%v min=%v, want true / 0.8", c.shadow, c.minConfidence)
		}
		if gotCfg.Model != "jev-test" || gotKey != "test-key" {
			t.Errorf("client got cfg=%+v key=%q", gotCfg, gotKey)
		}
	})

	t.Run("shadow false and min_confidence flow through", func(t *testing.T) {
		t.Setenv(typesafe.APIKeyEnv, "test-key")
		off, min := false, 0.9
		cfg := jevCfg(&off)
		cfg.CIFailure.Classifier.MinConfidence = &min
		c := newCIFailureClassifier(cfg, nil)
		if c == nil || c.shadow || c.minConfidence != 0.9 {
			t.Errorf("classifier = %+v", c)
		}
	})
}

func TestCIFailureConfig_Accessors(t *testing.T) {
	var nilCfg *CIFailureConfig
	if nilCfg.EffectiveClassifierProvider() != CIFailureClassifierRegex || nilCfg.EffectiveMinConfidence() != 0.8 || !nilCfg.EffectiveShadow() {
		t.Error("nil receiver defaults wrong")
	}
	for _, bad := range []float64{-0.1, 1.1} {
		v := bad
		c := &CIFailureConfig{Classifier: &CIFailureClassifierConfig{MinConfidence: &v}}
		if got := c.EffectiveMinConfidence(); got != 0.8 {
			t.Errorf("min_confidence %v = %v, want fallback 0.8", bad, got)
		}
	}
	ok := 0.65
	c := &CIFailureConfig{Classifier: &CIFailureClassifierConfig{Provider: " JEV ", MinConfidence: &ok}}
	if c.EffectiveMinConfidence() != 0.65 {
		t.Error("explicit min_confidence ignored")
	}
	t.Setenv(typesafe.APIKeyEnv, "")
	if c.EffectiveClassifierProvider() != CIFailureClassifierRegex {
		t.Error("jev without key must report regex")
	}
	t.Setenv(typesafe.APIKeyEnv, "test-key")
	if c.EffectiveClassifierProvider() != CIFailureClassifierJev {
		t.Error("jev with key must report jev")
	}
}

// TestCIFailureClassifier_InstructionFormatPerCheck guards the instruction
// template itself: it must interpolate the index, name and excerpt.
func TestCIFailureClassifier_InstructionFormatPerCheck(t *testing.T) {
	a := fmt.Sprintf(ciFailureInstructionsFmt, "1", "1", "n1", "e1")
	b := fmt.Sprintf(ciFailureInstructionsFmt, "2", "2", "n2", "e2")
	if a == b || strings.Count(ciFailureInstructionsFmt, "%") < 4 {
		t.Errorf("instruction template does not vary per check: %q", ciFailureInstructionsFmt)
	}
}

// TestCIFailureClassifier_LiveSmoke is the pre-merge live smoke (TASK-507
// acceptance 7): four labelled excerpts through the real TypeSafe client, run in
// shadow so nothing but the per-check output matters. It skips without
// TYPESAFE_API_KEY. Run with:
//
//	TYPESAFE_API_KEY=... go test ./internal/autopilot/ -run CIFailureClassifier_LiveSmoke -v -count=1
func TestCIFailureClassifier_LiveSmoke(t *testing.T) {
	key, _ := typesafe.APIKeyFromEnv()
	if key == "" {
		t.Skip("TYPESAFE_API_KEY not set; live smoke skipped")
	}
	checks := []struct {
		label string
		chk   FailedCheckLog
	}{
		{"go compile error", FailedCheckLog{CheckName: "test", Logs: "go build ./...\n# github.com/example/app/internal/foo\ninternal/foo/bar.go:42:6: undefined: parseConfig\nFAIL\tgithub.com/example/app/internal/foo [build failed]\n##[error]Process completed with exit code 1."}},
		{"runner-lost outage", FailedCheckLog{CheckName: "build", Logs: "##[error]The self-hosted runner: ci-runner-7 lost communication with the server. Verify the machine is running and has a healthy network connection."}},
		{"billing refusal", FailedCheckLog{CheckName: "lint", AnnotationText: "The job was not started because recent account payments have failed or your spending limit needs to be increased. Please check the 'Billing & plans' section in your settings."}},
		{"empty log", FailedCheckLog{CheckName: "e2e"}},
	}
	var buf bytes.Buffer
	c := newTestCIFailureClassifier(typesafe.NewClient(typesafe.Config{}, key, nil), true, bufLogger(&buf))
	logs := make([]FailedCheckLog, len(checks))
	for i, x := range checks {
		logs[i] = x.chk
	}
	per, stats := c.classify(context.Background(), logs, "smoke/test")
	for i, x := range checks {
		t.Logf("check %d (%s): regex=%s", i+1, x.label, per[i].class)
	}
	t.Logf("stats: %+v", stats)
	t.Log("\n" + buf.String())
	if stats.Errors != 0 {
		t.Errorf("live smoke had %d errors", stats.Errors)
	}
}
