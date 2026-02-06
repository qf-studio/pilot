package github

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// parentRefRegex extracts parent issue references from issue body.
// Matches "Parent: GH-123" or "Parent: #123" at the start of a line.
var parentRefRegex = regexp.MustCompile(`(?m)^Parent:\s*(?:GH-|#)(\d+)`)

// GroupedIssue represents either a standalone issue or an epic with its sub-issues.
type GroupedIssue struct {
	// Issue is the top-level issue (parent for epics, the issue itself for standalone).
	Issue *Issue

	// SubIssues are the child issues for epics. Empty for standalone issues.
	SubIssues []*Issue

	// TotalSubs is the count of all sub-issues.
	TotalSubs int

	// DoneSubs is the count of completed sub-issues (state=="closed" or has pilot-done label).
	DoneSubs int

	// IsEpic indicates this is a parent epic with sub-issues.
	IsEpic bool

	// IsActive is true when the epic still has incomplete sub-issues.
	// For standalone issues, reflects whether the issue is open.
	IsActive bool
}

// ParseParentIssueNumber extracts the parent issue number from an issue body.
// Returns 0 if no parent reference is found.
func ParseParentIssueNumber(body string) int {
	matches := parentRefRegex.FindStringSubmatch(body)
	if len(matches) < 2 {
		return 0
	}
	var num int
	_, _ = fmt.Sscanf(matches[1], "%d", &num)
	return num
}

// GroupIssues takes a flat list of issues and groups epics with their sub-issues.
// Standalone tasks (no parent, not a parent) pass through unchanged.
// Sub-issues are absorbed into their parent's GroupedIssue.
//
// Results are ordered: active epics first (by creation date), then active standalone
// issues, then completed epics, then completed standalone issues.
func GroupIssues(issues []*Issue) []GroupedIssue {
	if len(issues) == 0 {
		return nil
	}

	// Index issues by number for parent lookup.
	byNumber := make(map[int]*Issue, len(issues))
	for _, issue := range issues {
		byNumber[issue.Number] = issue
	}

	// Identify parent→children relationships.
	// Key: parent issue number, Value: child issues.
	children := make(map[int][]*Issue)
	// Track which issues are children (so we don't also list them standalone).
	isChild := make(map[int]bool)

	for _, issue := range issues {
		parentNum := ParseParentIssueNumber(issue.Body)
		if parentNum > 0 && byNumber[parentNum] != nil {
			// Only treat as a child if the parent is in the input set.
			// Orphan references (parent not in list) are treated as standalone.
			children[parentNum] = append(children[parentNum], issue)
			isChild[issue.Number] = true
		}
	}

	var result []GroupedIssue

	// Process all issues that are NOT children.
	for _, issue := range issues {
		if isChild[issue.Number] {
			continue
		}

		subs := children[issue.Number]
		if len(subs) > 0 {
			// This is an epic (parent with sub-issues).
			// Sort sub-issues by creation date (oldest first).
			sort.Slice(subs, func(i, j int) bool {
				return subs[i].CreatedAt.Before(subs[j].CreatedAt)
			})

			total := len(subs)
			done := countDone(subs)

			result = append(result, GroupedIssue{
				Issue:     issue,
				SubIssues: subs,
				TotalSubs: total,
				DoneSubs:  done,
				IsEpic:    true,
				IsActive:  done < total,
			})
		} else {
			// Standalone issue.
			result = append(result, GroupedIssue{
				Issue:    issue,
				IsEpic:   false,
				IsActive: isIssueOpen(issue),
			})
		}
	}

	// Sort: active items first, then completed.
	// Within each group, sort by creation date (oldest first).
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].IsActive != result[j].IsActive {
			return result[i].IsActive // active before completed
		}
		// Within same active/completed group, epics before standalone.
		if result[i].IsEpic != result[j].IsEpic {
			return result[i].IsEpic
		}
		return result[i].Issue.CreatedAt.Before(result[j].Issue.CreatedAt)
	})

	return result
}

// countDone counts sub-issues that are completed.
// An issue is "done" if its state is "closed" or it has the pilot-done label.
func countDone(issues []*Issue) int {
	count := 0
	for _, issue := range issues {
		if isIssueDone(issue) {
			count++
		}
	}
	return count
}

// isIssueDone checks if an issue is completed.
func isIssueDone(issue *Issue) bool {
	if strings.EqualFold(issue.State, "closed") {
		return true
	}
	return HasLabel(issue, LabelDone)
}

// isIssueOpen checks if an issue is still active.
func isIssueOpen(issue *Issue) bool {
	return strings.EqualFold(issue.State, "open") && !HasLabel(issue, LabelDone)
}
