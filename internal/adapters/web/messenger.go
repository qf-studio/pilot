package web

import (
	"context"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/comms"
)

// maxEventsPerConversation is the drop-oldest cap on a single conversation's
// event buffer (GH-4835 spec). Chosen generously above any realistic single
// task's event count (confirmation + progress edits + result rarely exceeds
// a few dozen entries) so drop-oldest is a safety valve, not a steady-state
// occurrence.
const maxEventsPerConversation = 500

// conversationExpiry is how long a conversation's buffer survives with no
// new events before it is eligible for eviction (GH-4835 spec: "1h
// inactivity"). Checked lazily on access, not via a background sweep — see
// pruneLocked.
const conversationExpiry = time.Hour

// MaxMessageLength is the cap WebMessenger reports via comms.Messenger. The
// buffer is in-memory JSON, not a platform with a real transport limit, so
// this is set high enough that comms' chunking logic rarely engages.
const MaxMessageLength = 32768

// EventType enumerates the wire "type" field of Event.
type EventType string

const (
	EventText         EventType = "text"
	EventConfirmation EventType = "confirmation"
	EventProgress     EventType = "progress"
	EventResult       EventType = "result"
)

// Event is one entry in a conversation's outbound timeline, served verbatim
// by GET /api/v1/chat/conversations/{id}/events. Seq is monotonically
// increasing per conversation, starting at 1 (0 is never a valid seq, so
// ?after=0 always means "from the beginning" — see API.Events).
//
// Restart semantics (GH-4835 spec): buffers are in-memory only. A daemon
// restart wipes all conversations and seq numbering restarts at 0/1. A
// client that polls with an `after` value greater than the newest seq
// currently held (i.e. it remembers more history than the server has) must
// treat that as a buffer reset and re-poll from after=0, since there is no
// way for the server to distinguish "you're caught up" from "I forgot
// everything before your bookmark" — both look like "nothing new past your
// cursor" from small `after` values, but a client-side cursor larger than
// the newest known seq is the unambiguous signal.
type Event struct {
	Seq        int64     `json:"seq"`
	Type       EventType `json:"type"`
	Text       string    `json:"text,omitempty"`
	TaskID     string    `json:"taskId,omitempty"`
	Phase      string    `json:"phase,omitempty"`
	Progress   *float64  `json:"progress,omitempty"`
	PRUrl      string    `json:"prUrl,omitempty"`
	Success    *bool     `json:"success,omitempty"`
	MessageRef string    `json:"messageRef,omitempty"`
	OccurredAt time.Time `json:"occurredAt"`
}

type conversation struct {
	events     []Event
	nextSeq    int64
	lastActive time.Time
}

// WebMessenger implements comms.Messenger by appending typed events to a
// per-conversation in-memory buffer, polled by GET
// /api/v1/chat/conversations/{id}/events (GH-4835). contextID here is always
// the comms ContextID ("web:" + conversationID, per the API assembly in
// gateway/chat.go) — WebMessenger strips no prefix and stores buffers keyed
// on the raw contextID it's given, so the caller (API) is responsible for
// using the same contextID both when dispatching HandleMessage and when
// looking up events for a conversationID.
type WebMessenger struct {
	mu            sync.Mutex
	conversations map[string]*conversation
	now           func() time.Time // seam for tests
}

var _ comms.Messenger = (*WebMessenger)(nil)

// NewMessenger creates an empty WebMessenger.
func NewMessenger() *WebMessenger {
	return &WebMessenger{
		conversations: make(map[string]*conversation),
		now:           time.Now,
	}
}

func (m *WebMessenger) conversationLocked(contextID string) *conversation {
	c, ok := m.conversations[contextID]
	if !ok {
		c = &conversation{}
		m.conversations[contextID] = c
	}
	return c
}

// appendLocked appends ev to contextID's buffer, assigning it the next seq,
// enforcing the drop-oldest cap, and bumping lastActive. Returns the
// assigned seq.
func (m *WebMessenger) appendLocked(contextID string, ev Event) int64 {
	c := m.conversationLocked(contextID)
	c.nextSeq++
	ev.Seq = c.nextSeq
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = m.now()
	}
	c.events = append(c.events, ev)
	if len(c.events) > maxEventsPerConversation {
		c.events = c.events[len(c.events)-maxEventsPerConversation:]
	}
	c.lastActive = m.now()
	return ev.Seq
}

