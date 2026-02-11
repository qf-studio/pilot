package telegram

import (
	"github.com/alekspetrov/pilot/internal/intent"
)

// Intent re-exports the intent.Intent type for backward compatibility
type Intent = intent.Intent

// Intent constants re-exported for backward compatibility
const (
	IntentCommand  = intent.IntentCommand
	IntentGreeting = intent.IntentGreeting
	IntentResearch = intent.IntentResearch
	IntentPlanning = intent.IntentPlanning
	IntentQuestion = intent.IntentQuestion
	IntentChat     = intent.IntentChat
	IntentTask     = intent.IntentTask
)

// DetectIntent re-exports intent.DetectIntent for backward compatibility
func DetectIntent(message string) Intent {
	return intent.DetectIntent(message)
}

// IsEphemeralTask re-exports intent.IsEphemeralTask for backward compatibility
func IsEphemeralTask(description string) bool {
	return intent.IsEphemeralTask(description)
}

// isClearQuestion wraps intent.IsClearQuestion for internal use in handler.go
func isClearQuestion(msg string) bool {
	return intent.IsClearQuestion(msg)
}
