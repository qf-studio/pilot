package ghissue

import (
	"errors"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
	"strings"
	"testing"
)

// validBody returns a body with a section header and >= 100 chars to pass all rules.
func validBody() string {
	return strings.Repeat("x", 60) + "\n\n## Acceptance\n\n- [ ] Does the thing correctly\n"
}

func TestValidateSpec_ValidBody(t *testing.T) {
	issue := &github.Issue{
		Number: 1,
		Title:  "feat(foo): do something",
		Body:   validBody(),
	}
	result := ValidateSpec(issue, nil)
	if !result.Valid {
		t.Errorf("expected Valid=true, got reasons=%v", result.FailureReasons)
	}
	if result.SkipReason != "" {
		t.Errorf("expected no SkipReason, got %q", result.SkipReason)
	}
}

func TestValidateSpec_BodyTooShort(t *testing.T) {
	issue := &github.Issue{
		Number: 2,
		Title:  "cascade-2 repro",
		Body:   "cascade-2 repro", // 15 chars — mirrors the real cascade-2 sub-issues
	}
	result := ValidateSpec(issue, nil)
	if result.Valid {
		t.Fatal("expected Valid=false for 15-char body")
	}
	foundShort := false
	for _, r := range result.FailureReasons {
		if strings.Contains(r, "body too short") {
			foundShort = true
		}
	}
	if !foundShort {
		t.Errorf("expected 'body too short' reason, got %v", result.FailureReasons)
	}
}

func TestValidateSpec_ParentOnlyBody(t *testing.T) {
	issue := &github.Issue{
		Number: 3,
		Body:   "Parent: GH-201",
	}
	result := ValidateSpec(issue, nil)
	if result.Valid {
		t.Fatal("expected Valid=false for parent-only body")
	}
	foundParent := false
	for _, r := range result.FailureReasons {
		if strings.Contains(r, "parent reference") {
			foundParent = true
		}
	}
	if !foundParent {
		t.Errorf("expected 'parent reference' reason, got %v", result.FailureReasons)
	}
}

func TestValidateSpec_NoSectionHeader(t *testing.T) {
	// 200 chars, no section header
	body := strings.Repeat("This is a long body without any structural section header. ", 4)
	issue := &github.Issue{
		Number: 4,
		Body:   body,
	}
	result := ValidateSpec(issue, nil)
	if result.Valid {
		t.Fatal("expected Valid=false for body without section header")
	}
	foundHeader := false
	for _, r := range result.FailureReasons {
		if strings.Contains(r, "structural section header") && strings.Contains(r, "H1 is not accepted") && strings.Contains(r, "any language") {
			foundHeader = true
		}
	}
	if !foundHeader {
		t.Errorf("expected reason explaining H2-H6 range, H1 rejection, and any-language body content, got %v", result.FailureReasons)
	}
}

func TestValidateSpec_SkipLabelOptOut(t *testing.T) {
	// Body fails all rules — but the skip label is present.
	issue := &github.Issue{
		Number: 5,
		Body:   "too short",
		Labels: []github.Label{{Name: github.LabelSkipSpecCheck}},
	}
	result := ValidateSpec(issue, nil)
	if !result.Valid {
		t.Errorf("expected Valid=true (opted out), got reasons=%v", result.FailureReasons)
	}
	if result.SkipReason == "" {
		t.Error("expected non-empty SkipReason for opted-out issue")
	}
}

func TestValidateSpec_SubIssueParentPasses(t *testing.T) {
	// Child body is "see parent" (terse) but has autopilot-meta marker and the
	// parent is well-specced → child should pass.
	parentBody := validBody()
	parentResolver := func(num int) (*github.Issue, error) {
		if num == 201 {
			return &github.Issue{Number: 201, Body: parentBody}, nil
		}
		return nil, errors.New("not found")
	}

	child := &github.Issue{
		Number: 6,
		Body:   "see parent\nParent: GH-201\n<!-- autopilot-meta branch:pilot/GH-200 pr:99 iteration:1 -->",
	}
	result := ValidateSpec(child, parentResolver)
	if !result.Valid {
		t.Errorf("expected Valid=true (parent passes), got reasons=%v", result.FailureReasons)
	}
	if !strings.Contains(result.SkipReason, "parent GH-201") {
		t.Errorf("expected SkipReason to mention parent GH-201, got %q", result.SkipReason)
	}
}

