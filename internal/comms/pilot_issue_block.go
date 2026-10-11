package comms

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	pilotIssueFence = "```pilot-issue"
	closingFence    = "```"
)

// conventionalCommitTitleRE is a copy of conventionalCommitRE in
// internal/adapters/github/issue_create.go. comms cannot import that package
// (adapters/github imports comms), so keep the two in sync.
var conventionalCommitTitleRE = regexp.MustCompile(`^(feat|fix|chore|refactor|test|docs|perf|build|ci|style)(\([^)]+\))?: .+$`)

// extractPilotIssueBlock splits a chat reply into its prose and the last
// fenced pilot-issue block, parsed as YAML (title, labels, body).
//
// found is false (and err nil) when the reply has no such block. When a block
// exists but is unusable, found is true, err describes the problem, and text
// still has the block removed.
func extractPilotIssueBlock(reply string) (text string, draft IssueDraft, found bool, err error) {
	lines := strings.Split(reply, "\n")

	start := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimRight(lines[i], " \t\r") == pilotIssueFence {
			start = i
			break
		}
	}
	if start < 0 {
		return reply, IssueDraft{}, false, nil
	}

	// The closing fence must start at column 0 so fences nested inside an
	// indented YAML block scalar do not end the block early.
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t\r") == closingFence {
			end = i
			break
		}
	}

	rest := lines[:start]
	var body string
	if end < 0 {
		body = strings.Join(lines[start+1:], "\n")
	} else {
		body = strings.Join(lines[start+1:end], "\n")
		rest = append(append([]string{}, lines[:start]...), lines[end+1:]...)
	}
	text = strings.TrimRight(strings.Join(rest, "\n"), " \t\r\n")

	if end < 0 {
		return text, IssueDraft{}, true, errors.New("pilot-issue block is not closed")
	}

	var parsed struct {
		Title  string   `yaml:"title"`
		Labels []string `yaml:"labels"`
		Body   string   `yaml:"body"`
	}
	if yerr := yaml.Unmarshal([]byte(body), &parsed); yerr != nil {
		return text, IssueDraft{}, true, fmt.Errorf("invalid YAML: %w", yerr)
	}
	title := strings.TrimSpace(parsed.Title)
	switch {
	case title == "":
		return text, IssueDraft{}, true, errors.New("title is empty")
	case strings.TrimSpace(parsed.Body) == "":
		return text, IssueDraft{}, true, errors.New("body is empty")
	case !conventionalCommitTitleRE.MatchString(title):
		return text, IssueDraft{}, true, fmt.Errorf("title %q is not a conventional-commit title (type(scope): description)", title)
	}

	return text, IssueDraft{Title: title, Body: parsed.Body, Labels: withPilotLabel(parsed.Labels)}, true, nil
}

// withPilotLabel returns labels with "pilot" appended when missing.
func withPilotLabel(labels []string) []string {
	for _, l := range labels {
		if l == "pilot" {
			return labels
		}
	}
	return append(append([]string{}, labels...), "pilot")
}
