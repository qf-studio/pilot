package discord

import "time"

// Config holds Discord adapter configuration.
type Config struct {
	BotToken        string
	AllowedGuilds   []string      // Guild IDs allowed to send tasks
	AllowedChannels []string      // Channel IDs allowed to send tasks
	RateLimit       *RateLimitConfig
}

// RateLimitConfig holds rate limiting configuration.
type RateLimitConfig struct {
	MessagesPerSecond int
	TasksPerMinute    int
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		RateLimit: &RateLimitConfig{
			MessagesPerSecond: 5,
			TasksPerMinute:    10,
		},
	}
}

// Discord Gateway intents (https://discord.com/developers/docs/topics/gateway#gateway-intents)
const (
	IntentGuilds              = 1 << 0
	IntentGuildMembers        = 1 << 1
	IntentGuildModeration     = 1 << 2
	IntentGuildEmojis         = 1 << 3
	IntentGuildIntegrations   = 1 << 4
	IntentGuildWebhooks       = 1 << 5
	IntentGuildInvites        = 1 << 6
	IntentGuildVoiceStates    = 1 << 7
	IntentGuildPresences      = 1 << 8
	IntentGuildMessages       = 1 << 9
	IntentGuildMessageReactions = 1 << 10
	IntentGuildMessageTyping  = 1 << 11
	IntentDirectMessages      = 1 << 12
	IntentDirectMessageReactions = 1 << 13
	IntentDirectMessageTyping = 1 << 14
	IntentMessageContent      = 1 << 15
	IntentGuildScheduledEvents = 1 << 16
	IntentAutoModerationConfiguration = 1 << 20
	IntentAutoModerationExecution     = 1 << 21
)

// DefaultIntents for Pilot: guilds, guild messages, direct messages, message content
const DefaultIntents = IntentGuilds | IntentGuildMessages | IntentDirectMessages | IntentMessageContent

// Discord API constants
const (
	DiscordAPIURL = "https://discord.com/api/v10"
	DiscordGatewayURL = "wss://gateway.discord.gg"

	// Opcode for IDENTIFY
	OpcodeIdentify = 2
	// Opcode for HEARTBEAT
	OpcodeHeartbeat = 1
	// Opcode for RESUME
	OpcodeResume = 6
	// Opcode for HELLO (server-to-client)
	OpcodeHello = 10
	// Opcode for DISPATCH (server-to-client, carries events)
	OpcodeDispatch = 0

	// Close codes for resumable disconnects (4000–4009)
	CloseCodeUnknownError = 4000
	CloseCodeUnknownOpcode = 4001
	CloseCodeDecodeError = 4002
	CloseCodeNotAuthenticated = 4003
	CloseCodeAuthenticationFailed = 4004
	CloseCodeAlreadyAuthenticated = 4005
	CloseCodeInvalidSeq = 4007
	CloseCodeRateLimited = 4008
	CloseCodeSessionTimeout = 4009

	// Non-resumable close code
	CloseCodeInvalidToken = 4014
)

// MaxMessageLength is the maximum message length for Discord.
const MaxMessageLength = 2000

// GatewayEvent represents a Discord Gateway event.
type GatewayEvent struct {
	Op   int             `json:"op"`
	D    interface{}     `json:"d"`
	S    *int            `json:"s"`
	T    *string         `json:"t"`
}

// Heartbeat is sent by client to maintain connection.
type Heartbeat struct {
	Op int  `json:"op"`
	D  *int `json:"d"`
}

// Identify is sent by client on connection.
type Identify struct {
	Op int                  `json:"op"`
	D  IdentifyData `json:"d"`
}

// IdentifyData contains identify payload.
type IdentifyData struct {
	Token      string `json:"token"`
	Intents    int    `json:"intents"`
	Properties map[string]string `json:"properties"`
}

// Resume is sent by client to resume session.
type Resume struct {
	Op int         `json:"op"`
	D  ResumeData  `json:"d"`
}

// ResumeData contains resume payload.
type ResumeData struct {
	Token     string `json:"token"`
	SessionID string `json:"session_id"`
	Seq       int    `json:"seq"`
}

// Hello is sent by server.
type Hello struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

// MessageCreate event data.
type MessageCreate struct {
	ID        string    `json:"id"`
	ChannelID string    `json:"channel_id"`
	GuildID   string    `json:"guild_id,omitempty"`
	Author    User      `json:"author"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// User represents a Discord user.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
}

// InteractionCreate event data.
type InteractionCreate struct {
	ID            string `json:"id"`
	Token         string `json:"token"`
	Type          int    `json:"type"` // 1=PING, 2=APPLICATION_COMMAND, 3=MESSAGE_COMPONENT
	GuildID       string `json:"guild_id,omitempty"`
	ChannelID     string `json:"channel_id,omitempty"`
	Member        *Member `json:"member,omitempty"`
	User          *User   `json:"user,omitempty"`
	Data          InteractionData `json:"data"`
	Message       *Message `json:"message,omitempty"`
}

// Member represents a guild member.
type Member struct {
	User User   `json:"user"`
	Nick string `json:"nick,omitempty"`
}

// InteractionData contains interaction payload data.
type InteractionData struct {
	CustomID string `json:"custom_id,omitempty"` // Button interaction custom ID
}

// Message represents a Discord message (REST).
type Message struct {
	ID        string `json:"id,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	Content   string `json:"content,omitempty"`
	Embeds    []Embed `json:"embeds,omitempty"`
	Components []Component `json:"components,omitempty"`
}

// Embed represents a Discord embed.
type Embed struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Color       int    `json:"color,omitempty"`
	Footer      *EmbedFooter `json:"footer,omitempty"`
}

// EmbedFooter represents an embed footer.
type EmbedFooter struct {
	Text string `json:"text,omitempty"`
}

// Component represents an interactive component (action row with buttons).
type Component struct {
	Type       int         `json:"type"` // 1=ACTION_ROW
	Components []Button    `json:"components,omitempty"`
}

// Button represents a button in a component.
type Button struct {
	Type     int    `json:"type"` // 2=BUTTON
	Style    int    `json:"style"` // 1=PRIMARY, 4=DANGER
	Label    string `json:"label"`
	CustomID string `json:"custom_id"`
}

// InteractionResponse is sent to acknowledge an interaction.
type InteractionResponse struct {
	Type int             `json:"type"` // 4=CHANNEL_MESSAGE_WITH_SOURCE, 5=DEFERRED_CHANNEL_MESSAGE_WITH_SOURCE
	Data InteractionData `json:"data,omitempty"`
}
