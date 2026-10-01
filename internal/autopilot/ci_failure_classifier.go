package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/typesafe"
)

// TASK-507 (gate 3 of the Jev roadmap): an optional TypeSafe (Jev) classifier
// for the CI failure class, layered behind the regex floor in failure_class.go.
// Incident history: infra outages read as code closed correct PRs and spawned
// fix issues (#4526, #4533, #4591, #4779, #4791). Jev may only move a check the
// regex called `code` toward `infra` / `infra_billing` (the retry direction);
// every other disagreement, any low confidence, invalid answers and Ask errors
// keep the regex verdict.

const (
	// CIFailureClassifierRegex is the deterministic, offline classifier.
	CIFailureClassifierRegex = "regex"
	// CIFailureClassifierJev is the optional TypeSafe Jev classifier.
	CIFailureClassifierJev = "jev"

	defaultCIFailureClassifierMinConfidence = 0.8

	// ciFailureLogTailChars is how much of a job log's tail is sent: failures
	// sit at the end of a log, so the head (runner setup) is never sent.
	ciFailureLogTailChars = 1500
	// ciFailureAnnotationChars caps the check run's own annotation text.
	ciFailureAnnotationChars = 1500
	// ciFailureCheckNameChars caps the check name quoted in an instruction.
	ciFailureCheckNameChars = 120
	// ciFailureRedactWindow is how much of the log tail is redacted before the
	// final tail cut. Redacting a wider window than is sent means a secret that
	// straddles the cut is matched whole and only a harmless fragment of it, if
	// anything, could survive — and that fragment is cut off by the final tail.
	ciFailureRedactWindow = 8000
	// ciFailureDebugHeadChars caps the excerpt head in the per-check debug line.
	ciFailureDebugHeadChars = 200

	// ciFailureJevSource is the verdict source when Jev moved at least one
	// check, so downstream evidence names the classifier honestly.
	ciFailureJevSource = "classifyPRFailure+jev"
)

// Question text. The instruction is built per check from ciFailureInstructionsFmt
// with the check index, check name and excerpt: a shared constant would make
// every check's question byte-identical over the same state, and Jev returns
// one distribution for identical questions (pitfall
// jev-identical-questions-return-identical-answers).
//
// Rubrics are one-sentence restatements of the comments on the FailureClass
// constants in failure_class.go.
const (
	ciFailureInstructionsFmt = "Classify failed CI check %s (state.checks[%q], named %q). Excerpt: %q. " +
		"Did this check fail because of the pull request's own code, or because of CI infrastructure?"
	ciFailureRubricCode  = "A genuine code, test or lint failure: the job ran the repository's code and it failed (compiler error, failing test, lint finding)."
	ciFailureRubricInfra = "A CI infrastructure outage unrelated to the code: runner death or lost communication, action-download rate limiting, " +
		"transient 5xx from the Actions backend; safe to retry."
	ciFailureRubricBilling = "GitHub Actions refused to start the job because of an organisation billing problem (payment failure or spending limit reached)."
	ciFailureRubricUnknown = "The excerpt holds no usable evidence either way (empty, truncated or unrelated output)."
)

// CIFailureConfig configures CI failure classification.
//
//	orchestrator:
//	  autopilot:
//	    ci_failure:
//	      classifier:
//	        provider: regex   # regex (default) | jev
//	        min_confidence: 0.8
//	        shadow: true
type CIFailureConfig struct {
	// Classifier optionally layers a TypeSafe (Jev) classifier on top of the
	// regex classifier, which stays the floor and fallback.
	Classifier *CIFailureClassifierConfig `yaml:"classifier,omitempty"`
}

