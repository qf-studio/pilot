package linear

import (
	"log/slog"

	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/text"
)

// sanitizeIssueInPlace strips invisible Unicode format characters from
// the issue's untrusted text fields (Title, Description) before the
// issue is handed to any downstream consumer (pilot callback, memory
// store, prompt builder). Emits a slog.Warn when any runes are
// stripped — this is the attack-in-progress signal.
func sanitizeIssueInPlace(issue *Issue) {
	if issue == nil {
		return
	}
	var titleStripped, descStripped int
	issue.Title, titleStripped = text.SanitizeUntrusted(issue.Title)
	issue.Description, descStripped = text.SanitizeUntrusted(issue.Description)

	if titleStripped+descStripped > 0 {
		logging.WithComponent("linear").Warn(
			"invisible_unicode_stripped",
			slog.String("source", "linear"),
			slog.String("issue", issue.Identifier),
			slog.Int("title_stripped", titleStripped),
			slog.Int("description_stripped", descStripped),
		)
	}
}
