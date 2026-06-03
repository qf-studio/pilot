package sdkshim

import (
	"strings"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// messageRef encoding.
//
// Pilot's comms.Messenger interface returns/accepts string handles
// (`messageRef string`), but the SDK's core.MessageRef is a struct with
// three fields (ChannelID, MessageID, ThreadID). To preserve Pilot's
// existing public surface during M7 we encode/decode at the shim boundary.
//
// Encoding: "<channelID>\x1f<messageID>\x1f<threadID>"
// We use U+001F (ASCII Unit Separator) — never appears in chat IDs across
// slack/telegram/discord by construction, so no escaping is needed.
//
// Callers should NEVER parse messageRef strings themselves; round-trip
// through these helpers.
const messageRefSep = "\x1f"

// MessageRefToString encodes a core.MessageRef as Pilot's string handle.
func MessageRefToString(ref core.MessageRef) string {
	return ref.ChannelID + messageRefSep + ref.MessageID + messageRefSep + ref.ThreadID
}

// MessageRefFromString decodes Pilot's string handle back into a
// core.MessageRef. Returns the zero MessageRef when s is empty or malformed
// (callers can treat that as "no previous message" — comms.Messenger today
// already accepts empty refs).
func MessageRefFromString(s string) core.MessageRef {
	if s == "" {
		return core.MessageRef{}
	}
	parts := strings.SplitN(s, messageRefSep, 3)
	if len(parts) != 3 {
		// Legacy or malformed handle — treat ChannelID as the whole string
		// so existing callers that passed a bare channelID degrade gracefully.
		return core.MessageRef{ChannelID: s}
	}
	return core.MessageRef{
		ChannelID: parts[0],
		MessageID: parts[1],
		ThreadID:  parts[2],
	}
}

// ComposeOutboundMessage builds a core.OutboundMessage from Pilot's primitive
// inputs (channel + thread + text + buttons). Buttons are passed as parallel
// label/actionID/data slices — Phases 6/7/8 will likely replace this with a
// richer builder once each platform's button-set shape is settled. For Phase 0
// the function is purely structural so downstream code can compile against
// the expected signature.
func ComposeOutboundMessage(channelID, threadID, text string, buttonLabels, buttonActionIDs, buttonData []string) core.OutboundMessage {
	out := core.OutboundMessage{
		ChannelID: channelID,
		ThreadID:  threadID,
		Text:      text,
	}
	n := len(buttonLabels)
	if n > len(buttonActionIDs) {
		n = len(buttonActionIDs)
	}
	if n > len(buttonData) {
		n = len(buttonData)
	}
	if n == 0 {
		return out
	}
	out.Buttons = make([]core.Button, 0, n)
	for i := 0; i < n; i++ {
		out.Buttons = append(out.Buttons, core.Button{
			Label:    buttonLabels[i],
			ActionID: buttonActionIDs[i],
			Data:     buttonData[i],
		})
	}
	return out
}
