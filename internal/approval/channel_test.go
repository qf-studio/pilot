package approval

import (
	"context"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

func TestNormalizeChannelName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"github-review alias maps to github", "github-review", "github"},
		{"telegram passes through", "telegram", "telegram"},
		{"slack passes through", "slack", "slack"},
		{"github passes through", "github", "github"},
		{"empty passes through", "", ""},
		{"unknown passes through unchanged", "webhook", "webhook"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeChannelName(tt.in); got != tt.want {
				t.Errorf("NormalizeChannelName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestOwnsChannel(t *testing.T) {
	tests := []struct {
		name             string
		handlerName      string
		preferredChannel string
		want             bool
	}{
		{"telegram owns its own explicit rows", "telegram", "telegram", true},
		{"telegram owns legacy empty rows", "telegram", "", true},
		{"slack does not own legacy empty rows", "slack", "", false},
		{"slack owns its own explicit rows", "slack", "slack", true},
		{"github owns rows via the github-review alias", "github", "github-review", true},
		{"telegram does not own slack rows", "telegram", "slack", false},
		{"slack does not own telegram rows", "slack", "telegram", false},
		{"telegram does not own unknown-channel rows", "telegram", "webhook", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ownsChannel(tt.handlerName, tt.preferredChannel); got != tt.want {
				t.Errorf("ownsChannel(%q, %q) = %v, want %v", tt.handlerName, tt.preferredChannel, got, tt.want)
			}
		})
	}
}

func TestOwnedChannels(t *testing.T) {
	if got := ownedChannels("telegram"); len(got) != 2 || got[0] != "telegram" || got[1] != "" {
		t.Errorf("ownedChannels(telegram) = %v, want [telegram \"\"]", got)
	}
	if got := ownedChannels("slack"); len(got) != 1 || got[0] != "slack" {
		t.Errorf("ownedChannels(slack) = %v, want [slack]", got)
	}
}

// TestRehydrate_MixedChannelScenario is the GH-4772 acceptance-criterion-5
// regression test: it seeds one row per channel type — telegram, slack,
// legacy (empty PreferredChannel), and an unknown/orphaned channel name —
// and verifies that each handler's Rehydrate only ever claims the rows it
// owns. The legacy row must be claimed by telegram (the documented default
// legacy-channel owner) and the unknown-channel row must be claimed by
// neither.
func TestRehydrate_MixedChannelScenario(t *testing.T) {
	store := newMockPendingStore()
	future := time.Now().Add(time.Hour)

	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "row-telegram", TaskID: "T-telegram", Stage: "pre_merge",
		Title: "Telegram row", PreferredChannel: "telegram", CreatedAt: time.Now(), ExpiresAt: future,
	})
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "row-slack", TaskID: "T-slack", Stage: "pre_merge",
		Title: "Slack row", PreferredChannel: "slack", CreatedAt: time.Now(), ExpiresAt: future,
	})
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "row-legacy", TaskID: "T-legacy", Stage: "pre_merge",
		Title: "Legacy row", PreferredChannel: "", CreatedAt: time.Now(), ExpiresAt: future,
	})
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "row-unknown", TaskID: "T-unknown", Stage: "pre_merge",
		Title: "Unknown-channel row", PreferredChannel: "webhook", CreatedAt: time.Now(), ExpiresAt: future,
	})

	telegramHandler := NewTelegramHandler(&mockTelegramClient{}, "chat123").WithStore(store)
	if err := telegramHandler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("telegram rehydrate: unexpected error: %v", err)
	}

	slackHandler := NewSlackHandler(&mockSlackClient{}, "#approvals").WithStore(store)
	if err := slackHandler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("slack rehydrate: unexpected error: %v", err)
	}

	telegramHandler.mu.RLock()
	_, tgGotTelegram := telegramHandler.pending["row-telegram"]
	_, tgGotSlack := telegramHandler.pending["row-slack"]
	_, tgGotLegacy := telegramHandler.pending["row-legacy"]
	_, tgGotUnknown := telegramHandler.pending["row-unknown"]
	telegramHandler.mu.RUnlock()

	if !tgGotTelegram {
		t.Error("expected telegram Rehydrate to claim its own row")
	}
	if tgGotSlack {
		t.Error("expected telegram Rehydrate to skip the slack-owned row")
	}
	if !tgGotLegacy {
		t.Error("expected telegram Rehydrate to claim the legacy empty-channel row (default owner)")
	}
	if tgGotUnknown {
		t.Error("expected telegram Rehydrate to skip the unknown-channel row")
	}

	slackHandler.mu.RLock()
	_, skGotTelegram := slackHandler.pending["row-telegram"]
	_, skGotSlack := slackHandler.pending["row-slack"]
	_, skGotLegacy := slackHandler.pending["row-legacy"]
	_, skGotUnknown := slackHandler.pending["row-unknown"]
	slackHandler.mu.RUnlock()

	if skGotTelegram {
		t.Error("expected slack Rehydrate to skip the telegram-owned row")
	}
	if !skGotSlack {
		t.Error("expected slack Rehydrate to claim its own row")
	}
	if skGotLegacy {
		t.Error("expected slack Rehydrate to skip the legacy empty-channel row (telegram-owned)")
	}
	if skGotUnknown {
		t.Error("expected slack Rehydrate to skip the unknown-channel row")
	}

	// The unknown-channel row is claimed by neither handler; it's swept
	// separately via PrunePendingApprovalsOutside once it expires.
	if store.get("row-unknown") == nil {
		t.Error("expected the unknown-channel row to still exist in the store, untouched")
	}
}