func TestValidateSpec_SubIssueParentFails(t *testing.T) {
	// Child has autopilot-meta marker but parent also fails — child should fail.
	parentResolver := func(num int) (*github.Issue, error) {
		return &github.Issue{Number: num, Body: "too short"}, nil
	}

	child := &github.Issue{
		Number: 7,
		Body:   "see parent\nParent: GH-300\n<!-- autopilot-meta branch:pilot/GH-299 pr:88 iteration:1 -->",
	}
	result := ValidateSpec(child, parentResolver)
	if result.Valid {
		t.Error("expected Valid=false when both child and parent fail")
	}
}

func TestValidateSpec_SectionHeaderVariants(t *testing.T) {
	headers := []string{
		"## Acceptance", "## Implementation", "## Context",
		"## Background", "## Approach", "## Design", "## Refs",
	}
	for _, h := range headers {
		body := strings.Repeat("x", 80) + "\n\n" + h + "\n\nsome content here and there\n"
		issue := &github.Issue{Number: 8, Body: body}
		result := ValidateSpec(issue, nil)
		if !result.Valid {
			t.Errorf("header %q should make body valid, got reasons=%v", h, result.FailureReasons)
		}
	}

	// Body content underneath a valid English header may be written in any
	// language — only the heading text itself is checked.
	nonEnglishBodyContent := strings.Repeat("x", 80) + "\n\n## Acceptance\n\nContenido de aceptación en español aquí.\n"
	issue := &github.Issue{Number: 8, Body: nonEnglishBodyContent}
	result := ValidateSpec(issue, nil)
	if !result.Valid {
		t.Errorf("non-English body content under a valid English header should still be valid, got reasons=%v", result.FailureReasons)
	}

	// A translated (non-English) heading is not recognized — the heading
	// text itself must match one of the accepted words exactly in English.
	translatedHeader := strings.Repeat("x", 80) + "\n\n## Aceptación\n\nsome content here and there\n"
	issue = &github.Issue{Number: 8, Body: translatedHeader}
	result = ValidateSpec(issue, nil)
	if result.Valid {
		t.Error("translated (non-English) heading '## Aceptación' should NOT be accepted as a structural section header")
	}
	foundHeader := false
	for _, r := range result.FailureReasons {
		if strings.Contains(r, "structural section header") && strings.Contains(r, "H1 is not accepted") && strings.Contains(r, "exactly in English") && strings.Contains(r, "any language") {
			foundHeader = true
		}
	}
	if !foundHeader {
		t.Errorf("expected reason explaining exact-English heading match and any-language body content, got %v", result.FailureReasons)
	}
}

func TestValidateSpec_H3ToH6SectionHeaders(t *testing.T) {
	// H3–H6 headers must be accepted (relaxed from H2-only).
	passCases := []string{
		"### Acceptance criteria",
		"### Acceptance",
		"#### Implementation",
		"###### Refs",
	}
	for _, h := range passCases {
		body := strings.Repeat("x", 80) + "\n\n" + h + "\n\nsome content here and there\n"
		issue := &github.Issue{Number: 9, Body: body}
		result := ValidateSpec(issue, nil)
		if !result.Valid {
			t.Errorf("H3–H6 header %q should be accepted, got reasons=%v", h, result.FailureReasons)
		}
	}

	// H1 must still be rejected.
	h1Body := strings.Repeat("x", 80) + "\n\n# Acceptance\n\nsome content here and there\n"
	issue := &github.Issue{Number: 10, Body: h1Body}
	result := ValidateSpec(issue, nil)
	if result.Valid {
		t.Error("H1 header '# Acceptance' should NOT be accepted as a structural section header")
	}
	foundHeader := false
	for _, r := range result.FailureReasons {
		if strings.Contains(r, "structural section header") && strings.Contains(r, "H1 is not accepted") {
			foundHeader = true
		}
	}
	if !foundHeader {
		t.Errorf("expected reason explaining H1 rejection for H1 body, got %v", result.FailureReasons)
	}
}
