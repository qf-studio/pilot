package comms

import (
	"strings"
	"testing"
)

const draftsFence = "```"

func draftsReply(yamlBody string) string {
	return "Here are the drafts.\n\n" + draftsFence + "pilot-drafts\n" + yamlBody + "\n" + draftsFence + "\n"
}

func TestDraftsBlock_ParsesList(t *testing.T) {
	reply := draftsReply(`- id: d1
  title: "feat(comms): add widget"
  labels: [enhancement]
  body: |
    ## Context
    Add a widget.
  deps: []
- id: d2
  title: "test(comms): cover widget"
  body: "## Context\nCover it."
  deps: [d1]`)
	text, drafts, found, err := extractPilotDraftsBlock(reply)
	if !found || err != nil {
		t.Fatalf("found=%v err=%v, want found and no error", found, err)
	}
	if strings.Contains(text, "pilot-drafts") || !strings.Contains(text, "Here are the drafts.") {
		t.Errorf("prose = %q, want the text outside the block", text)
	}
	if len(drafts) != 2 {
		t.Fatalf("drafts = %d, want 2", len(drafts))
	}
	if drafts[0].ID != "d1" || drafts[0].Title != "feat(comms): add widget" || drafts[0].Labels[0] != "enhancement" {
		t.Errorf("draft 0 = %+v", drafts[0])
	}
	if len(drafts[1].Deps) != 1 || drafts[1].Deps[0] != "d1" {
		t.Errorf("draft 1 deps = %v, want [d1]", drafts[1].Deps)
	}
	if drafts[1].Labels == nil || drafts[0].Deps == nil {
		t.Errorf("labels and deps must be non-nil so they serialize as []")
	}
}

func TestDraftsBlock_AcceptsDraftsKey(t *testing.T) {
	reply := draftsReply("drafts:\n  - id: a\n    title: \"fix(x): y\"\n    body: b")
	_, drafts, found, err := extractPilotDraftsBlock(reply)
	if !found || err != nil || len(drafts) != 1 || drafts[0].ID != "a" {
		t.Fatalf("drafts=%+v found=%v err=%v", drafts, found, err)
	}
}

func TestDraftsBlock_NoBlock(t *testing.T) {
	text, drafts, found, err := extractPilotDraftsBlock("just prose")
	if found || err != nil || drafts != nil || text != "just prose" {
		t.Errorf("text=%q drafts=%v found=%v err=%v, want not found", text, drafts, found, err)
	}
}

func TestDraftsBlock_LastBlockWins(t *testing.T) {
	reply := draftsReply("- id: old\n  title: t\n  body: b") + draftsReply("- id: new\n  title: t\n  body: b")
	_, drafts, _, err := extractPilotDraftsBlock(reply)
	if err != nil || len(drafts) != 1 || drafts[0].ID != "new" {
		t.Errorf("drafts=%+v err=%v, want the last block", drafts, err)
	}
}

func TestDraftsBlock_Invalid(t *testing.T) {
	many := ""
	for i := 0; i <= maxDraftsPerReport; i++ {
		many += "- id: d" + string(rune('a'+i)) + "\n  title: t\n  body: b\n"
	}
	tests := []struct {
		name, body, want string
	}{
		{"bad yaml", "- id: [unclosed", "invalid YAML"},
		{"empty list", "[]", "no drafts"},
		{"empty id", "- id: \"\"\n  title: t\n  body: b", "id is empty"},
		{"duplicate id", "- id: a\n  title: t\n  body: b\n- id: a\n  title: t\n  body: b", "duplicate id"},
		{"empty title", "- id: a\n  title: \"\"\n  body: b", "title is empty"},
		{"empty body", "- id: a\n  title: t\n  body: \"\"", "body is empty"},
		{"too many", many, "exceeds the limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, drafts, found, err := extractPilotDraftsBlock(draftsReply(tt.body))
			if !found || err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("found=%v err=%v, want error containing %q", found, err, tt.want)
			}
			if drafts != nil {
				t.Errorf("drafts = %v, want none on error", drafts)
			}
		})
	}
}
