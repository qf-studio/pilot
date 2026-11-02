package comms

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// maxDraftsPerReport bounds one drafting reply so a runaway model cannot flood
// the console review screen.
const maxDraftsPerReport = 20

// Draft kinds set by the shippable check.
const (
	DraftKindTicket = "ticket"
	DraftKindEpic   = "epic"
)

// DraftShippable is the machine verdict of the shippable check for one draft.
type DraftShippable struct {
	OK      bool     `json:"ok"`
	Reasons []string `json:"reasons"`
}

// TicketDraft is one issue drafted from a research report. Nothing files it:
// the console reviews it and files it after approval.
type TicketDraft struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Labels    []string       `json:"labels"`
	Body      string         `json:"body"`
	Deps      []string       `json:"deps"`
	Kind      string         `json:"kind"`
	Shippable DraftShippable `json:"shippable"`
}

var pilotDraftsBlockRE = regexp.MustCompile("(?ms)^```pilot-drafts[ \t]*\r?\n(.*?)^```[ \t]*$")

type draftYAML struct {
	ID     string   `yaml:"id"`
	Title  string   `yaml:"title"`
	Labels []string `yaml:"labels"`
	Body   string   `yaml:"body"`
	Deps   []string `yaml:"deps"`
}

// extractPilotDraftsBlock splits a drafting reply into prose and the last
// fenced pilot-drafts block (YAML list of {id, title, labels, body, deps}).
// found is false when there is no block; found with err set means the block is
// unusable. The returned drafts carry no kind or shippable verdict yet.
func extractPilotDraftsBlock(reply string) (text string, drafts []TicketDraft, found bool, err error) {
	locs := pilotDraftsBlockRE.FindAllStringSubmatchIndex(reply, -1)
	if len(locs) == 0 {
		return reply, nil, false, nil
	}
	loc := locs[len(locs)-1]
	text = strings.TrimRight(reply[:loc[0]]+reply[loc[1]:], " \t\r\n")
	raw := []byte(reply[loc[2]:loc[3]])

	var list []draftYAML
	if yerr := yaml.Unmarshal(raw, &list); yerr != nil {
		// Tolerate a mapping with a top-level "drafts" key.
		var wrapped struct {
			Drafts []draftYAML `yaml:"drafts"`
		}
		if werr := yaml.Unmarshal(raw, &wrapped); werr != nil || len(wrapped.Drafts) == 0 {
			return text, nil, true, fmt.Errorf("invalid YAML: %w", yerr)
		}
		list = wrapped.Drafts
	}
	switch {
	case len(list) == 0:
		return text, nil, true, errors.New("no drafts in the block")
	case len(list) > maxDraftsPerReport:
		return text, nil, true, fmt.Errorf("%d drafts exceeds the limit of %d", len(list), maxDraftsPerReport)
	}

	seen := make(map[string]bool, len(list))
	for i, d := range list {
		id := strings.TrimSpace(d.ID)
		title := strings.TrimSpace(d.Title)
		switch {
		case id == "":
			return text, nil, true, fmt.Errorf("draft %d: id is empty", i+1)
		case seen[id]:
			return text, nil, true, fmt.Errorf("draft %d: duplicate id %q", i+1, id)
		case title == "":
			return text, nil, true, fmt.Errorf("draft %q: title is empty", id)
		case strings.TrimSpace(d.Body) == "":
			return text, nil, true, fmt.Errorf("draft %q: body is empty", id)
		}
		seen[id] = true
		drafts = append(drafts, TicketDraft{
			ID: id, Title: title, Body: d.Body,
			Labels: nonNilStrings(d.Labels), Deps: trimmedStrings(d.Deps),
		})
	}
	return text, drafts, true, nil
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// trimmedStrings returns a non-nil copy of in with blanks dropped.
func trimmedStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
