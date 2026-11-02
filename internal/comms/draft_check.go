package comms

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/ghissue"
)

var (
	acceptanceHeadingRE = regexp.MustCompile(`(?i)^##\s+Acceptance\b`)
	h2HeadingRE         = regexp.MustCompile(`^##\s`)
	bulletLineRE        = regexp.MustCompile(`^\s*[-*]\s+\S`)
)

// checkDrafts runs the shippable check on every draft in place: it fills
// Shippable and sets Kind to "ticket" when every rule passes and "epic"
// otherwise. There is no re-split: an epic-candidate keeps its body and the
// console files it without no-decompose, so executor decomposition splits it
// after approval. projectPath is the project checkout the drafting turn ran in.
func checkDrafts(drafts []TicketDraft, projectPath string) {
	known := make(map[string]bool, len(drafts))
	for _, d := range drafts {
		known[d.ID] = true
	}
	for i := range drafts {
		reasons := checkDraft(drafts[i], known, projectPath)
		drafts[i].Shippable = DraftShippable{OK: len(reasons) == 0, Reasons: reasons}
		drafts[i].Kind = DraftKindEpic
		if len(reasons) == 0 {
			drafts[i].Kind = DraftKindTicket
		}
	}
}

// checkDraft returns why a draft is not a single shippable ticket; empty means
// shippable. Pure Go, no LLM.
func checkDraft(d TicketDraft, known map[string]bool, projectPath string) []string {
	reasons := []string{}
	reasons = append(reasons, checkSpec(d)...)
	reasons = append(reasons, checkTitle(d)...)
	reasons = append(reasons, checkPathsExist(d, projectPath)...)
	reasons = append(reasons, checkAcceptanceDialect(d)...)
	reasons = append(reasons, checkDeps(d, known)...)
	return reasons
}

// checkSpec applies the dispatcher's spec gate. Labels are deliberately not
// passed: a pilot-skip-spec-check label would bypass the rule.
func checkSpec(d TicketDraft) []string {
	res := ghissue.ValidateSpec(&github.Issue{Title: d.Title, Body: d.Body}, nil)
	if res.Valid {
		return nil
	}
	out := make([]string, 0, len(res.FailureReasons))
	for _, r := range res.FailureReasons {
		out = append(out, "spec: "+r)
	}
	return out
}

func checkTitle(d TicketDraft) []string {
	if conventionalCommitTitleRE.MatchString(d.Title) {
		return nil
	}
	return []string{fmt.Sprintf("title %q is not a conventional-commit title", d.Title)}
}

// checkPathsExist requires every backticked path span of the body to exist in
// the project checkout. The drafting turn runs inside the project worktree, so
// a stat is enough.
func checkPathsExist(d TicketDraft, projectPath string) []string {
	var out []string
	for _, p := range executor.ExtractReferencedPaths(d.Body) {
		if !pathExistsIn(projectPath, p) {
			out = append(out, fmt.Sprintf("referenced path %q does not exist in the checkout", p))
		}
	}
	return out
}

func pathExistsIn(root, rel string) bool {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if root == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	_, err := os.Stat(filepath.Join(root, clean))
	return err == nil
}

// checkAcceptanceDialect requires at least one acceptance bullet the evidence
// gate can act on (SOP Rule 5b): a backticked allowlisted command with a
// paste-output phrase, or a mutation pin ("... -> TestX fails").
func checkAcceptanceDialect(d TicketDraft) []string {
	for _, bullet := range acceptanceBullets(d.Body) {
		if isEvidenceDialect(bullet) {
			return nil
		}
	}
	return []string{"## Acceptance has no bullet in the evidence dialect (a backticked allowlisted command with \"paste the output into the PR body\", or a mutation pin \"... -> TestName fails\")"}
}

func isEvidenceDialect(bullet string) bool {
	item := executor.ClassifyAcceptanceItem(bullet)
	switch item.Kind {
	case executor.AcceptanceItemMutation:
		return true
	case executor.AcceptanceItemPasteOutput:
		allowed := make(map[string]bool)
		for _, c := range executor.DefaultAcceptanceEvidenceAllowedCommands() {
			allowed[c] = true
		}
		for _, cmd := range item.Commands {
			if f := strings.Fields(cmd); len(f) > 0 && allowed[f[0]] {
				return true
			}
		}
	}
	return false
}

// acceptanceBullets returns the bullets under the body's "## Acceptance"
// heading, continuation lines joined to their bullet.
func acceptanceBullets(body string) []string {
	var bullets []string
	in := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, " \t\r")
		switch {
		case acceptanceHeadingRE.MatchString(line):
			in = true
		case h2HeadingRE.MatchString(line):
			in = false
		case !in || strings.TrimSpace(line) == "":
		case bulletLineRE.MatchString(line):
			bullets = append(bullets, strings.TrimSpace(line))
		case len(bullets) > 0:
			bullets[len(bullets)-1] += " " + strings.TrimSpace(line)
		}
	}
	return bullets
}

// checkDeps requires deps to name other drafts of the same report.
func checkDeps(d TicketDraft, known map[string]bool) []string {
	var out []string
	for _, dep := range d.Deps {
		switch {
		case dep == d.ID:
			out = append(out, fmt.Sprintf("dep %q is the draft itself", dep))
		case !known[dep]:
			out = append(out, fmt.Sprintf("dep %q is not a draft id of this report", dep))
		}
	}
	return out
}