// CIFailureClassifierConfig mirrors the executor classifier blocks: same keys,
// same defaults, same TYPESAFE_API_KEY requirement.
type CIFailureClassifierConfig struct {
	// Provider is "regex" (default) or "jev".
	Provider string `yaml:"provider,omitempty"`
	// MinConfidence is the confidence (0..1) a Jev verdict needs to move a
	// regex verdict. Default 0.8; out-of-range falls back.
	MinConfidence *float64 `yaml:"min_confidence,omitempty"`
	// Shadow, when true (default), records what Jev would have decided
	// without changing behaviour.
	Shadow *bool `yaml:"shadow,omitempty"`
}

// EffectiveClassifierProvider returns "regex" unless provider is "jev" and
// TYPESAFE_API_KEY is non-empty. Safe on nil receivers.
func (c *CIFailureConfig) EffectiveClassifierProvider() string {
	if c == nil || c.Classifier == nil {
		return CIFailureClassifierRegex
	}
	if strings.EqualFold(strings.TrimSpace(c.Classifier.Provider), CIFailureClassifierJev) {
		if key, _ := typesafe.APIKeyFromEnv(); key != "" {
			return CIFailureClassifierJev
		}
	}
	return CIFailureClassifierRegex
}

// EffectiveMinConfidence returns the configured minimum confidence, or 0.8
// when unset or outside 0..1. Safe on nil receivers.
func (c *CIFailureConfig) EffectiveMinConfidence() float64 {
	if c == nil || c.Classifier == nil || c.Classifier.MinConfidence == nil {
		return defaultCIFailureClassifierMinConfidence
	}
	v := *c.Classifier.MinConfidence
	if !(v >= 0 && v <= 1) { // also rejects NaN
		return defaultCIFailureClassifierMinConfidence
	}
	return v
}

// EffectiveShadow returns whether the classifier runs in shadow mode; default
// true. Safe on nil receivers.
func (c *CIFailureConfig) EffectiveShadow() bool {
	if c == nil || c.Classifier == nil || c.Classifier.Shadow == nil {
		return true
	}
	return *c.Classifier.Shadow
}

// newCIFailureTypeSafeClient builds the System One client. It is a variable so
// tests can swap it to count constructions (the default configuration must
// never build a client).
var newCIFailureTypeSafeClient = func(cfg typesafe.Config, key string, log *slog.Logger) typesafe.Asker {
	return typesafe.NewClient(cfg, key, log)
}

// newCIFailureClassifier returns nil (regex only) unless provider is "jev" with
// TYPESAFE_API_KEY present. Jev configured without a key warns once per call
// and stays regex; the default configuration never constructs a client.
func newCIFailureClassifier(cfg *Config, log *slog.Logger) *ciFailureClassifier {
	if cfg == nil || cfg.CIFailure == nil || cfg.CIFailure.Classifier == nil ||
		!strings.EqualFold(strings.TrimSpace(cfg.CIFailure.Classifier.Provider), CIFailureClassifierJev) {
		return nil
	}
	key, source := typesafe.APIKeyFromEnv()
	if key == "" {
		if log != nil {
			log.Warn("CI failure classifier provider is jev but no API key is set; using regex classifier",
				slog.String("env", typesafe.APIKeyEnv))
		}
		return nil
	}

	var tsCfg typesafe.Config
	if cfg.TypeSafe != nil {
		tsCfg = *cfg.TypeSafe
	}
	if log != nil {
		log.Info("CI failure classifier using Jev",
			slog.String("auth_source", source),
			slog.Bool("shadow", cfg.CIFailure.EffectiveShadow()),
			slog.Float64("min_confidence", cfg.CIFailure.EffectiveMinConfidence()),
		)
	}
	return &ciFailureClassifier{
		asker:         newCIFailureTypeSafeClient(tsCfg, key, log),
		minConfidence: cfg.CIFailure.EffectiveMinConfidence(),
		shadow:        cfg.CIFailure.EffectiveShadow(),
		log:           log,
	}
}

// ciFailureClassifier layers a Jev classifier on top of the regex per-check
// verdicts. One Ask call per classification, one choice question per failed
// check.
type ciFailureClassifier struct {
	asker         typesafe.Asker
	minConfidence float64
	shadow        bool
	log           *slog.Logger
}

