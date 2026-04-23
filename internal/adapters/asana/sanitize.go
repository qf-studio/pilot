package asana

import (
	"log/slog"

	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/text"
)

// sanitizeTaskInPlace strips invisible Unicode format characters from
// the task's untrusted text fields (Name, Notes, HTMLNotes) before the
// task is handed to any downstream consumer. Emits a slog.Warn when
// any runes are stripped — attack-in-progress signal.
func sanitizeTaskInPlace(task *Task) {
	if task == nil {
		return
	}
	var nameStripped, notesStripped, htmlStripped int
	task.Name, nameStripped = text.SanitizeUntrusted(task.Name)
	task.Notes, notesStripped = text.SanitizeUntrusted(task.Notes)
	task.HTMLNotes, htmlStripped = text.SanitizeUntrusted(task.HTMLNotes)

	if nameStripped+notesStripped+htmlStripped > 0 {
		logging.WithComponent("asana").Warn(
			"invisible_unicode_stripped",
			slog.String("source", "asana"),
			slog.String("task", task.GID),
			slog.Int("name_stripped", nameStripped),
			slog.Int("notes_stripped", notesStripped),
			slog.Int("html_notes_stripped", htmlStripped),
		)
	}
}
