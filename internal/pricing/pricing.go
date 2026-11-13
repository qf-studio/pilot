// Package pricing is the one table that turns model tokens into US dollars.
// The executor estimate (executions.estimated_cost_usd), the dashboard cost
// card and the replay recorder all price through it (GH-6355).
//
// It is a leaf package: it imports nothing from this repository.
//
// Rates are USD per 1M tokens, Anthropic first-party list
// (https://platform.claude.com/docs/en/about-claude/pricing, checked
// 2026-11-03). The customer flat rate in internal/memory/metering.go is a
// different thing and does not live here.
package pricing

import "strings"

const (
	// cacheReadShare is the cache read rate, as a share of the input rate,
	// for a model whose list gives no explicit cache read rate.
	cacheReadShare = 0.10
	// cacheWrite5mFactor and cacheWrite1hFactor are the cache write rates as
	// multiples of the input rate, for the 5-minute and the 1-hour cache TTL.
	// Source: https://platform.claude.com/docs/en/build-with-claude/prompt-caching#pricing
	cacheWrite5mFactor = 1.25
	cacheWrite1hFactor = 2.0
)

// rates holds the per-1M-token prices of one model. cacheRead is zero when
// the list gives no explicit cache read rate; it then falls back to
// cacheReadShare of input.
type rates struct {
	input, output, cacheRead float64
}

// entry is one row of the table. A model matches the row when its lower-case
// name contains any of contains (or equals any of exact) and, when also is
// set, contains also as well.
type entry struct {
	contains []string
	exact    []string
	also     string
	rates
}

func (e entry) matches(modelLower string) bool {
	if e.also != "" && !strings.Contains(modelLower, e.also) {
		return false
	}
	for _, s := range e.exact {
		if modelLower == s {
			return true
		}
	}
	for _, s := range e.contains {
		if strings.Contains(modelLower, s) {
			return true
		}
	}
	return false
}

// table is matched top to bottom; the first row that matches wins. A more
// specific name comes before a more general one, so claude-opus-5-5 never
// falls to an older opus row. Substring matching lets a provider prefix
// (us.anthropic.claude-opus-5-5) or a date suffix (claude-opus-5-5-20261101)
// still match.
var table = []entry{
	// Claude Fable 5.1 lists a $0.25 cache read.
	{contains: []string{"fable-5-1"}, rates: rates{input: 10.00, output: 50.00, cacheRead: 0.25}},
	// Claude 5 Fable/Mythos.
	{contains: []string{"fable", "mythos"}, rates: rates{input: 10.00, output: 50.00}},
	// Claude Opus 5.5 — before the opus-5 row. Cache read is 5% of input.
	{contains: []string{"opus-5-5"}, rates: rates{input: 4.00, output: 20.00, cacheRead: 0.20}},
	// Claude Opus 5 — ahead of the opus-4-1 legacy row.
	{contains: []string{"opus-5"}, rates: rates{input: 5.00, output: 25.00}},
	// Claude Sonnet 5.5 — before the sonnet-5 row; its cache read is 10% of
	// input, unlike Sonnet 5.
	{contains: []string{"sonnet-5-5"}, rates: rates{input: 2.00, output: 10.00, cacheRead: 0.10}},
	// Claude Sonnet 5.
	{contains: []string{"sonnet-5"}, rates: rates{input: 2.00, output: 10.00, cacheRead: 0.20}},
	// Legacy Opus 4.1/4.0.
	{contains: []string{"opus-4-1", "opus-4-0"}, exact: []string{"claude-opus-4"}, rates: rates{input: 15.00, output: 75.00}},
	// Opus 4.6/4.5.
	{contains: []string{"opus"}, rates: rates{input: 5.00, output: 25.00}},
	// Claude Haiku 5.5, prompts up to 100K tokens; the $0.50/$2.50 tier
	// beyond is not modelled. Before the generic haiku row.
	{contains: []string{"haiku-5-5"}, rates: rates{input: 0.10, output: 0.50}},
	// Haiku 4.5.
	{contains: []string{"haiku"}, rates: rates{input: 1.00, output: 5.00}},
	// Qwen3-Coder-Plus (International, 0-32K).
	{contains: []string{"480b", "plus"}, also: "qwen", rates: rates{input: 1.00, output: 5.00}},
	// Qwen3-Coder-Flash.
	{contains: []string{"flash"}, also: "qwen", rates: rates{input: 0.30, output: 1.50}},
	// Qwen3-Coder-Next and any other qwen.
	{contains: []string{"qwen"}, rates: rates{input: 0.07, output: 0.30}},
}

// defaultRates price an unknown model: the Sonnet 4.x rate.
var defaultRates = rates{input: 3.00, output: 15.00}

func lookup(model string) rates {
	modelLower := strings.ToLower(model)
	for _, e := range table {
		if e.matches(modelLower) {
			return e.rates
		}
	}
	return defaultRates
}

// ModelRates returns the input and output rate of model in USD per 1M tokens.
func ModelRates(model string) (input, output float64) {
	r := lookup(model)
	return r.input, r.output
}

// CacheReadRate returns the cache read rate of model in USD per 1M tokens:
// the listed rate where there is one, else 10% of the input rate. Opus 5.5
// and Sonnet 5 list $0.20 (5% of Opus 5.5's $4 input, 10% of Sonnet 5's $2);
// Sonnet 5.5 lists $0.10 (10% of its $2 input); Fable 5.1 lists $0.25.
func CacheReadRate(model string) float64 {
	return lookup(model).cacheReadRate()
}

func (r rates) cacheReadRate() float64 {
	if r.cacheRead > 0 {
		return r.cacheRead
	}
	return r.input * cacheReadShare
}

// Cost returns the USD cost of a run of model. cacheWrite5m and cacheWrite1h
// are the cache write tokens of the 5-minute and the 1-hour TTL, priced at
// 1.25x and 2x the input rate.
func Cost(model string, input, output, cacheRead, cacheWrite5m, cacheWrite1h int64) float64 {
	r := lookup(model)
	usd := float64(input)*r.input +
		float64(output)*r.output +
		float64(cacheRead)*r.cacheReadRate() +
		float64(cacheWrite5m)*r.input*cacheWrite5mFactor +
		float64(cacheWrite1h)*r.input*cacheWrite1hFactor
	return usd / 1_000_000
}
