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

// DetectIntent re-exports intent.DetectIntent for backward compatibility.
// Use type alias (Option A from GH-644 spec).
var DetectIntent = intent.DetectIntent

// IsEphemeralTask re-exports intent.IsEphemeralTask for backward compatibility.
// Use type alias (Option A from GH-644 spec).
var IsEphemeralTask = intent.IsEphemeralTask

// IsClearQuestion re-exports intent.IsClearQuestion for backward compatibility.
// Use type alias (Option A from GH-644 spec).
var IsClearQuestion = intent.IsClearQuestion
