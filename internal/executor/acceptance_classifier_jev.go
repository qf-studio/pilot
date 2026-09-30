package executor

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/typesafe"
)

// maxAcceptanceItemChars caps each checklist item sent to the API.
const maxAcceptanceItemChars = 600

// targetNone is the target_<n> option meaning "no listed test is the one that
// must fail".
const targetNone = "none"

// goTestIdentRe finds Go test identifiers offered as target_<n> options.
var goTestIdentRe = regexp.MustCompile(`\bTest[A-Z]\w*`)

// Question rubrics. Source: the stream demo script jev-classify-demo.sh
// (b4 paste_output 1.00, b5 mutation 1.00, b2 other 0.42 vs mutation 0.39).
const (
	kindInstructions = "Classify the checklist item in state.items at this question's index: " +
		"does it ask for evidence of a command run, assert a named test fails after a described change, or neither?"
	kindRubricMutation = "The item names a change to make and asserts that a named test FAILS as a result. " +
		"Items saying a test must NOT fail are not this."
	kindRubricPasteOutput = "The item asks to run a command and show its output, or names a runnable command that must pass."
	kindRubricOther       = "Everything else."
	targetInstructions    = "Which test named in the checklist item at this index is the one that must FAIL after the described change?"
	targetRubricNone      = "None of these is the test that must fail."
)

// jevAcceptanceClassifier layers a TypeSafe (Jev) kind classifier on top of the
// regex floor. One Ask call per task; any failure keeps the regex items.
type jevAcceptanceClassifier struct {
	asker         typesafe.Asker
	minConfidence float64
	shadow        bool
	log           *slog.Logger
}

func (c *jevAcceptanceClassifier) Classify(ctx context.Context, criteria []string) ([]AcceptanceItem, AcceptanceClassifyStats) {
	start := time.Now()
	regexItems := ParseAcceptanceItems(criteria)
	n := len(regexItems)
	stats := AcceptanceClassifyStats{
		Classifier: AcceptanceClassifierJev,
		Items:      n,
		Shadow:     c.shadow,
		Reasons:    make([]typesafe.Reason, n),
		WouldBe:    make([]typesafe.Reason, n),
	}
	if n == 0 {
		return regexItems, stats
	}

	// State carries only redacted, capped checklist text.
	texts := make(map[string]string, n)
	questions := make(map[string]typesafe.Question, 2*n)
	targets := make([][]string, n)
	for i, it := range regexItems {
		idx := fmt.Sprintf("%d", i+1)
		text := typesafe.RedactAndCap(it.Text, maxAcceptanceItemChars)
		texts[idx] = text

		questions["kind_"+idx] = typesafe.ChoiceQuestion(kindInstructions, map[string]any{
			string(AcceptanceItemMutation):    kindRubricMutation,
			string(AcceptanceItemPasteOutput): kindRubricPasteOutput,
			string(AcceptanceItemOther):       kindRubricOther,
		})

		// Options come from the redacted text, so every offered token is a
		// span of the original item text and can reach a shell command only
		// as such.
		if toks := uniqueStrings(goTestIdentRe.FindAllString(text, -1)); len(toks) > 0 {
			targets[i] = toks
			opts := make(map[string]any, len(toks)+1)
			for _, t := range toks {
				opts[t] = nil
			}
			opts[targetNone] = targetRubricNone
			questions["target_"+idx] = typesafe.ChoiceQuestion(targetInstructions, opts)
		}
	}

	answers, err := c.asker.Ask(ctx, map[string]any{"items": texts}, questions)
	stats.Latency = time.Since(start)
	if err != nil {
		if c.log != nil {
			c.log.Warn("Acceptance classifier Ask failed; keeping regex classification",
				slog.String("error", err.Error()))
		}
		for i := range regexItems {
			stats.Reasons[i] = typesafe.ReasonError
			stats.WouldBe[i] = typesafe.ReasonError
		}
		stats.Errors = 1
		return regexItems, stats
	}

	out := make([]AcceptanceItem, n)
	for i, rx := range regexItems {
		idx := fmt.Sprintf("%d", i+1)
		kindAns := validKindAnswer(answers, "kind_"+idx)
		item, reason, wouldBe := c.mergeItem(rx, kindAns, answers, "target_"+idx, targets[i])
		out[i] = item
		stats.Reasons[i] = reason
		stats.WouldBe[i] = wouldBe
		if reason == typesafe.ReasonError {
			stats.Errors++
		}
	}
	stats.tally()
	stats.Latency = time.Since(start)
	return out, stats
}

// mergeItem resolves one item. reason is what happened; wouldBe is what the
// live (non-shadow) path produces.
func (c *jevAcceptanceClassifier) mergeItem(rx AcceptanceItem, kindAns *typesafe.Answer, answers typesafe.Answers, targetKey string, targetOpts []string) (AcceptanceItem, typesafe.Reason, typesafe.Reason) {
	verdict, wouldBe := typesafe.Resolve(string(rx.Kind), kindAns, c.minConfidence, false)
	candidate := rx
	if wouldBe == typesafe.ReasonOverrode {
		var ok bool
		candidate, ok = applyKindOverride(rx, AcceptanceItemKind(verdict), answers, targetKey, targetOpts, c.minConfidence)
		if !ok {
			// Decision: a vetoed override keeps the regex item whole (mutation
			// precedence, #5486; Jev never invents a command or a test name)
			// and is counted as low_confidence.
			return rx, typesafe.ReasonLowConfidence, typesafe.ReasonLowConfidence
		}
	}
	if c.shadow && wouldBe == typesafe.ReasonOverrode {
		return rx, typesafe.ReasonShadow, wouldBe
	}
	return candidate, wouldBe, wouldBe
}

// applyKindOverride builds the item for a Jev verdict that differs from the
// regex kind. ok is false when the override must be vetoed.
func applyKindOverride(rx AcceptanceItem, kind AcceptanceItemKind, answers typesafe.Answers, targetKey string, targetOpts []string, minConfidence float64) (AcceptanceItem, bool) {
	item := AcceptanceItem{Text: rx.Text, Kind: kind}
	switch kind {
	case AcceptanceItemMutation:
		ans, ok := answers.Answers[targetKey]
		if !ok || ans.Confidence < minConfidence || ans.Choice == targetNone || !containsString(targetOpts, ans.Choice) {
			return rx, false
		}
		item.MutationDescription = rx.Text
		if m := mutationArrowRe.FindStringSubmatch(rx.Text); m != nil {
			item.MutationDescription = strings.TrimSpace(m[1])
		}
		item.MutationTarget = ans.Choice
		return item, true
	case AcceptanceItemPasteOutput:
		// A regex mutation shape wins over paste-output (#5486), and the
		// command must come from backtick spans already in the text.
		if rx.Kind == AcceptanceItemMutation {
			return rx, false
		}
		cmds := rx.Commands
		if len(cmds) == 0 {
			cmds = extractInlineCommands(rx.Text)
		}
		if len(cmds) == 0 {
			return rx, false
		}
		item.Commands = cmds
		return item, true
	case AcceptanceItemOther:
		return item, true
	}
	return rx, false
}

// validKindAnswer returns the answer for key, or nil when it is missing or its
// choice is not one of the three kinds.
func validKindAnswer(answers typesafe.Answers, key string) *typesafe.Answer {
	a, ok := answers.Answers[key]
	if !ok {
		return nil
	}
	switch AcceptanceItemKind(a.Choice) {
	case AcceptanceItemMutation, AcceptanceItemPasteOutput, AcceptanceItemOther:
		return &a
	}
	return nil
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
