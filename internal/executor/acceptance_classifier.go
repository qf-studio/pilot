package executor

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/typesafe"
)

// AcceptanceClassifier classifies a task's acceptance-checklist items. The
// regex classifier (ParseAcceptanceItems) is always the floor; an optional
// model classifier (acceptance_classifier_jev.go) may override it per item.
// A classifier only ever changes Kind and MutationTarget/Commands — the runner,
// allowlist, confinement and renderer are untouched.
type AcceptanceClassifier interface {
	Classify(ctx context.Context, criteria []string) ([]AcceptanceItem, AcceptanceClassifyStats)
}

// AcceptanceClassifyStats reports what a Classify call did, for the per-task
// log line that drives the shadow-to-live flip decision.
type AcceptanceClassifyStats struct {
	// Classifier names the classifier that ran ("regex" or "jev").
	Classifier string
	// Items is the number of classified checklist items.
	Items int
	// Shadow is true when the model ran in shadow mode (regex verdicts kept).
	Shadow bool
	// Reasons is the reason per item (same order as the returned items) for
	// what actually happened; in shadow mode a differing confident verdict
	// shows as ReasonShadow.
	Reasons []typesafe.Reason
	// WouldBe is the reason per item the live (non-shadow) path produces. It
	// equals Reasons outside shadow mode.
	WouldBe []typesafe.Reason
	// Counters below are tallied from WouldBe, so in shadow mode Overrode is
	// the number of would-be overrides — the flip signal.
	RegexOnly     int
	Agreed        int
	Overrode      int
	LowConfidence int
	// Errors counts failures: 1 for a failed Ask call, plus one per item
	// whose answer was missing or invalid.
	Errors  int
	Latency time.Duration
}

// tally fills the per-reason counters from WouldBe.
func (s *AcceptanceClassifyStats) tally() {
	s.RegexOnly, s.Agreed, s.Overrode, s.LowConfidence = 0, 0, 0, 0
	for _, r := range s.WouldBe {
		switch r {
		case typesafe.ReasonRegexOnly:
			s.RegexOnly++
		case typesafe.ReasonAgreed:
			s.Agreed++
		case typesafe.ReasonOverrode:
			s.Overrode++
		case typesafe.ReasonLowConfidence:
			s.LowConfidence++
		}
	}
}

// regexAcceptanceClassifier wraps ParseAcceptanceItems; every item is reported
// as ReasonRegexOnly.
type regexAcceptanceClassifier struct{}

func (regexAcceptanceClassifier) Classify(_ context.Context, criteria []string) ([]AcceptanceItem, AcceptanceClassifyStats) {
	items := ParseAcceptanceItems(criteria)
	stats := AcceptanceClassifyStats{
		Classifier: AcceptanceClassifierRegex,
		Items:      len(items),
		Reasons:    make([]typesafe.Reason, len(items)),
		WouldBe:    make([]typesafe.Reason, len(items)),
	}
	for i := range items {
		stats.Reasons[i] = typesafe.ReasonRegexOnly
		stats.WouldBe[i] = typesafe.ReasonRegexOnly
	}
	stats.tally()
	return items, stats
}

// newTypeSafeClient builds the System One client. It is a package-level
// variable so tests can swap it to count constructions (the default
// configuration must never build a client).
var newTypeSafeClient = func(cfg typesafe.Config, key string, log *slog.Logger) typesafe.Asker {
	return typesafe.NewClient(cfg, key, log)
}

// newAcceptanceClassifier returns the regex classifier unless the provider is
// "jev" with a key present (EffectiveClassifierProvider), in which case it
// returns the Jev classifier built on a fresh TypeSafe client. It also logs, once
// per call, a warning when jev is configured without a key and an info line
// naming the key source (never the key) when it resolves.
func newAcceptanceClassifier(cfg *BackendConfig, log *slog.Logger) AcceptanceClassifier {
	if cfg == nil || cfg.AcceptanceEvidence == nil {
		return regexAcceptanceClassifier{}
	}
	ae := cfg.AcceptanceEvidence
	if ae.Classifier == nil || !strings.EqualFold(strings.TrimSpace(ae.Classifier.Provider), AcceptanceClassifierJev) {
		return regexAcceptanceClassifier{}
	}

	key, source := typesafe.APIKeyFromEnv()
	if key == "" {
		if log != nil {
			log.Warn("Acceptance classifier provider is jev but no API key is set; using regex classifier",
				slog.String("env", typesafe.APIKeyEnv))
		}
		return regexAcceptanceClassifier{}
	}
	if ae.EffectiveClassifierProvider() != AcceptanceClassifierJev {
		return regexAcceptanceClassifier{}
	}

	var tsCfg typesafe.Config
	if cfg.TypeSafe != nil {
		tsCfg = *cfg.TypeSafe
	}
	if log != nil {
		log.Info("Acceptance classifier using Jev",
			slog.String("auth_source", source),
			slog.Bool("shadow", ae.EffectiveShadow()),
			slog.Float64("min_confidence", ae.EffectiveMinConfidence()),
		)
	}
	return &jevAcceptanceClassifier{
		asker:         newTypeSafeClient(tsCfg, key, log),
		minConfidence: ae.EffectiveMinConfidence(),
		shadow:        ae.EffectiveShadow(),
		log:           log,
	}
}
