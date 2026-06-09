package telegram

import (
	"context"

	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/studio-sdk/sdk/core"
)

// Handler implements the studio-sdk chat contract (core.MessageHandler) so the
// Telegram poll path can be driven by the SDK chat bridge instead of the local
// long-poll loop (M7 Phase 6, GH-3470). The bridge calls HandleMessage for each
// normalized inbound event; Pilot's existing photo/voice handling stays on the
// host-side path and fills ImagePath/VoiceText there — only inbound text flows
// through this shim.
var _ core.MessageHandler = (*Handler)(nil)

// messageEventToIncoming converts a normalized SDK chat event into Pilot's
// comms.IncomingMessage.
//
// The conversion mirrors sdkshim.MessageEventToIncomingMessage but is inlined
// here: telegram → sdkshim → config → telegram is an import cycle (config.go
// imports the telegram package), so telegram cannot import sdkshim. The Slack
// adapter inlines the same conversion for the identical reason — see
// slack/handler.go HandleMessage.
//
// Telegram sender IDs arrive from sdk/integrations/telegram as a stringified
// int64, and Telegram's MemberResolver consumes them as strings, so the SDK
// UserID is used as-is (pass-through).
func messageEventToIncoming(ev core.MessageEvent) *comms.IncomingMessage {
	msg := &comms.IncomingMessage{
		ContextID:  ev.ChannelID,
		SenderID:   ev.Sender.UserID,
		SenderName: ev.Sender.DisplayName,
		Text:       ev.Text,
		ThreadID:   ev.ThreadID,
		Platform:   "telegram",
		RawEvent:   &ev,
	}
	if ev.Action == "callback" {
		msg.IsCallback = true
		msg.CallbackID = ev.CallbackID
		// ev.Data carries the button payload (e.g. "approve:TASK123"); comms
		// routes it via ActionID. Per-platform normalization (execute/cancel)
		// happens inside comms.Handler.
		msg.ActionID = ev.Data
	}
	return msg
}

// HandleMessage implements core.MessageHandler. It converts the normalized SDK
// event to comms.IncomingMessage and delegates to the shared comms.Handler,
// matching the Slack/Discord chat-contract adapters.
func (h *Handler) HandleMessage(ctx context.Context, ev core.MessageEvent) error {
	if h.commsHandler != nil {
		h.commsHandler.HandleMessage(ctx, messageEventToIncoming(ev))
	}
	return nil
}

// SetCommsHandler wires the shared comms handler after construction. Used when
// the bridge messenger must be created before the comms handler, avoiding the
// chicken-and-egg dependency between the bridge and its Messenger.
func (h *Handler) SetCommsHandler(ch *comms.Handler) {
	h.commsHandler = ch
}
