package comms

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// conventionalCommitTitleRE copies conventionalCommitRE from
// internal/adapters/github/issue_create.go (comms cannot import that package).
var conventionalCommitTitleRE = regexp.MustCompile(`^(feat|fix|chore|refactor|test|docs|perf|build|ci|style)(\([^)]+\))?: .+$`)

var pilotIssueBlockRE = regexp.MustCompile("(?ms)^```pilot-issue[ \t]*\r?\n(.*?)^```[ \t]*$")

// extractPilotIssueBlock splits a chat reply into prose and the last fenced
// pilot-issue block (YAML: title, labels, body). found is false when there is
// no block; found with err set means the block is unusable.
func extractPilotIssueBlock(reply string) (text string, draft IssueDraft, found bool, err error) {
	locs := pilotIssueBlockRE.FindAllStringSubmatchIndex(reply, -1)
	if len(locs) == 0 {
		return reply, IssueDraft{}, false, nil
	}
	loc := locs[len(locs)-1]
	text = strings.TrimRight(reply[:loc[0]]+reply[loc[1]:], " \t\r\n")

	var p struct {
		Title  string   `yaml:"title"`
		Labels []string `yaml:"labels"`
		Body   string   `yaml:"body"`
	}
	if yerr := yaml.Unmarshal([]byte(reply[loc[2]:loc[3]]), &p); yerr != nil {
		return text, draft, true, fmt.Errorf("invalid YAML: %w", yerr)
	}
	title := strings.TrimSpace(p.Title)
	switch {
	case title == "":
		return text, draft, true, errors.New("title is empty")
	case strings.TrimSpace(p.Body) == "":
		return text, draft, true, errors.New("body is empty")
	case !conventionalCommitTitleRE.MatchString(title):
		return text, draft, true, fmt.Errorf("title %q is not a conventional-commit title", title)
	}
	return text, IssueDraft{Title: title, Body: p.Body, Labels: withPilotLabel(p.Labels)}, true, nil
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
