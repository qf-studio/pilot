# TASK-35: Remove ffmpeg Dependency - Send OGG Directly to Whisper

**Status**: 🚧 Planned
**Priority**: P2 - Simplifies setup
**Created**: 2026-01-27

---

## Context

**Problem**:
Voice transcription currently requires ffmpeg to convert Telegram OGG files to WAV before sending to Whisper API. This adds:
- Extra dependency users must install
- Extra processing step (conversion)
- Extra disk I/O (temp WAV file)

**Goal**:
Remove ffmpeg dependency by sending OGG files directly to Whisper API.

**Evidence**:
OpenAI Whisper API supports: `flac, mp3, mp4, mpeg, mpga, m4a, ogg, wav, webm`
Telegram voice messages are OGG/OPUS format - directly supported.

---

## Implementation Plan

### Phase 1: Remove Conversion Logic

**Goal**: Skip WAV conversion, pass OGG directly

**Tasks**:
- [ ] Modify `transcriber.go` - remove `convertToWav` call in `Transcribe()`
- [ ] Delete `convert.go` entirely (or keep CleanupWav for backward compat)
- [ ] Remove `FFmpegPath` from Config struct
- [ ] Update `DefaultConfig()` - remove ffmpeg reference

**Files**:
- `internal/transcription/transcriber.go` - remove conversion call
- `internal/transcription/convert.go` - DELETE or gut

### Phase 2: Remove ffmpeg Checks

**Goal**: Remove all ffmpeg health checks and setup prompts

**Tasks**:
- [ ] `internal/transcription/setup.go` - remove FFmpegInstalled, InstallFFmpeg
- [ ] `internal/health/health.go` - remove ffmpeg checks from doctor
- [ ] `cmd/pilot/setup.go` - remove ffmpeg install prompt
- [ ] `internal/adapters/telegram/handler.go` - remove ffmpeg error messages

**Files**:
- `internal/transcription/setup.go`
- `internal/health/health.go`
- `cmd/pilot/setup.go`
- `internal/adapters/telegram/handler.go`

### Phase 3: Update Documentation

**Goal**: Update docs to reflect simplified setup

**Tasks**:
- [ ] Update README.md - voice only needs OPENAI_API_KEY
- [ ] Update setup wizard messaging
- [ ] Update TASK-35 completion

**Files**:
- `README.md`

---

## Technical Decisions

| Decision | Options | Recommendation | Reasoning |
|----------|---------|----------------|-----------|
| Keep convert.go? | Delete / Keep stub | Delete | No need for backward compat |
| Test coverage? | Manual / Add tests | Manual | Whisper API is external |

---

## Files to Modify

| File | Action | Changes |
|------|--------|---------|
| `internal/transcription/convert.go` | DELETE | Entire file |
| `internal/transcription/transcriber.go` | MODIFY | Remove convertToWav call, FFmpegPath |
| `internal/transcription/setup.go` | MODIFY | Remove FFmpeg checks/install |
| `internal/health/health.go` | MODIFY | Remove ffmpeg health check |
| `cmd/pilot/setup.go` | MODIFY | Remove ffmpeg install |
| `internal/adapters/telegram/handler.go` | MODIFY | Remove ffmpeg error messages |
| `README.md` | MODIFY | Simplify voice setup docs |

---

## Verify

```bash
# Build succeeds
go build ./...

# Tests pass
go test ./internal/transcription/...

# Doctor shows voice without ffmpeg
./bin/pilot doctor

# Voice works (manual test with Telegram)
```

---

## Done

- [ ] ffmpeg no longer required for voice
- [ ] `pilot doctor` doesn't check ffmpeg for voice
- [ ] Voice transcription works with OGG directly
- [ ] Setup wizard doesn't prompt for ffmpeg
- [ ] README shows simplified setup (just OPENAI_API_KEY)

---

## Risk Assessment

**Low Risk**:
- Whisper API officially supports OGG
- No format conversion edge cases
- Simpler code = fewer bugs

**Rollback**:
- If OGG fails for some reason, can re-add ffmpeg conversion
- But unlikely given official API support

---

**Estimated Effort**: 30 minutes
**Dependencies**: TASK-34 (SenseVoice removal) - ✅ Complete