// ciFailureStats reports what one classification did, for the info line that
// drives the shadow-to-live flip decision. Counters are tallied from what the
// live path would do, so in shadow mode WouldOverride is the flip signal.
type ciFailureStats struct {
	Checks        int
	Agreed        int
	WouldOverride int
	LowConfidence int
	// Vetoed counts confident disagreements the retry-only rule refuses: any
	// `unknown`, `infra` -> `code`, and moves between infra-family classes.
	Vetoed int
	// Errors is 1 for a failed Ask call, plus one per check whose answer was
	// missing or invalid.
	Errors  int
	Latency time.Duration
	// Overridden is how many checks the live path actually moved (always 0 in
	// shadow mode).
	Overridden int
}

// classify returns the per-check verdicts: the regex verdicts, with a regex
// `code` check moved to Jev's infra-family choice only in live mode at or above
// minConfidence.
func (c *ciFailureClassifier) classify(ctx context.Context, checks []FailedCheckLog, scope string) ([]checkVerdict, ciFailureStats) {
	start := time.Now()
	per := regexCheckVerdicts(checks)
	n := len(checks)
	stats := ciFailureStats{Checks: n}
	if n == 0 {
		return per, stats
	}
	regexPer := make([]checkVerdict, n)
	copy(regexPer, per)

	excerpts := make(map[string]string, n)
	questions := make(map[string]typesafe.Question, n)
	for i, chk := range checks {
		idx := strconv.Itoa(i + 1)
		name := typesafe.RedactAndCap(chk.CheckName, ciFailureCheckNameChars)
		excerpt := ciFailureExcerpt(chk)
		excerpts[idx] = excerpt
		questions["check_"+idx] = typesafe.ChoiceQuestion(
			fmt.Sprintf(ciFailureInstructionsFmt, idx, idx, name, excerpt),
			map[string]any{
				string(FailureClassCode):         ciFailureRubricCode,
				string(FailureClassInfra):        ciFailureRubricInfra,
				string(FailureClassInfraBilling): ciFailureRubricBilling,
				string(FailureClassUnknown):      ciFailureRubricUnknown,
			})
	}

	answers, err := c.asker.Ask(ctx, map[string]any{"checks": excerpts}, questions)
	stats.Latency = time.Since(start)
	if err != nil {
		if c.log != nil {
			c.log.Warn("CI failure classifier Ask failed; keeping regex classification",
				slog.String("scope", scope), slog.String("error", err.Error()))
		}
		stats.Errors = 1
		c.logStats(scope, stats)
		return per, stats
	}

	for i, chk := range checks {
		idx := strconv.Itoa(i + 1)
		ans := validCIFailureAnswer(answers, "check_"+idx)
		regex := regexPer[i].class
		_, reason := typesafe.Resolve(string(regex), ans, c.minConfidence, false)
		outcome := string(reason)
		switch reason {
		case typesafe.ReasonError:
			stats.Errors++
		case typesafe.ReasonLowConfidence:
			stats.LowConfidence++
		case typesafe.ReasonAgreed:
			stats.Agreed++
		case typesafe.ReasonOverrode:
			// Retry direction only: a regex code verdict may move to infra or
			// infra_billing. Everything else keeps the regex verdict.
			choice := FailureClass(ans.Choice)
			if regex == FailureClassCode && choice.IsInfra() {
				stats.WouldOverride++
				outcome = "would_override"
				if !c.shadow {
					per[i] = checkVerdict{class: choice, signal: signalJev}
					stats.Overridden++
					outcome = "overrode"
				}
			} else {
				stats.Vetoed++
				outcome = "vetoed"
			}
		}
		if c.log != nil {
			jevChoice, conf := "", 0.0
			if ans != nil {
				jevChoice, conf = ans.Choice, ans.Confidence
			}
			c.log.Debug("CI failure classifier check",
				slog.String("scope", scope),
				slog.String("check", chk.CheckName),
				slog.String("regex_class", string(regex)),
				slog.String("excerpt_head", headRunes(excerpts[idx], ciFailureDebugHeadChars)),
				slog.String("jev_choice", jevChoice),
				slog.Float64("confidence", conf),
				slog.String("outcome", outcome),
				slog.String("reason", string(reason)),
			)
		}
	}
	stats.Latency = time.Since(start)
	c.logStats(scope, stats)
	return per, stats
}

