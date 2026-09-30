package executor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/typesafe"
)

// Base-presence span classes Jev chooses between (TASK-506/GH-5543).
const (
	spanExistingPrerequisite = "existing_prerequisite"
	spanToBeCreated          = "to_be_created"
	spanNotARepoFile         = "not_a_repo_file"
)

const (
	// maxBasePresenceSpanChars caps the quoted span sent to the API.
	maxBasePresenceSpanChars = 200
	// maxBasePresenceContextChars caps the one sentence of surrounding body
	// text sent with each span.
	maxBasePresenceContextChars = 300

	// basePresenceInstructionsFmt takes the span index (twice) and the redacted,
	// capped span and context sentence. The index, span and sentence must
	// appear in every instruction: a shared constant makes all span_<i>
	// questions byte-identical over the same state, so Jev returns one
	// distribution for every span (pitfall
	// jev-identical-questions-return-identical-answers).
	basePresenceInstructionsFmt = "Classify span %s (state.spans[%q]): %q. Context sentence: %q. " +
		"Is this span a file that already exists in the repository and must be on the default branch before the task starts, " +
		"a file the task will create, or not a repository file at all?"

	basePresenceRubricExisting = "A repository file path the task depends on, which already exists on the default branch."
	basePresenceRubricCreate   = "A repository file path the task itself is asked to create or add; it does not exist yet."
	basePresenceRubricNotFile  = "Not a file in this repository: a git range or branch name, a home-directory or absolute system path, " +
		"a Go type or symbol, a URL-like string, or a path in another repository."
)

// BasePresenceClassifier filters the path spans ExtractReferencedPaths
// extracted before the base-presence probe. The regex extractor stays the
// floor: a classifier may only remove spans, and any failure keeps them all.
type BasePresenceClassifier interface {
	// Classify returns the paths to probe and stats for the per-task log line.
	Classify(ctx context.Context, body string, paths []string) ([]string, BasePresenceClassifyStats)
}

// BasePresenceClassifyStats reports what a Classify call did, for the
// per-task log line that drives the shadow-to-live flip decision.
type BasePresenceClassifyStats struct {
	// Spans is the number of extracted spans classified.
	Spans int
	// Shadow is true when Jev ran in shadow mode (all spans kept).
	Shadow bool
	// Agreed counts spans Jev classified as an existing prerequisite with
	// enough confidence (the regex verdict).
	Agreed int
	// WouldSkip counts spans the live path removes from the probe list; in
	// shadow mode it is the would-be removal count.
	WouldSkip int
	// LowConfidence counts spans Jev disagreed on below min_confidence.
	LowConfidence int
	// Errors is 1 for a failed Ask call, plus one per span whose answer was
	// missing or invalid.
	Errors  int
	Latency time.Duration
}

// jevBasePresenceClassifier asks Jev one choice question per span, in a single
// Ask call per task.
type jevBasePresenceClassifier struct {
	asker         typesafe.Asker
	minConfidence float64
	shadow        bool
	log           *slog.Logger
}

// newBasePresenceClassifier returns nil (regex only, no client constructed)
// unless the provider is "jev" with TYPESAFE_API_KEY present. A missing key
// warns once here and stays regex.
func newBasePresenceClassifier(cfg *BackendConfig, log *slog.Logger) BasePresenceClassifier {
	if cfg == nil || cfg.BasePresence == nil || cfg.BasePresence.Classifier == nil {
		return nil
	}
	bp := cfg.BasePresence
	if !strings.EqualFold(strings.TrimSpace(bp.Classifier.Provider), AcceptanceClassifierJev) {
		return nil
	}

	key, source := typesafe.APIKeyFromEnv()
	if key == "" {
		if log != nil {
			log.Warn("Base-presence classifier provider is jev but no API key is set; using regex only",
				slog.String("env", typesafe.APIKeyEnv))
		}
		return nil
	}

	var tsCfg typesafe.Config
	if cfg.TypeSafe != nil {
		tsCfg = *cfg.TypeSafe
	}
	if log != nil {
		log.Info("Base-presence classifier using Jev",
			slog.String("auth_source", source),
			slog.Bool("shadow", bp.EffectiveShadow()),
			slog.Float64("min_confidence", bp.EffectiveMinConfidence()),
		)
	}
	return &jevBasePresenceClassifier{
		asker:         newTypeSafeClient(tsCfg, key, log),
		minConfidence: bp.EffectiveMinConfidence(),
		shadow:        bp.EffectiveShadow(),
		log:           log,
	}
}

