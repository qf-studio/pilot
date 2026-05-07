package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// JudgeVerdict is the result of an intent judge evaluation.
type JudgeVerdict struct {
	// Passed indicates whether the diff aligns with the issue intent.
	Passed bool
	// Reason explains why the verdict was PASS or FAIL.
	Reason string
	// Confidence is the judge's confidence level (0.0-1.0).
	Confidence float64
}

// IntentJudge compares git diffs against the original issue to catch scope creep,
// missing requirements, and unrelated changes. Uses Claude Haiku for fast, cheap evaluation.
// Industry research (Spotify) shows this catches ~25% of PRs that would ship wrong code.
type IntentJudge struct {
	apiKey     string
	apiURL     string
	model      string
	httpClient *http.Client
}

// NewIntentJudge creates a new IntentJudge that calls the Anthropic API directly.
func NewIntentJudge(apiKey string) *IntentJudge {
	return &IntentJudge{
		apiKey: apiKey,
		apiURL: "https://api.anthropic.com/v1/messages",
		model:  "claude-haiku-4-5-20251001",
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// newIntentJudgeWithURL creates an IntentJudge with a custom API URL for testing.
func newIntentJudgeWithURL(apiKey, url string) *IntentJudge {
	j := NewIntentJudge(apiKey)
	j.apiURL = url
	return j
}

// PreFlightDecision is the decision returned by the pre-flight intent judge.
type PreFlightDecision string

const (
	PreFlightAccept           PreFlightDecision = "accept"
	PreFlightRejectQuestion   PreFlightDecision = "reject_question"
	PreFlightRejectVague      PreFlightDecision = "reject_vague"
	PreFlightRejectConflicting PreFlightDecision = "reject_conflicting"
	PreFlightRejectStale      PreFlightDecision = "reject_stale"
	PreFlightRejectOutOfScope PreFlightDecision = "reject_out_of_scope"
)

// PreFlightVerdict is the result of a pre-flight issue evaluation.
type PreFlightVerdict struct {
	Decision   PreFlightDecision
	Reason     string
	Confidence float64
}

// IsRejection returns true if the verdict indicates the issue should be rejected.
func (v *PreFlightVerdict) IsRejection() bool {
	return v.Decision != PreFlightAccept
}

var (
	verdictPassRegex    = regexp.MustCompile(`VERDICT:\s*PASS`)
	verdictFailRegex    = regexp.MustCompile(`VERDICT:\s*FAIL`)
	confidenceRegex     = regexp.MustCompile(`CONFIDENCE:\s*([0-9]*\.?[0-9]+)`)
	maxDiffCharsDefault = 8000

	preflightDecisionRegex   = regexp.MustCompile(`DECISION:\s*(\S+)`)
	preflightReasonRegex     = regexp.MustCompile(`REASON:\s*(.+)`)
	preflightConfidenceRegex = regexp.MustCompile(`CONFIDENCE:\s*([0-9]*\.?[0-9]+)`)
	maxPreflightBodyChars    = 4000
)

const preflightJudgeSystemPrompt = `You are a pre-flight issue quality judge. Evaluate whether a GitHub issue is actionable for an autonomous AI developer.

Classify the issue as exactly one of:
- accept: clear, specific, implementable request with enough context
- reject_question: issue is asking a question rather than requesting implementation
- reject_vague: too vague or ambiguous to implement without further clarification
- reject_conflicting: contains contradictory requirements that cannot all be satisfied
- reject_stale: describes something already done or clearly outdated
- reject_out_of_scope: outside the repository's purpose or requires unavailable external resources

Output exactly:
DECISION: <classification>
REASON: <one sentence explanation>
CONFIDENCE: <0.0-1.0>`

const intentJudgeSystemPrompt = `You are a code review judge. Compare the git diff against the original issue title and description. Determine if the diff implements what was requested.

Check for:
1) Scope creep (changes unrelated to the issue)
2) Missing requirements (issue asks for X but diff doesn't include it)
3) Unrelated changes (refactoring or cleanup not mentioned in issue)
4) Incomplete multi-file changes (if the issue implies changes to multiple backends, adapters, or sibling files, verify ALL were updated — not just one)

Output exactly one of: VERDICT:PASS or VERDICT:FAIL followed by a brief reason on the next line.
Then output CONFIDENCE:X.X (0.0-1.0).`

// Judge evaluates whether a git diff aligns with the original issue intent.
func (j *IntentJudge) Judge(ctx context.Context, issueTitle, issueBody, diff string) (*JudgeVerdict, error) {
	if diff == "" {
		return nil, fmt.Errorf("empty diff")
	}

	// Truncate diff to prevent token overflow
	maxChars := maxDiffCharsDefault
	if len(diff) > maxChars {
		diff = diff[:maxChars] + "\n...[truncated]"
	}

	userContent := fmt.Sprintf("## Issue Title\n%s\n\n## Issue Description\n%s\n\n## Git Diff\n```diff\n%s\n```",
		issueTitle, issueBody, diff)

	reqBody := haikuRequest{
		Model:     j.model,
		MaxTokens: 512,
		System:    intentJudgeSystemPrompt,
		Messages: []haikuMessage{
			{Role: "user", Content: userContent},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.apiURL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", j.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := j.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var apiResp haikuResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if len(apiResp.Content) == 0 || apiResp.Content[0].Text == "" {
		return nil, fmt.Errorf("empty response from API")
	}

	return parseJudgeResponse(apiResp.Content[0].Text)
}

// JudgeIssue evaluates whether a GitHub issue is actionable before dispatching to a worker.
// Returns a PreFlightVerdict with decision, reason, and confidence.
// Empty body is treated as vague (returns reject_vague, not an error).
// Body is truncated to maxPreflightBodyChars before sending.
func (j *IntentJudge) JudgeIssue(ctx context.Context, title, body, repoContext string) (*PreFlightVerdict, error) {
	if len(body) > maxPreflightBodyChars {
		body = body[:maxPreflightBodyChars] + "\n...[truncated]"
	}

	userContent := fmt.Sprintf("## Issue Title\n%s\n\n## Issue Description\n%s", title, body)
	if repoContext != "" {
		userContent += fmt.Sprintf("\n\n## Repository Context\n%s", repoContext)
	}

	reqBody := haikuRequest{
		Model:     j.model,
		MaxTokens: 256,
		System:    preflightJudgeSystemPrompt,
		Messages: []haikuMessage{
			{Role: "user", Content: userContent},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.apiURL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", j.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := j.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var apiResp haikuResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if len(apiResp.Content) == 0 || apiResp.Content[0].Text == "" {
		return nil, fmt.Errorf("empty response from API")
	}

	return parsePreFlightResponse(apiResp.Content[0].Text)
}

// parsePreFlightResponse extracts decision, reason, and confidence from the pre-flight judge's response.
func parsePreFlightResponse(text string) (*PreFlightVerdict, error) {
	verdict := &PreFlightVerdict{}

	m := preflightDecisionRegex.FindStringSubmatch(text)
	if len(m) < 2 {
		return nil, fmt.Errorf("no DECISION signal found in response")
	}
	decision := PreFlightDecision(strings.ToLower(strings.TrimSpace(m[1])))
	switch decision {
	case PreFlightAccept, PreFlightRejectQuestion, PreFlightRejectVague,
		PreFlightRejectConflicting, PreFlightRejectStale, PreFlightRejectOutOfScope:
		verdict.Decision = decision
	default:
		return nil, fmt.Errorf("unknown DECISION value: %q", m[1])
	}

	if rm := preflightReasonRegex.FindStringSubmatch(text); len(rm) >= 2 {
		verdict.Reason = strings.TrimSpace(rm[1])
	}

	if cm := preflightConfidenceRegex.FindStringSubmatch(text); len(cm) >= 2 {
		if c, err := strconv.ParseFloat(cm[1], 64); err == nil {
			verdict.Confidence = c
		}
	}

	return verdict, nil
}

// parseJudgeResponse extracts verdict, reason, and confidence from the judge's response.
func parseJudgeResponse(text string) (*JudgeVerdict, error) {
	verdict := &JudgeVerdict{}

	if verdictPassRegex.MatchString(text) {
		verdict.Passed = true
	} else if verdictFailRegex.MatchString(text) {
		verdict.Passed = false
	} else {
		return nil, fmt.Errorf("no VERDICT signal found in response")
	}

	// Extract confidence
	if m := confidenceRegex.FindStringSubmatch(text); len(m) >= 2 {
		if c, err := strconv.ParseFloat(m[1], 64); err == nil {
			verdict.Confidence = c
		}
	}

	// Extract reason: text between verdict line and confidence line
	lines := strings.Split(text, "\n")
	var reasonLines []string
	pastVerdict := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if verdictPassRegex.MatchString(trimmed) || verdictFailRegex.MatchString(trimmed) {
			pastVerdict = true
			continue
		}
		if confidenceRegex.MatchString(trimmed) {
			break
		}
		if pastVerdict && trimmed != "" {
			reasonLines = append(reasonLines, trimmed)
		}
	}
	verdict.Reason = strings.Join(reasonLines, " ")

	return verdict, nil
}
