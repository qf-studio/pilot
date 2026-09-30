package memory

import (
	"database/sql"
	"strings"
	"time"
	"unicode"
)

// Review verdict vocabulary (GH-5494). Reviewers write the verdict as the
// first word of the review body; founder-token PRs cannot receive APPROVED /
// CHANGES_REQUESTED events, so their verdicts arrive as COMMENTED reviews.
const (
	VerdictApproveWDefects = "APPROVE-w-defects"
	VerdictApproveWNotes   = "APPROVE-w-notes"
	VerdictApprove         = "APPROVE"
	VerdictRequestChanges  = "REQUEST-CHANGES"
)

// verdictTokens is ordered longest-first so "APPROVE-w-notes" is never
// truncated to "APPROVE".
var verdictTokens = []string{
	VerdictApproveWDefects,
	VerdictApproveWNotes,
	VerdictApprove,
	VerdictRequestChanges,
}

// ParseReviewVerdict derives a verdict from a review body and GitHub state.
//
// The body wins: the first non-empty line, after stripping leading markdown
// emphasis, is matched case-insensitively against the verdict tokens. Only
// when the body carries no verdict word does the GitHub state apply
// (APPROVED -> APPROVE, CHANGES_REQUESTED -> REQUEST-CHANGES). Anything else
// yields "" — an unparseable review is recorded as unknown, never guessed.
func ParseReviewVerdict(body, state string) string {
	if v := verdictFromBody(body); v != "" {
		return v
	}
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "APPROVED":
		return VerdictApprove
	case "CHANGES_REQUESTED":
		return VerdictRequestChanges
	}
	return ""
}

func verdictFromBody(body string) string {
	var first string
	for _, line := range strings.Split(body, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			first = t
			break
		}
	}
	first = strings.TrimLeft(first, "*_`~#> \t")
	for _, tok := range verdictTokens {
		if len(first) < len(tok) || !strings.EqualFold(first[:len(tok)], tok) {
			continue
		}
		// The token must end at a word boundary: "APPROVED" or
		// "APPROVE-with-notes" are not the APPROVE verdict.
		if rest := first[len(tok):]; rest != "" {
			r := []rune(rest)[0]
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
				continue
			}
		}
		return tok
	}
	return ""
}

// PRReview is one GitHub review of a PR, as recorded in the pr_reviews ledger
// table.
type PRReview struct {
	ReviewID    int64
	ProjectPath string
	PRNumber    int
	// ExecutionID joins the review to executions.id ('' when unresolved).
	ExecutionID string
	Reviewer    string
	// State is the GitHub review state (APPROVED / CHANGES_REQUESTED / COMMENTED ...).
	State string
	// Verdict is the parsed verdict (see ParseReviewVerdict), '' when unknown.
	Verdict     string
	SubmittedAt time.Time
}

// UpsertPRReview inserts a review, or refreshes the existing row with the same
// review_id. A refresh never blanks a previously resolved execution_id.
func (s *Store) UpsertPRReview(r *PRReview) error {
	var submitted interface{}
	if !r.SubmittedAt.IsZero() {
		submitted = r.SubmittedAt.UTC()
	}
	return s.withRetry("UpsertPRReview", func() error {
		_, err := s.db.Exec(`
			INSERT INTO pr_reviews (review_id, project_path, pr_number, execution_id, reviewer, state, verdict, submitted_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(review_id) DO UPDATE SET
				project_path = excluded.project_path,
				pr_number = excluded.pr_number,
				execution_id = CASE WHEN excluded.execution_id <> '' THEN excluded.execution_id ELSE pr_reviews.execution_id END,
				reviewer = excluded.reviewer,
				state = excluded.state,
				verdict = excluded.verdict,
				submitted_at = excluded.submitted_at
		`, r.ReviewID, r.ProjectPath, r.PRNumber, r.ExecutionID, r.Reviewer, r.State, r.Verdict, submitted)
		return err
	})
}

// ListPRReviewIDs returns the review ids already recorded for a PR, so the
// collector only upserts reviews it has not seen.
func (s *Store) ListPRReviewIDs(projectPath string, prNumber int) (map[int64]struct{}, error) {
	rows, err := s.db.Query(`SELECT review_id FROM pr_reviews WHERE project_path = ? AND pr_number = ?`, projectPath, prNumber)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	ids := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = struct{}{}
	}
	return ids, rows.Err()
}

// ListPRReviewsByExecution returns the reviews joined to an execution,
// oldest first.
func (s *Store) ListPRReviewsByExecution(executionID string) ([]*PRReview, error) {
	rows, err := s.db.Query(`
		SELECT review_id, project_path, pr_number, execution_id, reviewer, state, verdict, submitted_at
		FROM pr_reviews
		WHERE execution_id = ?
		ORDER BY submitted_at ASC, review_id ASC
	`, executionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*PRReview
	for rows.Next() {
		var r PRReview
		var submitted sql.NullTime
		if err := rows.Scan(&r.ReviewID, &r.ProjectPath, &r.PRNumber, &r.ExecutionID, &r.Reviewer, &r.State, &r.Verdict, &submitted); err != nil {
			return nil, err
		}
		if submitted.Valid {
			r.SubmittedAt = submitted.Time
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ListRecentPRExecutions returns non-canary executions of projectPath that
// opened a PR (pr_url set) and were created at or after since, newest first.
// Only ID, TaskID, ProjectPath, PRUrl and CreatedAt are populated. It is the
// candidate list for post-merge review collection: merged PRs have left
// autopilot's active set, but their executions remain in the ledger.
func (s *Store) ListRecentPRExecutions(projectPath string, since time.Time) ([]*Execution, error) {
	rows, err := s.db.Query(`
		SELECT id, task_id, project_path, COALESCE(pr_url, ''), created_at
		FROM executions
		WHERE project_path = ? AND COALESCE(pr_url, '') != '' AND created_at >= ? AND COALESCE(is_canary, 0) = 0
		ORDER BY created_at DESC
	`, projectPath, since.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*Execution
	for rows.Next() {
		var e Execution
		if err := rows.Scan(&e.ID, &e.TaskID, &e.ProjectPath, &e.PRUrl, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// ModelVerdictCount is the number of reviews with a given verdict on
// executions of a given model.
type ModelVerdictCount struct {
	Model   string
	Verdict string
	Count   int
}

// GetReviewVerdictCountsByModel joins pr_reviews to executions by
// execution_id and counts parsed verdicts per model for executions created at
// or after since — the read side the bench uses for a per-model defect-rate
// column. Reviews with an empty (unparsed) verdict are excluded.
func (s *Store) GetReviewVerdictCountsByModel(since time.Time) ([]ModelVerdictCount, error) {
	rows, err := s.db.Query(`
		SELECT COALESCE(NULLIF(e.model_name, ''), 'unknown') AS model, r.verdict, COUNT(*)
		FROM pr_reviews r
		JOIN executions e ON e.id = r.execution_id
		WHERE r.verdict != '' AND e.created_at >= ?
		GROUP BY model, r.verdict
		ORDER BY model, r.verdict
	`, since.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ModelVerdictCount
	for rows.Next() {
		var c ModelVerdictCount
		if err := rows.Scan(&c.Model, &c.Verdict, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
