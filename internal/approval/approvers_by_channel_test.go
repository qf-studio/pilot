package approval

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// GH-5753: per-channel approver resolution in Manager.SubmitApprovalRequest.
func TestManager_SubmitApprovalRequest_ApproversByChannel(t *testing.T) {
	tests := []struct {
		name      string
		flat      []string
		byChannel map[string][]string
		channel   string
		preset    []string
		want      []string
	}{
		{
			name:      "per-channel list wins over flat list",
			flat:      []string{"U111", "222"},
			byChannel: map[string][]string{"slack": {"U111"}, "telegram": {"222"}},
			channel:   "telegram",
			want:      []string{"222"},
		},
		{
			name:      "flat list used when channel has no entry",
			flat:      []string{"U111", "222"},
			byChannel: map[string][]string{"slack": {"U111"}},
			channel:   "telegram",
			want:      []string{"U111", "222"},
		},
		{
			name:      "github-review key resolves through alias normalization",
			flat:      []string{"flat"},
			byChannel: map[string][]string{"github-review": {"reviewer"}},
			channel:   "github-review",
			want:      []string{"reviewer"},
		},
		{
			name:      "github key matches github-review request",
			flat:      []string{"flat"},
			byChannel: map[string][]string{"github": {"reviewer"}},
			channel:   "github-review",
			want:      []string{"reviewer"},
		},
		{
			name:    "flat only behaves as before",
			flat:    []string{"U111", "222"},
			channel: "telegram",
			want:    []string{"U111", "222"},
		},
		{
			name:    "no approvers at all is unchanged",
			channel: "telegram",
			want:    nil,
		},
		{
			name:      "empty channel uses flat list",
			flat:      []string{"flat"},
			byChannel: map[string][]string{"telegram": {"222"}},
			channel:   "",
			want:      []string{"flat"},
		},
		{
			name:      "preset request approvers win",
			flat:      []string{"flat"},
			byChannel: map[string][]string{"telegram": {"222"}},
			channel:   "telegram",
			preset:    []string{"project-override"},
			want:      []string{"project-override"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Enabled = true
			cfg.PreMerge.Enabled = true
			cfg.PreMerge.Timeout = 5 * time.Second
			cfg.PreMerge.Approvers = tt.flat
			cfg.PreMerge.ApproversByChannel = tt.byChannel

			m := NewManager(cfg)
			m.RegisterHandler(&mockHandler{name: "telegram"})
			m.RegisterHandler(&mockHandler{name: "slack"})
			m.RegisterHandler(&mockHandler{name: "github"})

			req := &Request{
				ID:               "req-" + tt.name,
				TaskID:           "TASK-01",
				Stage:            StagePreMerge,
				Title:            "t",
				CreatedAt:        time.Now(),
				PreferredChannel: tt.channel,
				Approvers:        tt.preset,
			}
			if _, err := m.SubmitApprovalRequest(context.Background(), req); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(req.Approvers) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(req.Approvers, tt.want) {
				t.Errorf("Approvers = %v, want %v", req.Approvers, tt.want)
			}
		})
	}
}

// GH-5753: with per-channel approvers a Telegram-ID tapper is authorized, and
// a Slack ID that lives only in the slack list has no effect on Telegram.
func TestTelegramHandler_PerChannelApprovers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.PreMerge.Enabled = true
	cfg.PreMerge.Timeout = time.Hour
	cfg.PreMerge.Approvers = nil
	cfg.PreMerge.ApproversByChannel = map[string][]string{
		"slack":    {"U0SLACK1"},
		"telegram": {"424242"},
	}

	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123", 0)
	m := NewManager(cfg)
	m.RegisterHandler(handler)

	req := &Request{
		ID:               "req-per-channel-tg",
		TaskID:           "TASK-01",
		Stage:            StagePreMerge,
		Title:            "t",
		CreatedAt:        time.Now(),
		PreferredChannel: "telegram",
	}
	if _, err := m.SubmitApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handler.HandleCallback(context.Background(), "cb1", "approve:req-per-channel-tg", "U0SLACK1", "slackuser")
	cbs := client.getAnsweredCallbacks()
	if len(cbs) != 1 || cbs[0].Text != "⛔ Not an approver" {
		t.Fatalf("Slack ID must not authorize a Telegram tap, got %+v", cbs)
	}

	handler.HandleCallback(context.Background(), "cb2", "approve:req-per-channel-tg", "424242", "tguser")
	cbs = client.getAnsweredCallbacks()
	if len(cbs) != 2 || cbs[1].Text != "Approved!" {
		t.Fatalf("Telegram approver should be authorized, got %+v", cbs)
	}
}
