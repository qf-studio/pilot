// Package comms defines shared communication contracts for adapter implementations.
package comms

import (
	"context"
	"time"
)

// IncomingMessage is the platform-agnostic representation of an inbound user message.
type IncomingMessage struct {
	ContextID  string      // chatID / channelID
	SenderID   string      // user ID (string; adapters convert int64)
	Text       string      // normalized message text
	ThreadID   string      // thread context (Slack threadTS, Telegram reply, etc.)
	ImagePath  string      // downloaded image path (optional)
	VoiceText  string      // transcribed voice text (optional)
	IsCallback bool        // true when this is a button callback
	CallbackID string      // platform callback ID
	ActionID   string      // button action ID
	RawEvent   interface{} // platform-specific escape hatch
}

// Messenger is the interface every chat adapter must implement for outbound messaging.
type Messenger interface {
	// SendText sends a plain text message to the given context (channel/chat).
	SendText(ctx context.Context, contextID, text string) error

	// SendConfirmation sends a task confirmation prompt with approve/reject buttons.
	// Returns a messageRef that can be used to update the message later.
	SendConfirmation(ctx context.Context, contextID, threadID, taskID, desc, project string) (messageRef string, err error)

	// SendProgress updates an existing message (identified by messageRef) with progress info.
	// Returns a new messageRef if the platform creates a new message.
	SendProgress(ctx context.Context, contextID, messageRef, taskID, phase string, progress int, detail string) (newRef string, err error)

	// SendResult sends the final task result (success or failure).
	SendResult(ctx context.Context, contextID, threadID, taskID string, success bool, output, prURL string) error

	// SendChunked sends long content split into platform-appropriate chunks.
	SendChunked(ctx context.Context, contextID, threadID, content, prefix string) error

	// AcknowledgeCallback responds to a button/callback interaction.
	AcknowledgeCallback(ctx context.Context, callbackID string) error

	// MaxMessageLength returns the platform's maximum single-message length.
	MaxMessageLength() int
}

// PendingTask represents a task awaiting user confirmation.
type PendingTask struct {
	TaskID      string
	Description string
	ContextID   string // chatID or channelID
	ThreadID    string // threadTS or empty
	MessageRef  string // platform message ID for later updates
	SenderID    string // user who requested the task (for RBAC)
	CreatedAt   time.Time
}

// RunningTask represents a task currently being executed.
type RunningTask struct {
	TaskID    string
	ContextID string
	StartedAt time.Time
	Cancel    context.CancelFunc
}
