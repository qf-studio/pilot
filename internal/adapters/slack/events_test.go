package slack

import (
	"encoding/json"
	"testing"
)

func TestParseEnvelope(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantID       string
		wantEvent    *SocketEvent
		wantErr      bool
		errContains  string
	}{
		{
			name: "message event",
			input: `{
				"envelope_id": "env-001",
				"type": "events_api",
				"payload": {
					"token": "tok",
					"team_id": "T123",
					"type": "event_callback",
					"event": {
						"type": "message",
						"channel": "C999",
						"user": "U456",
						"text": "hello world",
						"ts": "1234567890.123456",
						"thread_ts": "1234567890.000001"
					}
				}
			}`,
			wantID: "env-001",
			wantEvent: &SocketEvent{
				Type:      "message",
				ChannelID: "C999",
				UserID:    "U456",
				Text:      "hello world",
				ThreadTS:  "1234567890.000001",
				Timestamp: "1234567890.123456",
			},
		},
		{
			name: "app_mention event strips bot mention",
			input: `{
				"envelope_id": "env-002",
				"type": "events_api",
				"payload": {
					"token": "tok",
					"team_id": "T123",
					"type": "event_callback",
					"event": {
						"type": "app_mention",
						"channel": "C111",
						"user": "U789",
						"text": "<@UBOT123> deploy staging",
						"ts": "1111111111.111111"
					}
				}
			}`,
			wantID: "env-002",
			wantEvent: &SocketEvent{
				Type:      "app_mention",
				ChannelID: "C111",
				UserID:    "U789",
				Text:      "deploy staging",
				Timestamp: "1111111111.111111",
			},
		},
		{
			name: "app_mention with multiple mentions",
			input: `{
				"envelope_id": "env-003",
				"type": "events_api",
				"payload": {
					"token": "tok",
					"team_id": "T123",
					"type": "event_callback",
					"event": {
						"type": "app_mention",
						"channel": "C111",
						"user": "U789",
						"text": "<@UBOT123> <@UOTHER456> check this",
						"ts": "2222222222.222222"
					}
				}
			}`,
			wantID: "env-003",
			wantEvent: &SocketEvent{
				Type:      "app_mention",
				ChannelID: "C111",
				UserID:    "U789",
				Text:      "check this",
				Timestamp: "2222222222.222222",
			},
		},
		{
			name: "bot message has BotID set",
			input: `{
				"envelope_id": "env-004",
				"type": "events_api",
				"payload": {
					"token": "tok",
					"team_id": "T123",
					"type": "event_callback",
					"event": {
						"type": "message",
						"channel": "C999",
						"user": "U456",
						"text": "bot says hi",
						"ts": "3333333333.333333",
						"bot_id": "B999"
					}
				}
			}`,
			wantID: "env-004",
			wantEvent: &SocketEvent{
				Type:      "message",
				ChannelID: "C999",
				UserID:    "U456",
				Text:      "bot says hi",
				Timestamp: "3333333333.333333",
				BotID:     "B999",
			},
		},
		{
			name: "message with file attachments",
			input: `{
				"envelope_id": "env-005",
				"type": "events_api",
				"payload": {
					"token": "tok",
					"team_id": "T123",
					"type": "event_callback",
					"event": {
						"type": "message",
						"channel": "C999",
						"user": "U456",
						"text": "see attached",
						"ts": "4444444444.444444",
						"files": [
							{
								"id": "F001",
								"name": "screenshot.png",
								"mimetype": "image/png",
								"url_private": "https://files.slack.com/files-pri/T123-F001/screenshot.png",
								"size": 102400
							}
						]
					}
				}
			}`,
			wantID: "env-005",
			wantEvent: &SocketEvent{
				Type:      "message",
				ChannelID: "C999",
				UserID:    "U456",
				Text:      "see attached",
				Timestamp: "4444444444.444444",
				Files: []SlackFile{
					{
						ID:       "F001",
						Name:     "screenshot.png",
						Mimetype: "image/png",
						URL:      "https://files.slack.com/files-pri/T123-F001/screenshot.png",
						Size:     102400,
					},
				},
			},
		},
		{
			name: "disconnect envelope returns nil event",
			input: `{
				"envelope_id": "env-006",
				"type": "disconnect",
				"reason": "link_disabled"
			}`,
			wantID:    "env-006",
			wantEvent: nil,
		},
		{
			name: "hello envelope returns nil event",
			input: `{
				"envelope_id": "",
				"type": "hello"
			}`,
			wantID:    "",
			wantEvent: nil,
		},
		{
			name: "unknown envelope type returns nil event",
			input: `{
				"envelope_id": "env-007",
				"type": "slash_commands"
			}`,
			wantID:    "env-007",
			wantEvent: nil,
		},
		{
			name: "unsupported inner event type returns nil",
			input: `{
				"envelope_id": "env-008",
				"type": "events_api",
				"payload": {
					"token": "tok",
					"team_id": "T123",
					"type": "event_callback",
					"event": {
						"type": "reaction_added",
						"user": "U456",
						"item": {"type": "message", "channel": "C999", "ts": "1234.5678"}
					}
				}
			}`,
			wantID:    "env-008",
			wantEvent: nil,
		},
		{
			name:        "invalid JSON returns error",
			input:       `{not valid json`,
			wantErr:     true,
			errContains: "unmarshal envelope",
		},
		{
			name: "invalid event JSON returns error",
			input: `{
				"envelope_id": "env-009",
				"type": "events_api",
				"payload": "not a json object"
			}`,
			wantID:      "env-009",
			wantErr:     true,
			errContains: "parse events_api payload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotEvent, err := parseEnvelope([]byte(tt.input))

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if gotID != tt.wantID {
				t.Errorf("envelopeID = %q, want %q", gotID, tt.wantID)
			}

			if tt.wantEvent == nil {
				if gotEvent != nil {
					t.Errorf("expected nil event, got %+v", gotEvent)
				}
				return
			}

			if gotEvent == nil {
				t.Fatal("expected non-nil event, got nil")
			}

			if gotEvent.Type != tt.wantEvent.Type {
				t.Errorf("Type = %q, want %q", gotEvent.Type, tt.wantEvent.Type)
			}
			if gotEvent.ChannelID != tt.wantEvent.ChannelID {
				t.Errorf("ChannelID = %q, want %q", gotEvent.ChannelID, tt.wantEvent.ChannelID)
			}
			if gotEvent.UserID != tt.wantEvent.UserID {
				t.Errorf("UserID = %q, want %q", gotEvent.UserID, tt.wantEvent.UserID)
			}
			if gotEvent.Text != tt.wantEvent.Text {
				t.Errorf("Text = %q, want %q", gotEvent.Text, tt.wantEvent.Text)
			}
			if gotEvent.ThreadTS != tt.wantEvent.ThreadTS {
				t.Errorf("ThreadTS = %q, want %q", gotEvent.ThreadTS, tt.wantEvent.ThreadTS)
			}
			if gotEvent.Timestamp != tt.wantEvent.Timestamp {
				t.Errorf("Timestamp = %q, want %q", gotEvent.Timestamp, tt.wantEvent.Timestamp)
			}
			if gotEvent.BotID != tt.wantEvent.BotID {
				t.Errorf("BotID = %q, want %q", gotEvent.BotID, tt.wantEvent.BotID)
			}

			// Compare files
			if len(gotEvent.Files) != len(tt.wantEvent.Files) {
				t.Errorf("Files count = %d, want %d", len(gotEvent.Files), len(tt.wantEvent.Files))
			} else {
				for i, wf := range tt.wantEvent.Files {
					gf := gotEvent.Files[i]
					if gf.ID != wf.ID || gf.Name != wf.Name || gf.Mimetype != wf.Mimetype || gf.URL != wf.URL || gf.Size != wf.Size {
						t.Errorf("Files[%d] = %+v, want %+v", i, gf, wf)
					}
				}
			}
		})
	}
}

