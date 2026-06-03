package sdkshim

import (
	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/studio-sdk/sdk/core"
)

// Platform identifiers used in comms.IncomingMessage.Platform.
const (
	PlatformSlack    = "slack"
	PlatformTelegram = "telegram"
	PlatformDiscord  = "discord"
)

// SenderIDConverter maps an SDK Identity.UserID (opaque platform-specific
// string) into the form Pilot's comms.MemberResolver / teams expect.
//
// Slack/Discord IDs are already strings — pass-through is correct.
// Telegram IDs arrive as a stringified int64 from sdk/integrations/telegram;
// the Telegram MemberResolver consumes them as strings too, so pass-through
// is also correct there. The hook exists for future platforms where the
// canonical form differs from the SDK's wire form.
type SenderIDConverter func(sdkUserID string) string

// PassThroughSenderID is the identity converter — appropriate for Slack and
// Discord today. Telegram also uses pass-through; if a future MemberResolver
// requires int64 parsing, swap in a custom converter at wiring time.
func PassThroughSenderID(id string) string { return id }

// MessageEventToIncomingMessage shims a normalized SDK chat event into Pilot's
// comms.IncomingMessage. The single shim covers slack/telegram/discord with
// per-platform sender-ID conversion when needed.
//
// Conventions:
//   - core.MessageEvent.Text is pre-sanitized by the SDK adapter (live path).
//   - Action ∈ {"message","command","callback"}: callback maps IsCallback=true
//     plus CallbackID + ActionID (from Button.ActionID; the SDK exposes the
//     callback ID via the dedicated CallbackID field). "command" is left as a
//     plain message for Phase 0; Phase 6/7/8 will route Command/Args through
//     comms.CommandHandler instead of the local Telegram parser.
//   - Pilot-specific fields (ImagePath, VoiceText) are NOT in core.MessageEvent.
//     The Telegram adapter retains photo + voice handling outside this shim
//     and is responsible for filling those fields on its own host-side path.
//   - Timestamp: core.MessageEvent has no timestamp; we leave it zero. Callers
//     that need wall-clock time stamp it themselves on receipt.
//
// Phase 0: signature + minimal mapping. Phase 6 (Telegram) hardens the command
// routing decision; Phases 7/8 add Slack and Discord wiring.
func MessageEventToIncomingMessage(ev *core.MessageEvent, platform string, conv SenderIDConverter) *comms.IncomingMessage {
	if ev == nil {
		return nil
	}
	if conv == nil {
		conv = PassThroughSenderID
	}
	msg := &comms.IncomingMessage{
		ContextID:  ev.ChannelID,
		SenderID:   conv(ev.Sender.UserID),
		SenderName: ev.Sender.DisplayName,
		Text:       ev.Text,
		ThreadID:   ev.ThreadID,
		Platform:   platform,
		RawEvent:   ev,
	}
	if ev.Action == "callback" {
		msg.IsCallback = true
		msg.CallbackID = ev.CallbackID
		// ActionID is conventionally encoded in ev.Data (e.g. "approve:TASK123").
		// Pilot's existing per-platform code normalizes "execute_task" → "execute"
		// before this shim — at the SDK boundary we just forward the raw payload
		// and let comms route it.
		msg.ActionID = ev.Data
	}
	return msg
}
