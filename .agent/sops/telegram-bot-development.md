# SOP: Telegram Bot Development

**Created**: 2026-01-26
**Trigger**: Developing or modifying Telegram bot features

---

## Markdown Formatting

### Use `Markdown` NOT `MarkdownV2`

**Rule**: Always use `"Markdown"` parse mode unless you have a specific need for MarkdownV2.

```go
// GOOD - forgiving, works reliably
h.client.SendMessage(ctx, chatID, text, "Markdown")

// AVOID - strict escaping, easy to break
h.client.SendMessage(ctx, chatID, text, "MarkdownV2")
```

### Why?

**MarkdownV2 requires escaping these characters**:
```
_ * [ ] ( ) ~ ` > # + - = | { } . !
```

**Markdown (legacy) only needs escaping for**:
```
_ * ` [
```

### Incident: 2026-01-26

Progress updates silently failed because:
- Used `MarkdownV2` parse mode
- Task IDs contain `-` (e.g., `TG-1234567890`)
- `-` wasn't escaped → API returned error → no updates shown

**Fix**: Switched to `Markdown` mode.

---

## Message Formatting Guidelines

### Progress Messages

```go
// Simple, reliable format
fmt.Sprintf("%s *%s* (%d%%)\n`%s`", emoji, phase, progress, taskID)
```

- Use backticks for IDs and code
- Use asterisks for bold
- Parentheses don't need escaping in Markdown mode

### Error Messages

```go
// Use code blocks for errors
fmt.Sprintf("❌ *Error*\n```\n%s\n```", errorMsg)
```

### Task Results

```go
// Structure with clear sections
fmt.Sprintf("✅ *Completed*\n`%s`\n\n⏱ %s", taskID, duration)
```

---

## Testing Telegram Messages

### Before Pushing

1. Test message formatting in Telegram BotFather or test group
2. Check special characters: `-`, `.`, `_`, `*`, `(`, `)`
3. Verify emojis render correctly

### Common Failures

| Symptom | Cause | Fix |
|---------|-------|-----|
| Message not sent | Invalid Markdown | Check escaping or use plain text |
| Partial formatting | Unmatched `*` or `_` | Balance markdown pairs |
| Silent failure | API error not logged | Add error logging |

---

## API Error Handling

### Always Log Errors

```go
resp, err := h.client.SendMessage(ctx, chatID, text, "Markdown")
if err != nil {
    log.Printf("[telegram] SendMessage failed: %v", err)
    // Fallback to plain text
    h.client.SendMessage(ctx, chatID, stripMarkdown(text), "")
}
```

### Check Response

```go
if resp != nil && resp.Result != nil {
    messageID = resp.Result.MessageID
} else {
    log.Printf("[telegram] No message ID returned")
}
```

---

## EditMessage Gotchas

1. **Same content = error**: Telegram returns error if new text equals old text
2. **Message too old**: Can't edit messages after ~48 hours
3. **Rate limits**: Max ~30 edits/second per chat

### Throttling Pattern

```go
var lastUpdate time.Time

if time.Since(lastUpdate) < 3*time.Second {
    return // Skip update
}
lastUpdate = time.Now()
```

---

## File Downloads

### Voice Messages (.oga)

```go
// Telegram voice = Opus in Ogg container
// Convert to WAV for processing:
// ffmpeg -i input.oga -ar 16000 -ac 1 output.wav
```

### Photos

```go
// Photos come as array of sizes
// Always pick the largest for quality
largest := msg.Photo[len(msg.Photo)-1]
```

### Download Flow

```go
// 1. Get file path
file, _ := client.GetFile(ctx, fileID)

// 2. Download from Telegram servers
url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, file.FilePath)
```

---

## Checklist

Before shipping Telegram features:

- [ ] Using `"Markdown"` (not MarkdownV2) for formatting
- [ ] Special characters tested in messages
- [ ] API errors logged, not swallowed
- [ ] Fallback to plain text on formatting errors
- [ ] Rate limiting for EditMessage calls
- [ ] Tested with real Telegram (not just unit tests)

---

## References

- [Telegram Bot API - Formatting](https://core.telegram.org/bots/api#formatting-options)
- [Telegram Bot API - SendMessage](https://core.telegram.org/bots/api#sendmessage)
- [MarkdownV2 Escaping](https://core.telegram.org/bots/api#markdownv2-style)
