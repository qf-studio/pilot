# TASK-06: Telegram Image Support

**Status**: 🚧 In Progress
**Created**: 2026-01-26
**Assignee**: Manual

---

## Context

**Problem**:
Users cannot send images via Telegram to Claude for analysis. This limits the bot's usefulness for tasks involving screenshots, diagrams, or visual context.

**Goal**:
Enable Telegram bot to receive images and pass them to Claude Code for multimodal analysis.

**Success Criteria**:
- [ ] Bot receives photo messages from Telegram
- [ ] Images downloaded via Telegram `getFile` API
- [ ] Images passed to Claude as base64 or file path
- [ ] Claude analyzes image and responds appropriately

---

## Implementation Plan

### Phase 1: Telegram File Download
**Goal**: Download images from Telegram servers

**Tasks**:
- [ ] Add `GetFile` method to client.go (returns file path on Telegram servers)
- [ ] Add `DownloadFile` method to download file bytes
- [ ] Handle photo messages in handler.go (photos come as array of sizes)

**Files**:
- `internal/adapters/telegram/client.go` - Add GetFile, DownloadFile methods
- `internal/adapters/telegram/handler.go` - Handle Message.Photo field

### Phase 2: Image Processing
**Goal**: Convert downloaded image for Claude

**Tasks**:
- [ ] Select largest photo size from Telegram's array
- [ ] Download image to temp file or memory
- [ ] Convert to base64 if needed for Claude API
- [ ] Clean up temp files after processing

**Files**:
- `internal/adapters/telegram/handler.go` - Image processing logic
- `internal/adapters/telegram/formatter.go` - Format image-related messages

### Phase 3: Claude Integration
**Goal**: Pass image to Claude Code for analysis

**Tasks**:
- [ ] Modify executor to accept image attachments
- [ ] Build prompt with image context
- [ ] Handle Claude's multimodal response

**Files**:
- `internal/executor/runner.go` - Add image support to Task struct
- `internal/adapters/telegram/handler.go` - Wire image to executor

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Image storage | Memory, temp file, permanent | Temp file | Balance between memory and persistence |
| Image format | Base64, file path | TBD | Depends on Claude Code CLI capabilities |
| Photo size | Smallest, largest, specific | Largest | Best quality for analysis |

---

## Dependencies

**Requires**:
- [ ] Telegram Bot API file access (already have bot token)

**Blocks**:
- [ ] Future: Audio transcription support (similar pattern)

---

## Verify

Run these commands to validate the implementation:

```bash
# Run tests
make test

# Build
make build

# Manual test
pilot telegram -p <project>
# Send an image to the bot
```

---

## Done

Observable outcomes that prove completion:

- [ ] `client.go` exports GetFile and DownloadFile methods
- [ ] Handler processes Message.Photo field
- [ ] Image successfully passed to Claude
- [ ] Bot responds with image analysis
- [ ] Tests pass

---

## Notes

**Telegram Photo API**:
- Photos sent as array of PhotoSize objects
- Each has file_id, file_unique_id, width, height, file_size
- Use `getFile` to get file_path, then download from `https://api.telegram.org/file/bot<token>/<file_path>`

**Claude Multimodal**:
- Claude can process images natively
- Need to check how Claude Code CLI accepts images (likely via file path or stdin)

---

## Completion Checklist

Before marking complete:
- [ ] Implementation finished
- [ ] Tests written and passing
- [ ] Manual testing with real images
- [ ] Code reviewed

---

**Last Updated**: 2026-01-26
