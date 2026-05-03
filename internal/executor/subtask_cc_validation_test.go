package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestValidateAndFixSubtaskTitles_AllConventional verifies that already-valid titles
// pass through validateAndFixSubtaskTitles unchanged.
func TestValidateAndFixSubtaskTitles_AllConventional(t *testing.T) {
	subtasks := []PlannedSubtask{
		{Order: 1, Title: "feat(auth): add OAuth provider integration"},
		{Order: 2, Title: "fix(api): handle nil response in webhook"},
		{Order: 3, Title: "chore(deps): upgrade go modules to latest"},
	}
	parent := &Task{ID: "GH-100", Title: "feat(auth): add OAuth provider"}

	result := validateAndFixSubtaskTitles(context.Background(), subtasks, parent, nil, nil)

	if len(result) != 3 {
		t.Fatalf("expected 3 subtasks, got %d", len(result))
	}
	for i, st := range result {
		if !isConventionalSubtaskTitle(st.Title) {
			t.Errorf("subtask %d title %q: expected conventional-commit format", i+1, st.Title)
		}
		// Unchanged
		if st.Title != subtasks[i].Title {
			t.Errorf("subtask %d title changed from %q to %q", i+1, subtasks[i].Title, st.Title)
		}
	}
}

// TestValidateAndFixSubtaskTitles_PlaceholderFallback verifies that placeholder titles
// like "GH-N: Subtask K" (from syntheticSubtaskTitle) trigger Approach B fallback
// when no parser is available, and the result matches conventionalSubtaskTitleRE.
func TestValidateAndFixSubtaskTitles_PlaceholderFallback(t *testing.T) {
	subtasks := []PlannedSubtask{
		{Order: 1, Title: "GH-100: Subtask 1"},
		{Order: 2, Title: "GH-100: Subtask 2"},
	}
	parent := &Task{ID: "GH-100", Title: "feat(gateway): add webhook support"}

	result := validateAndFixSubtaskTitles(context.Background(), subtasks, parent, nil, nil)

	if len(result) != 2 {
		t.Fatalf("expected 2 subtasks, got %d", len(result))
	}
	for i, st := range result {
		if !isConventionalSubtaskTitle(st.Title) {
			t.Errorf("subtask %d title %q: does not match conventional-commits regex after fallback", i+1, st.Title)
		}
		if !strings.HasPrefix(st.Title, "feat(gateway):") {
			t.Errorf("subtask %d title %q: expected feat(gateway): prefix inherited from parent", i+1, st.Title)
		}
	}
}

// TestValidateAndFixSubtaskTitles_RepromptSucceeds verifies that a SubtaskParser
// re-prompt can fix non-conventional titles via the LLM before Approach B is needed.
func TestValidateAndFixSubtaskTitles_RepromptSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": `{"subtasks": [{"order": 1, "title": "feat(auth): add OAuth integration"}, {"order": 2, "title": "fix(api): handle nil response"}]}`,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	parser := newSubtaskParserWithURL("test-api-key", server.URL, nil)

	subtasks := []PlannedSubtask{
		{Order: 1, Title: "Add OAuth integration"},  // not conventional
		{Order: 2, Title: "Handle nil response"},    // not conventional
	}
	parent := &Task{ID: "GH-100", Title: "feat(auth): add OAuth provider"}

	result := validateAndFixSubtaskTitles(context.Background(), subtasks, parent, parser, nil)

	if len(result) != 2 {
		t.Fatalf("expected 2 subtasks, got %d", len(result))
	}
	for i, st := range result {
		if !isConventionalSubtaskTitle(st.Title) {
			t.Errorf("subtask %d title %q: expected conventional-commit format after re-prompt", i+1, st.Title)
		}
	}
	// Verify these are the API-returned titles, not Approach B fallbacks
	if result[0].Title != "feat(auth): add OAuth integration" {
		t.Errorf("subtask 1 title = %q; want re-prompt result", result[0].Title)
	}
}

