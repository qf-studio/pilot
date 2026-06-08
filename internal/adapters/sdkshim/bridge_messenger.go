package sdkshim

import (
	"context"
	"fmt"

	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/studio-sdk/sdk/core"
)

// Compile-time assertion that bridgeMessenger implements comms.Messenger.
var _ comms.Messenger = (*bridgeMessenger)(nil)

// bridgeMessenger adapts a core.ChatBridge to comms.Messenger so that
// comms.Handler can delegate outbound calls to the SDK bridge.
type bridgeMessenger struct {
	bridge core.ChatBridge
}

// MessengerToBridge wraps a core.ChatBridge as a comms.Messenger. All
// outbound calls are delegated to the bridge's Send / Edit / Ack methods.
func MessengerToBridge(b core.ChatBridge) comms.Messenger {
	return &bridgeMessenger{bridge: b}
}

func (m *bridgeMessenger) SendText(ctx context.Context, contextID, text string) error {
	_, err := m.bridge.Send(ctx, core.OutboundMessage{
		ChannelID: contextID,
		Text:      text,
	})
	return err
}

// SendConfirmation posts a confirmation prompt with Execute / Cancel buttons.
// The button ActionID and Data are both set to "execute" / "cancel" so that
// the SDK bridge callback event delivers ev.Data == "execute" and the sdkshim
// maps it to comms.IncomingMessage.ActionID without normalization.
func (m *bridgeMessenger) SendConfirmation(ctx context.Context, contextID, threadID, taskID, desc, project string) (string, error) {
	text := fmt.Sprintf("*Task %s*\n\n%s", taskID, desc)
	if project != "" {
		text = fmt.Sprintf("*Task %s* · _%s_\n\n%s", taskID, project, desc)
	}
	ref, err := m.bridge.Send(ctx, core.OutboundMessage{
		ChannelID: contextID,
		ThreadID:  threadID,
		Text:      text,
		Buttons: []core.Button{
			{Label: "Execute ✅", ActionID: "execute", Data: "execute"},
			{Label: "Cancel ❌", ActionID: "cancel", Data: "cancel"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("send confirmation: %w", err)
	}
	return MessageRefToString(ref), nil
}

// SendProgress updates the existing progress message when messageRef is set,
// or posts a new one when it is empty (first call in an execution).
func (m *bridgeMessenger) SendProgress(ctx context.Context, contextID, messageRef, taskID, phase string, progress int, detail string) (string, error) {
	text := fmt.Sprintf("⚙️ *%s* · %s %d%%\n%s", taskID, phase, progress, detail)
	ref := MessageRefFromString(messageRef)
	if ref.MessageID != "" {
		if err := m.bridge.Edit(ctx, core.MessageRef{
			ChannelID: contextID,
			MessageID: ref.MessageID,
			ThreadID:  ref.ThreadID,
		}, text); err != nil {
			return messageRef, fmt.Errorf("update progress: %w", err)
		}
		return messageRef, nil
	}
	newRef, err := m.bridge.Send(ctx, core.OutboundMessage{
		ChannelID: contextID,
		Text:      text,
	})
	if err != nil {
		return "", fmt.Errorf("send progress: %w", err)
	}
	return MessageRefToString(newRef), nil
}

func (m *bridgeMessenger) SendResult(ctx context.Context, contextID, threadID, taskID string, success bool, output, prURL string) error {
	status := "✅"
	if !success {
		status = "❌"
	}
	text := fmt.Sprintf("%s *Task %s completed*", status, taskID)
	if prURL != "" {
		text += fmt.Sprintf("\n<%s|View PR>", prURL)
	}
	if output != "" {
		text += "\n\n" + output
	}
	_, err := m.bridge.Send(ctx, core.OutboundMessage{
		ChannelID: contextID,
		ThreadID:  threadID,
		Text:      text,
	})
	return err
}

func (m *bridgeMessenger) SendChunked(ctx context.Context, contextID, threadID, content, prefix string) error {
	chunks := bridgeSplitChunks(content, m.MaxMessageLength())
	for i, chunk := range chunks {
		text := chunk
		if prefix != "" && i == 0 {
			text = prefix + "\n\n" + chunk
		}
		if _, err := m.bridge.Send(ctx, core.OutboundMessage{
			ChannelID: contextID,
			ThreadID:  threadID,
			Text:      text,
		}); err != nil {
			return fmt.Errorf("send chunk %d: %w", i, err)
		}
	}
	return nil
}

func (m *bridgeMessenger) AcknowledgeCallback(ctx context.Context, callbackID string) error {
	return m.bridge.Ack(ctx, callbackID)
}

func (m *bridgeMessenger) MaxMessageLength() int {
	return 3800
}

// bridgeSplitChunks splits s into chunks of at most maxLen bytes, preferring
// newline boundaries. Empty input returns a single empty-string chunk so
// callers always have at least one message to send.
func bridgeSplitChunks(s string, maxLen int) []string {
	if len(s) == 0 {
		return []string{""}
	}
	var chunks []string
	for len(s) > maxLen {
		idx := maxLen
		for idx > 0 && s[idx-1] != '\n' {
			idx--
		}
		if idx == 0 {
			idx = maxLen
		}
		chunks = append(chunks, s[:idx])
		s = s[idx:]
	}
	if s != "" {
		chunks = append(chunks, s)
	}
	return chunks
}
