package web

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultConfigDailyTurnCap(t *testing.T) {
	if got := DefaultConfig().DailyTurnCap; got != 200 {
		t.Errorf("DefaultConfig().DailyTurnCap = %d, want 200", got)
	}
}

func TestConfigDailyTurnCapYAML(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want int
	}{
		{"omitted keeps default", "enabled: true\n", 200},
		{"explicit value", "daily_turn_cap: 50\n", 50},
		{"zero is unlimited", "daily_turn_cap: 0\n", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			if err := yaml.Unmarshal([]byte(tt.yaml), cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if cfg.DailyTurnCap != tt.want {
				t.Errorf("DailyTurnCap = %d, want %d", cfg.DailyTurnCap, tt.want)
			}
		})
	}
}