func (c *ciFailureClassifier) logStats(scope string, s ciFailureStats) {
	if c.log == nil {
		return
	}
	c.log.Info("CI failure classifier",
		slog.String("scope", scope),
		slog.Bool("shadow", c.shadow),
		slog.Int("checks", s.Checks),
		slog.Int("agreed", s.Agreed),
		slog.Int("would_override", s.WouldOverride),
		slog.Int("low_confidence", s.LowConfidence),
		slog.Int("vetoed", s.Vetoed),
		slog.Int("errors", s.Errors),
		slog.Int64("latency_ms", s.Latency.Milliseconds()),
	)
}

// validCIFailureAnswer returns the answer for key, or nil when it is missing or
// its choice is not one of the four classes.
func validCIFailureAnswer(answers typesafe.Answers, key string) *typesafe.Answer {
	a, ok := answers.Answers[key]
	if !ok {
		return nil
	}
	switch FailureClass(a.Choice) {
	case FailureClassCode, FailureClassInfra, FailureClassInfraBilling, FailureClassUnknown:
		return &a
	}
	return nil
}

// ciFailureExcerpt builds the text Jev judges for one check: the redacted,
// capped annotation text plus the redacted last ciFailureLogTailChars of the
// job log. Never the full log. Empty parts read "(none)" so an empty log is
// visibly empty rather than a malformed question.
func ciFailureExcerpt(chk FailedCheckLog) string {
	annotation := typesafe.RedactAndCap(strings.TrimSpace(chk.AnnotationText), ciFailureAnnotationChars)
	window := tailRunes(strings.TrimSpace(chk.Logs), ciFailureRedactWindow)
	tail := tailRunes(typesafe.RedactAndCap(window, math.MaxInt32), ciFailureLogTailChars)
	if annotation == "" {
		annotation = "(none)"
	}
	if tail == "" {
		tail = "(none)"
	}
	return "annotation: " + annotation + "\nlog tail: " + tail
}

// tailRunes returns the last n runes of s.
func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// headRunes returns the first n runes of s.
func headRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ciFailureClassification is the controller-facing result of classifying one
// failed run.
type ciFailureClassification struct {
	Class    FailureClass
	Verdict  Verdict
	PerCheck []checkVerdict
}

// classifyCIFailure is the one seam both CI-failure call sites (pre-merge
// handleCIFailed, post-merge) classify through. With no Jev classifier
// configured it is exactly classifyPRFailure + newCIFailureVerdict.
func (c *Controller) classifyCIFailure(ctx context.Context, checks []FailedCheckLog) ciFailureClassification {
	scope := c.repoKey()
	if len(checks) == 0 {
		return ciFailureClassification{
			Class:   FailureClassUnknown,
			Verdict: NewUnknownVerdict("classifyPRFailure", scope),
		}
	}
	per := regexCheckVerdicts(checks)
	source := "classifyPRFailure"
	if c.ciFailureClassifier != nil {
		var stats ciFailureStats
		per, stats = c.ciFailureClassifier.classify(ctx, checks, scope)
		if stats.Overridden > 0 {
			source = ciFailureJevSource
		}
	}
	class := aggregateCheckVerdicts(per)
	return ciFailureClassification{
		Class:    class,
		Verdict:  newCIFailureVerdictFor(class, checks, per, source, scope),
		PerCheck: per,
	}
}
