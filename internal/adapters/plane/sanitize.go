package plane

import (
	"log/slog"

	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/text"
)

// sanitizeWorkItemInPlace strips invisible Unicode format characters
// from the work item's untrusted text fields (Name, Description)
// before it is handed to any downstream consumer. Emits a slog.Warn
// when any runes are stripped — attack-in-progress signal.
func sanitizeWorkItemInPlace(item *WorkItem) {
	if item == nil {
		return
	}
	var nameStripped, descStripped int
	item.Name, nameStripped = text.SanitizeUntrusted(item.Name)
	item.Description, descStripped = text.SanitizeUntrusted(item.Description)

	if nameStripped+descStripped > 0 {
		logging.WithComponent("plane").Warn(
			"invisible_unicode_stripped",
			slog.String("source", "plane"),
			slog.String("workitem", item.ID),
			slog.Int("name_stripped", nameStripped),
			slog.Int("description_stripped", descStripped),
		)
	}
}
