package comms

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodDraftBody = "## Context\n" +
	"The widget in `internal/x/widget.go:10` never validates its input, which lets bad config through.\n\n" +
	"## Acceptance\n" +
	"- [ ] `go test ./internal/x/ -run TestWidget` passes; paste the output into the PR body.\n\n" +
	"## Implementation\n" +
	"Validate in `internal/x/widget.go`.\n\n" +
	"## Refs\n- research report\n"

// draftProject returns a checkout holding internal/x/widget.go.
func draftProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "x", "widget.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func goodDraft() TicketDraft {
	return TicketDraft{ID: "d1", Title: "fix(x): validate widget input", Labels: []string{}, Body: goodDraftBody, Deps: []string{}}
}

func reasonsOf(t *testing.T, d TicketDraft, project string, others ...string) []string {
	t.Helper()
	known := map[string]bool{d.ID: true}
	for _, o := range others {
		known[o] = true
	}
	return checkDraft(d, known, project)
}

func hasReason(reasons []string, sub string) bool {
	for _, r := range reasons {
		if strings.Contains(r, sub) {
			return true
		}
	}
	return false
}

func TestDraftCheck_ShippableDraftIsTicket(t *testing.T) {
	drafts := []TicketDraft{goodDraft()}
	checkDrafts(drafts, draftProject(t))
	if !drafts[0].Shippable.OK || drafts[0].Kind != DraftKindTicket || len(drafts[0].Shippable.Reasons) != 0 {
		t.Errorf("draft = %+v, want ok ticket with no reasons", drafts[0])
	}
	if drafts[0].Shippable.Reasons == nil {
		t.Error("reasons must be non-nil so it serializes as []")
	}
}

func TestDraftCheck_FailureMakesEpicAndKeepsBody(t *testing.T) {
	d := goodDraft()
	d.Title = "not conventional"
	drafts := []TicketDraft{d}
	checkDrafts(drafts, draftProject(t))
	if drafts[0].Shippable.OK || drafts[0].Kind != DraftKindEpic {
		t.Errorf("draft = %+v, want epic", drafts[0])
	}
	if drafts[0].Body != goodDraftBody {
		t.Error("an epic-candidate must keep its body")
	}
}

func TestDraftCheck_SpecRequired(t *testing.T) {
	d := goodDraft()
	d.Body = "too short"
	if r := reasonsOf(t, d, draftProject(t)); !hasReason(r, "spec: body too short") {
		t.Errorf("reasons = %q, want a spec reason", r)
	}
}

func TestDraftCheck_TitleMustBeConventional(t *testing.T) {
	d := goodDraft()
	d.Title = "Add widget validation"
	if r := reasonsOf(t, d, draftProject(t)); !hasReason(r, "conventional-commit") {
		t.Errorf("reasons = %q, want a title reason", r)
	}
}

func TestDraftCheck_BackticketPathMustExist(t *testing.T) {
	d := goodDraft()
	d.Body = strings.Replace(d.Body, "Validate in `internal/x/widget.go`.", "Validate in `internal/x/missing.go`.", 1)
	r := reasonsOf(t, d, draftProject(t))
	if !hasReason(r, "internal/x/missing.go") {
		t.Errorf("reasons = %q, want the missing path named", r)
	}
	if hasReason(r, "internal/x/widget.go") {
		t.Errorf("reasons = %q, the existing path (with a :line suffix elsewhere) must pass", r)
	}
}

func TestDraftCheck_PathCannotEscapeCheckout(t *testing.T) {
	project := draftProject(t)
	if pathExistsIn(project, "../outside.go") || pathExistsIn(project, "/etc/passwd") || pathExistsIn("", "internal/x/widget.go") {
		t.Error("an escaping, absolute or rootless path must count as missing")
	}
	if !pathExistsIn(project, "internal/x/widget.go") {
		t.Error("an existing relative path must be found")
	}
}

func TestDraftCheck_AcceptanceDialectRequired(t *testing.T) {
	tests := []struct {
		name, bullet string
		ok           bool
	}{
		{"paste output", "- [ ] `go test ./internal/x/` passes; paste the output into the PR body.", true},
		{"mutation pin", "- [ ] remove the validation call -> TestWidget_Validates fails", true},
		{"prose only", "- [ ] The widget validates its input.", false},
		{"command without evidence phrase or outcome", "- [ ] run `go test ./internal/x/`", false},
		{"non-allowlisted command", "- [ ] `curl localhost` passes; paste the output into the PR body.", false},
		{"negated mutation", "- [ ] remove the call -> TestWidget does not fail", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := goodDraft()
			d.Body = strings.Replace(d.Body,
				"- [ ] `go test ./internal/x/ -run TestWidget` passes; paste the output into the PR body.", tt.bullet, 1)
			got := !hasReason(reasonsOf(t, d, draftProject(t)), "evidence dialect")
			if got != tt.ok {
				t.Errorf("dialect accepted = %v, want %v", got, tt.ok)
			}
		})
	}
}

func TestDraftCheck_AcceptanceBulletOutsideSectionDoesNotCount(t *testing.T) {
	d := goodDraft()
	d.Body = strings.Replace(d.Body, "## Acceptance\n- [ ] `go test ./internal/x/ -run TestWidget` passes; paste the output into the PR body.",
		"## Acceptance\n- [ ] it works", 1) +
		"## Extra\n- [ ] `go test ./internal/x/` passes; paste the output into the PR body.\n"
	if r := reasonsOf(t, d, draftProject(t)); !hasReason(r, "evidence dialect") {
		t.Errorf("reasons = %q, a bullet under another heading must not satisfy the rule", r)
	}
}

func TestDraftCheck_DepsMustBeKnownDraftIDs(t *testing.T) {
	d := goodDraft()
	d.Deps = []string{"d2", "#123", "d1"}
	r := reasonsOf(t, d, draftProject(t), "d2")
	if hasReason(r, `"d2"`) {
		t.Errorf("reasons = %q, a known id must pass", r)
	}
	if !hasReason(r, `"#123"`) {
		t.Errorf("reasons = %q, an issue number is not a draft id", r)
	}
	if !hasReason(r, "the draft itself") {
		t.Errorf("reasons = %q, a self dep must be flagged", r)
	}
}
