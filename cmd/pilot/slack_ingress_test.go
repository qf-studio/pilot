package main

import (
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/slack"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/gateway"
	"github.com/qf-studio/pilot/internal/testutil"
)

// TestValidateSlackApprovalIngress pins GH-5758 polling-mode behaviour: Slack
// approval cards with socket_mode off need the HTTP interactions route, so a
// run with no gateway must refuse to start and name socket_mode.
func TestValidateSlackApprovalIngress(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(sc *slack.Config)
		noGateway bool
		noGwCfg   bool
		wantErr   bool
	}{
		{name: "socket off, gateway runs", wantErr: false},
		{name: "socket off, --no-gateway", noGateway: true, wantErr: true},
		{name: "socket off, no gateway config", noGwCfg: true, wantErr: true},
		{name: "socket on, --no-gateway", noGateway: true, mutate: func(sc *slack.Config) { sc.SocketMode = true }, wantErr: false},
		{name: "approval disabled, --no-gateway", noGateway: true, mutate: func(sc *slack.Config) { sc.Approval.Enabled = false }, wantErr: false},
		{name: "slack disabled, --no-gateway", noGateway: true, mutate: func(sc *slack.Config) { sc.Enabled = false }, wantErr: false},
		{name: "no bot token (no cards), --no-gateway", noGateway: true, mutate: func(sc *slack.Config) { sc.BotToken = "" }, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Gateway = &gateway.Config{}
			if tt.noGwCfg {
				cfg.Gateway = nil
			}
			cfg.Adapters.Slack = slack.DefaultConfig()
			cfg.Adapters.Slack.Enabled = true
			cfg.Adapters.Slack.BotToken = testutil.FakeSlackBotToken
			cfg.Adapters.Slack.Approval = &slack.ApprovalConfig{Enabled: true}
			if tt.mutate != nil {
				tt.mutate(cfg.Adapters.Slack)
			}

			err := validateSlackApprovalIngress(cfg, tt.noGateway)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "socket_mode") {
				t.Errorf("error must name socket_mode: %v", err)
			}
		})
	}
}

func TestValidateSlackApprovalIngress_NilSlack(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Adapters.Slack = nil
	if err := validateSlackApprovalIngress(cfg, true); err != nil {
		t.Fatalf("nil slack config must pass, got %v", err)
	}
}