// pruneLocked evicts conversations that have been inactive past
// conversationExpiry. Called lazily from the read/write paths rather than a
// background goroutine — the buffer is small and operator-facing, so a sweep
// on access is sufficient and avoids a timer to manage across daemon
// lifecycle.
func (m *WebMessenger) pruneLocked() {
	cutoff := m.now().Add(-conversationExpiry)
	for id, c := range m.conversations {
		if c.lastActive.Before(cutoff) {
			delete(m.conversations, id)
		}
	}
}

// SendText appends a plain text event.
func (m *WebMessenger) SendText(_ context.Context, contextID, _threadID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	m.appendLocked(contextID, Event{Type: EventText, Text: text})
	return nil
}

// SendConfirmation appends a confirmation event and returns taskID as the
// messageRef — later SendProgress calls for the same task reuse it so a
// client can group/replace-in-place by messageRef, mirroring Telegram's
// edit-in-place semantics without a real "edit" operation on a poll-drain
// buffer.
func (m *WebMessenger) SendConfirmation(_ context.Context, contextID, _threadID, taskID, desc, project string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	text := desc
	if project != "" {
		text = desc + " (" + project + ")"
	}
	m.appendLocked(contextID, Event{
		Type:       EventConfirmation,
		Text:       text,
		TaskID:     taskID,
		MessageRef: taskID,
	})
	return taskID, nil
}

// SendProgress appends a progress event. When messageRef is empty (the
// initial progress call for a task — see comms.Handler.executeTaskCore) it
// defaults to taskID, same as SendConfirmation, so all of a task's progress
// events plus its opening confirmation share one messageRef.
func (m *WebMessenger) SendProgress(_ context.Context, contextID, messageRef, taskID, phase string, progress int, detail string) (string, error) {
	if messageRef == "" {
		messageRef = taskID
	}
	pct := float64(progress) / 100.0
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	m.appendLocked(contextID, Event{
		Type:       EventProgress,
		Text:       detail,
		TaskID:     taskID,
		Phase:      phase,
		Progress:   &pct,
		MessageRef: messageRef,
	})
	return messageRef, nil
}

// SendResult appends a result event carrying the final success/output/PR URL.
func (m *WebMessenger) SendResult(_ context.Context, contextID, _threadID, taskID string, success bool, output, prURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	s := success
	m.appendLocked(contextID, Event{
		Type:       EventResult,
		Text:       output,
		TaskID:     taskID,
		PRUrl:      prURL,
		Success:    &s,
		MessageRef: taskID,
	})
	return nil
}

// SendChunked appends chunks as sequential text events. WebMessenger's
// MaxMessageLength (32768) is high enough that comms rarely calls this in
// practice, but the interface requires an implementation.
func (m *WebMessenger) SendChunked(ctx context.Context, contextID, _threadID, content, prefix string) error {
	text := content
	if prefix != "" {
		text = prefix + content
	}
	return m.SendText(ctx, contextID, _threadID, text)
}

// AcknowledgeCallback is a no-op: there is no platform-level callback
// acknowledgement to send for a poll-drain HTTP transport (mirrors Slack's
// implementation, internal/adapters/slack/messenger.go).
func (m *WebMessenger) AcknowledgeCallback(context.Context, string) error {
	return nil
}

// MaxMessageLength returns the configured cap.
func (m *WebMessenger) MaxMessageLength() int {
	return MaxMessageLength
}

// Events returns the events for contextID with seq > after, in seq order,
// along with the newest seq currently known for the conversation (0 if the
// conversation doesn't exist / has no events). It does not mutate state
// beyond lazy pruning.
func (m *WebMessenger) Events(contextID string, after int64) (events []Event, latestSeq int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	c, ok := m.conversations[contextID]
	if !ok {
		// GH-4843 D3: an unknown/expired conversation must still serialize
		// as `"events": []`, not `null` — a nil slice here propagates all
		// the way to json.Marshal in the gateway handler.
		return []Event{}, 0
	}
	latestSeq = c.nextSeq
	out := make([]Event, 0, len(c.events))
	for _, ev := range c.events {
		if ev.Seq > after {
			out = append(out, ev)
		}
	}
	return out, latestSeq
}

// LatestSeq returns the newest seq currently held for contextID (0 if the
// conversation doesn't exist yet). Used by API.Dispatch to report the
// accept-time seq in the 202 response.
func (m *WebMessenger) LatestSeq(contextID string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conversations[contextID]
	if !ok {
		return 0
	}
	return c.nextSeq
}