// TestValidateAndFixSubtaskTitles_ParentInheritanceFallback verifies Approach B:
// when the LLM re-prompt fails (server 500), the parent's type/scope is inherited
// and the resulting title is valid conventional-commit format.
func TestValidateAndFixSubtaskTitles_ParentInheritanceFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	parser := newSubtaskParserWithURL("test-api-key", server.URL, nil)

	subtasks := []PlannedSubtask{
		{Order: 1, Title: "GH-100: Subtask 1"}, // placeholder
		{Order: 2, Title: "Add some feature"},  // not conventional
	}
	parent := &Task{ID: "GH-100", Title: "feat(gateway): add webhook support"}

	result := validateAndFixSubtaskTitles(context.Background(), subtasks, parent, parser, nil)

	for i, st := range result {
		if !isConventionalSubtaskTitle(st.Title) {
			t.Errorf("subtask %d title %q: expected conventional via Approach B (parent: %q)",
				i+1, st.Title, parent.Title)
		}
		if !strings.HasPrefix(st.Title, "feat(gateway):") {
			t.Errorf("subtask %d title %q: expected feat(gateway): prefix inherited from parent", i+1, st.Title)
		}
	}
}

// TestDecomposeEpicTitlesConventional verifies that a batch of subtasks returned from
// the planning pipeline always results in conventional-commit titles after
// validateAndFixSubtaskTitles is applied (the shape a real decompose run produces).
func TestDecomposeEpicTitlesConventional(t *testing.T) {
	// Simulate LLM output that does not follow conventional-commit format
	subtasks := []PlannedSubtask{
		{Order: 1, Title: "Set up database schema"},
		{Order: 2, Title: "Implement authentication service"},
		{Order: 3, Title: "Add API endpoints"},
	}
	parent := &Task{ID: "GH-50", Title: "feat(backend): add user authentication system"}

	result := validateAndFixSubtaskTitles(context.Background(), subtasks, parent, nil, nil)

	if len(result) != 3 {
		t.Fatalf("expected 3 subtasks, got %d", len(result))
	}
	for i, st := range result {
		if !conventionalSubtaskTitleRE.MatchString(st.Title) {
			t.Errorf("subtask %d title %q: does not match conventional-commits regex", i+1, st.Title)
		}
	}
}

// TestCreateSubIssues_SkipsWhenChildrenExist verifies that CreateSubIssues returns
// ErrSubIssuesAlreadyExist and does not spawn gh CLI when open children already exist.
func TestCreateSubIssues_SkipsWhenChildrenExist(t *testing.T) {
	r := NewRunner()
	r.dryRun = true // prevent any accidental gh calls
	r.openSubIssueCheck = func(_ context.Context, _, _ string) (bool, error) {
		return true, nil // simulate existing open children
	}

	plan := &EpicPlan{
		ParentTask: &Task{
			ID:    "GH-200",
			Title: "feat(auth): add OAuth provider",
		},
		Subtasks: []PlannedSubtask{
			{Order: 1, Title: "feat(auth): add OAuth provider integration"},
		},
	}

	_, err := r.CreateSubIssues(context.Background(), plan, "")
	if err != ErrSubIssuesAlreadyExist {
		t.Errorf("expected ErrSubIssuesAlreadyExist, got %v", err)
	}
}

// TestCreateSubIssues_ProceedsWhenNoChildren verifies that CreateSubIssues proceeds
// normally when the open-children check returns false.
func TestCreateSubIssues_ProceedsWhenNoChildren(t *testing.T) {
	r := NewRunner()
	r.dryRun = true
	r.openSubIssueCheck = func(_ context.Context, _, _ string) (bool, error) {
		return false, nil // no existing children
	}
	// executeFunc is nil → createSubIssuesViaGitHub will try gh CLI which will fail.
	// We just want to confirm the dedup guard doesn't fire — the gh CLI failure is expected.

	plan := &EpicPlan{
		ParentTask: &Task{
			ID:    "GH-201",
			Title: "feat(auth): add OAuth provider",
		},
		Subtasks: []PlannedSubtask{
			{Order: 1, Title: "feat(auth): add OAuth provider integration"},
		},
	}

	_, err := r.CreateSubIssues(context.Background(), plan, "")
	// Should NOT be ErrSubIssuesAlreadyExist — some other error is fine
	if err == ErrSubIssuesAlreadyExist {
		t.Error("should not return ErrSubIssuesAlreadyExist when no open children exist")
	}
}

// TestIsPlaceholderSubtaskTitle verifies detection of synthetic placeholder titles.
func TestIsPlaceholderSubtaskTitle(t *testing.T) {
	accept := []string{
		"GH-123: Subtask 1",
		"GH-2494: Subtask 10",
		"APP-456: Subtask 3",
	}
	for _, title := range accept {
		if !isPlaceholderSubtaskTitle(title) {
			t.Errorf("expected PLACEHOLDER for %q", title)
		}
	}

	reject := []string{
		"feat(auth): add OAuth provider",
		"fix: handle nil response",
		"Add OAuth provider",
		"GH-123: fix the bug",    // not "Subtask N"
		"GH-123: Subtask",        // no number
	}
	for _, title := range reject {
		if isPlaceholderSubtaskTitle(title) {
			t.Errorf("expected NOT placeholder for %q", title)
		}
	}
}

