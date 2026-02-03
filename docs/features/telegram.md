# Telegram Bot

Talk to Pilot naturally through Telegram. It understands different interaction modes automatically.

## Setup

1. Create a bot via [@BotFather](https://t.me/BotFather)
2. Get your bot token
3. Configure in `~/.pilot/config.yaml`:

```yaml
adapters:
  telegram:
    enabled: true
    bot_token: "${TELEGRAM_BOT_TOKEN}"
    chat_id: "${TELEGRAM_CHAT_ID}"  # Optional: restrict to specific chat
    transcription:
      provider: openai
      openai_key: "${OPENAI_API_KEY}"  # For voice messages
```

4. Start Pilot:

```bash
pilot start --telegram
```

## Interaction Modes

Pilot automatically detects your intent:

| Mode | Example | What Happens |
|------|---------|--------------|
| 💬 **Chat** | "What do you think about using Redis?" | Conversational response, no code changes |
| 🔍 **Question** | "What files handle authentication?" | Quick read-only answer |
| 🔬 **Research** | "Research how the caching layer works" | Deep analysis sent to chat |
| 📐 **Planning** | "Plan how to add rate limiting" | Shows plan with Execute/Cancel buttons |
| 🚀 **Task** | "Add rate limiting to /api/users" | Confirms, then creates PR |

## Example Conversations

### Planning Mode

```
You: Plan how to add user authentication

Pilot: 📐 Drafting plan...

Pilot: 📋 Implementation Plan
       1. Create auth middleware in internal/middleware/
       2. Add JWT token validation
       3. Protect routes in cmd/api/routes.go
       4. Add user session store
       5. Update tests

       Estimated: 3 files, ~200 lines

       [Execute] [Cancel]

You: [clicks Execute]

Pilot: 🚀 Executing...
Pilot: ✅ PR #142 ready: https://github.com/you/repo/pull/142
```

### Research Mode

```
You: Research how errors are handled in this codebase

Pilot: 🔬 Researching...

Pilot: 📊 Error Handling Analysis

       Pattern: Centralized error types in internal/errors/

       - errors.go: Custom error types (ValidationError, NotFoundError)
       - handler.go: HTTP error response formatting
       - middleware.go: Recovery and logging

       Usage: 47 files import internal/errors

       Key functions:
       - errors.Wrap() for context
       - errors.Is() for type checking
       - errors.WithStack() for debugging
```

### Chat Mode

```
You: What do you think about using Redis for caching?

Pilot: For this codebase, Redis would be a good fit because:

       1. Already have Docker setup - easy to add Redis container
       2. Current in-memory cache in internal/cache/ has TTL
          logic that maps to Redis EXPIRE
       3. Horizontal scaling later would benefit from shared cache

       Trade-offs to consider:
       - Added operational complexity
       - Network latency vs memory access
       - Need connection pooling

       Want me to plan a Redis integration?
```

## Voice Messages

Send voice messages and Pilot transcribes them using OpenAI Whisper:

```
You: [voice message: "Add a health check endpoint to the API"]

Pilot: 🎙️ Transcribed: "Add a health check endpoint to the API"

Pilot: 🚀 Starting task: Add a health check endpoint to the API
       Branch: pilot/telegram-task-1234
```

## Images

Send screenshots or images:

```
You: [image of error stack trace]

Pilot: I see a NullPointerException in UserService.java:42.

       The issue is that `user.getProfile()` returns null when
       the profile hasn't been loaded.

       Fix: Add null check or eager-load the profile.

       Want me to fix this?
```

## Commands

| Command | Description |
|---------|-------------|
| `/status` | Show current task status |
| `/queue` | Show pending tasks |
| `/cancel` | Cancel current task |
| `/help` | Show available commands |

## Multi-Project Support

Specify project in your message:

```
You: In pilot-api, add rate limiting to /users endpoint

Pilot: 🚀 Starting task in pilot-api...
```

Or configure a default project:

```yaml
projects:
  - name: "pilot-api"
    path: "~/Projects/pilot-api"
    navigator: true
    default: true  # Used when no project specified
```
