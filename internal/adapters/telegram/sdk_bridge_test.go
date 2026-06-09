package telegram

import (
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	sdkCore "github.com/qf-studio/studio-sdk/sdk/core"
	sdkTelegram "github.com/qf-studio/studio-sdk/sdk/integrations/telegram"
)

// The studio-sdk chat bridge must accept *telegram.Handler as its
// core.MessageHandler — this is the exact wiring cmd/pilot/main.go performs when
// sdk_bridge is enabled (GH-3470 Phase 6). Guards against an SDK API drift that
// would only otherwise surface at daemon start.
func TestSDKChatBridge_AcceptsHandler(t *testing.T) {
	h := &Handler{} // zero value: HandleMessage tolerates a nil comms handler
	bridge := sdkTelegram.New(sdkTelegram.Config{
		BotToken: testutil.FakeTelegramBotToken,
	}, nil).NewChatBridge(sdkCore.ChatDeps{Handler: h})
	if bridge == nil {
		t.Fatal("NewChatBridge returned nil for a valid *Handler")
	}
}

// sdk_bridge must default off so existing deployments keep the long-poll path
// (non-breaking opt-in).
func TestConfig_SDKBridgeDefaultsOff(t *testing.T) {
	var c Config
	if c.SDKBridge {
		t.Error("Config.SDKBridge should default to false (non-breaking)")
	}
}