// TestExtractParentTypeScope verifies prefix extraction from parent titles.
func TestExtractParentTypeScope(t *testing.T) {
	tests := []struct {
		parent string
		want   string
	}{
		{"feat(auth): add OAuth", "feat(auth):"},
		{"fix: resolve nil panic", "fix:"},
		{"chore(deps): bump versions", "chore(deps):"},
		{"Add some feature", "chore:"},    // not conventional → default
		{"", "chore:"},                    // empty → default
	}
	for _, tt := range tests {
		got := extractParentTypeScope(tt.parent)
		if got != tt.want {
			t.Errorf("extractParentTypeScope(%q) = %q, want %q", tt.parent, got, tt.want)
		}
	}
}

// TestApplyParentTypeScopeFallback verifies Approach B produces valid titles.
func TestApplyParentTypeScopeFallback(t *testing.T) {
	subtasks := []PlannedSubtask{
		{Order: 1, Title: "GH-50: Subtask 1"},
		{Order: 2, Title: "Add API endpoints"},
		{Order: 3, Title: "feat(api): already valid"},
	}
	parent := &Task{Title: "feat(api): add REST endpoints"}
	invalidIdx := []int{0, 1} // only fix the first two

	result := applyParentTypeScopeFallback(subtasks, invalidIdx, parent.Title)

	for i := range result[:2] {
		if !conventionalSubtaskTitleRE.MatchString(result[i].Title) {
			t.Errorf("subtask %d title %q does not match conventional-commits regex after Approach B", i+1, result[i].Title)
		}
	}
	// Third subtask should be unchanged
	if result[2].Title != "feat(api): already valid" {
		t.Errorf("subtask 3 title changed unexpectedly: %q", result[2].Title)
	}
}

// TestReformatTitles_SubtaskParser verifies that ReformatTitles correctly parses
// the LLM response and updates titles by order.
func TestReformatTitles_SubtaskParser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": `{"subtasks": [{"order": 1, "title": "feat(db): add migration"}, {"order": 2, "title": "chore(api): wire endpoint"}]}`,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	parser := newSubtaskParserWithURL("test-api-key", server.URL, nil)

	subtasks := []PlannedSubtask{
		{Order: 1, Title: "Add migration", Description: "Create user table"},
		{Order: 2, Title: "Wire endpoint", Description: "REST handler"},
	}

	result, err := parser.ReformatTitles(context.Background(), "feat(db): add user schema", subtasks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 subtasks, got %d", len(result))
	}
	// Titles should be updated
	if result[0].Title != "feat(db): add migration" {
		t.Errorf("subtask 1 title = %q, want %q", result[0].Title, "feat(db): add migration")
	}
	// Descriptions should be preserved
	if result[0].Description != "Create user table" {
		t.Errorf("subtask 1 description changed unexpectedly: %q", result[0].Description)
	}
}

// TestReformatTitles_NilParser verifies that nil parser returns an error.
func TestReformatTitles_NilParser(t *testing.T) {
	var p *SubtaskParser
	_, err := p.ReformatTitles(context.Background(), "feat: some parent", nil)
	if err == nil {
		t.Fatal("expected error from nil parser")
	}
}

// TestAutoCorrector_Unaffected verifies that the user-submitted-issue normalizeTitle path
// (the auto-corrector) is unaffected by the decomposer changes.
func TestAutoCorrector_Unaffected(t *testing.T) {
	tests := []struct {
		title    string
		labels   []string
		wantErr  bool
		wantType string // conventional type prefix expected in output
	}{
		{"fix(api): handle nil", nil, false, "fix(api):"},
		{"feat: add login", nil, false, "feat:"},
		{"Add login feature", []string{"enhancement"}, false, "feat:"},
		{"this is unrecognizable", nil, true, ""},
	}
	for _, tt := range tests {
		got, err := normalizeTitle(tt.title, tt.labels)
		if (err != nil) != tt.wantErr {
			t.Errorf("normalizeTitle(%q) error = %v, wantErr %v", tt.title, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && tt.wantType != "" && !strings.HasPrefix(got, tt.wantType) {
			t.Errorf("normalizeTitle(%q) = %q; expected prefix %q", tt.title, got, tt.wantType)
		}
	}
}