func TestStripBotMention(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "single mention at start",
			input: "<@UBOT123> deploy staging",
			want:  "deploy staging",
		},
		{
			name:  "multiple mentions",
			input: "<@UBOT123> <@UOTHER456> check this",
			want:  "check this",
		},
		{
			name:  "mention in middle",
			input: "hey <@UBOT123> do something",
			want:  "hey do something",
		},
		{
			name:  "no mention",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "only mention",
			input: "<@UBOT123>",
			want:  "",
		},
		{
			name:  "mention with no space after",
			input: "<@UBOT123>deploy",
			want:  "deploy",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripBotMention(tt.input)
			if got != tt.want {
				t.Errorf("stripBotMention(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSocketEventIsBotMessage(t *testing.T) {
	tests := []struct {
		name  string
		event SocketEvent
		want  bool
	}{
		{
			name:  "bot message",
			event: SocketEvent{BotID: "B999"},
			want:  true,
		},
		{
			name:  "user message",
			event: SocketEvent{BotID: ""},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.IsBotMessage(); got != tt.want {
				t.Errorf("IsBotMessage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSocketEventIsFromBot(t *testing.T) {
	tests := []struct {
		name   string
		event  SocketEvent
		botID  string
		want   bool
	}{
		{
			name:  "matches own bot ID",
			event: SocketEvent{BotID: "B999"},
			botID: "B999",
			want:  true,
		},
		{
			name:  "different bot ID",
			event: SocketEvent{BotID: "B999"},
			botID: "B111",
			want:  false,
		},
		{
			name:  "no bot ID on event",
			event: SocketEvent{BotID: ""},
			botID: "B999",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.IsFromBot(tt.botID); got != tt.want {
				t.Errorf("IsFromBot(%q) = %v, want %v", tt.botID, got, tt.want)
			}
		})
	}
}

func TestSlackFileJSONRoundTrip(t *testing.T) {
	original := SlackFile{
		ID:       "F001",
		Name:     "report.pdf",
		Mimetype: "application/pdf",
		URL:      "https://files.slack.com/files-pri/T123-F001/report.pdf",
		Size:     204800,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SlackFile
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded != original {
		t.Errorf("round-trip mismatch:\n  got:  %+v\n  want: %+v", decoded, original)
	}
}

// contains checks if s contains substr (helper to avoid strings import in tests).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