func (c *jevBasePresenceClassifier) Classify(ctx context.Context, body string, paths []string) ([]string, BasePresenceClassifyStats) {
	start := time.Now()
	n := len(paths)
	stats := BasePresenceClassifyStats{Spans: n, Shadow: c.shadow}
	if n == 0 {
		return paths, stats
	}

	// State carries only the redacted, capped spans; the context sentence
	// travels inside each question's instruction. Never the whole body.
	spans := make(map[string]string, n)
	questions := make(map[string]typesafe.Question, n)
	for i, p := range paths {
		idx := fmt.Sprintf("%d", i+1)
		span := typesafe.RedactAndCap(p, maxBasePresenceSpanChars)
		sentence := spanContextSentence(body, p, maxBasePresenceContextChars)
		spans[idx] = span
		questions["span_"+idx] = typesafe.ChoiceQuestion(
			fmt.Sprintf(basePresenceInstructionsFmt, idx, idx, span, sentence),
			map[string]any{
				spanExistingPrerequisite: basePresenceRubricExisting,
				spanToBeCreated:          basePresenceRubricCreate,
				spanNotARepoFile:         basePresenceRubricNotFile,
			})
	}

	answers, err := c.asker.Ask(ctx, map[string]any{"spans": spans}, questions)
	stats.Latency = time.Since(start)
	if err != nil {
		if c.log != nil {
			c.log.Warn("Base-presence classifier Ask failed; keeping all spans",
				slog.String("error", err.Error()))
		}
		stats.Errors = 1
		return paths, stats
	}

	kept := make([]string, 0, n)
	for i, p := range paths {
		ans := validBasePresenceAnswer(answers, fmt.Sprintf("span_%d", i+1))
		_, reason := typesafe.Resolve(spanExistingPrerequisite, ans, c.minConfidence, false)
		switch reason {
		case typesafe.ReasonAgreed:
			stats.Agreed++
		case typesafe.ReasonLowConfidence:
			stats.LowConfidence++
		case typesafe.ReasonError:
			stats.Errors++
		case typesafe.ReasonOverrode:
			stats.WouldSkip++
			if !c.shadow {
				continue
			}
		}
		kept = append(kept, p)
	}
	stats.Latency = time.Since(start)
	return kept, stats
}

// validBasePresenceAnswer returns the answer for key, or nil when it is
// missing or its choice is not one of the three span classes.
func validBasePresenceAnswer(answers typesafe.Answers, key string) *typesafe.Answer {
	a, ok := answers.Answers[key]
	if !ok {
		return nil
	}
	switch a.Choice {
	case spanExistingPrerequisite, spanToBeCreated, spanNotARepoFile:
		return &a
	}
	return nil
}

// spanContextSentence returns the sentence of body containing the first
// backticked occurrence of path (as ExtractReferencedPaths resolves it),
// redacted and windowed around the span to at most maxChars characters. It
// returns "" when the span cannot be located.
func spanContextSentence(body, path string, maxChars int) string {
	for _, m := range backtickSpanRe.FindAllStringSubmatchIndex(body, -1) {
		cand := pathLineRefSuffixRe.ReplaceAllString(strings.TrimSpace(body[m[2]:m[3]]), "")
		if cand != path {
			continue
		}
		start := sentenceStart(body, m[0])
		end := sentenceEnd(body, m[1])
		sentence := strings.TrimSpace(body[start:end])
		// Redact before windowing so a secret cut by the window cannot survive.
		sentence = typesafe.RedactAndCap(sentence, len([]rune(sentence))+1)
		return windowAround(sentence, path, maxChars)
	}
	return ""
}

func isSentenceEnd(b byte) bool { return b == '.' || b == '!' || b == '?' }

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// sentenceStart scans back from pos to just after the previous newline or
// sentence terminator followed by whitespace.
func sentenceStart(body string, pos int) int {
	for i := pos - 1; i >= 0; i-- {
		if body[i] == '\n' {
			return i + 1
		}
		if isSpace(body[i]) && i > 0 && isSentenceEnd(body[i-1]) {
			return i + 1
		}
	}
	return 0
}

// sentenceEnd scans forward from pos to the next newline or sentence
// terminator followed by whitespace or end of text.
func sentenceEnd(body string, pos int) int {
	for i := pos; i < len(body); i++ {
		if body[i] == '\n' {
			return i
		}
		if isSentenceEnd(body[i]) && (i+1 == len(body) || isSpace(body[i+1])) {
			return i + 1
		}
	}
	return len(body)
}

// windowAround returns s unchanged when it fits in maxChars runes; otherwise a
// maxChars-rune window centred on the first occurrence of needle.
func windowAround(s, needle string, maxChars int) string {
	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}
	centre := 0
	if idx := strings.Index(s, needle); idx >= 0 {
		centre = len([]rune(s[:idx]))
	}
	lo := centre - maxChars/2
	if lo < 0 {
		lo = 0
	}
	hi := lo + maxChars
	if hi > len(r) {
		hi = len(r)
		lo = hi - maxChars
	}
	return string(r[lo:hi])
}

// classifyBasePresencePaths runs the optional classifier over the extracted
// paths and logs one per-task info line. Nil-safe: with no classifier the paths
// are returned untouched and nothing is logged.
func (r *Runner) classifyBasePresencePaths(ctx context.Context, taskID, body string, paths []string) []string {
	if r == nil || r.basePresenceClassifier == nil || len(paths) == 0 {
		return paths
	}
	kept, stats := r.basePresenceClassifier.Classify(ctx, body, paths)
	if r.log != nil {
		r.log.Info("Base-presence classifier",
			slog.String("task_id", taskID),
			slog.Bool("shadow", stats.Shadow),
			slog.Int("spans", stats.Spans),
			slog.Int("agreed", stats.Agreed),
			slog.Int("would_skip", stats.WouldSkip),
			slog.Int("low_confidence", stats.LowConfidence),
			slog.Int("errors", stats.Errors),
			slog.Int64("latency_ms", stats.Latency.Milliseconds()),
		)
	}
	return kept
}
