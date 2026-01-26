# TASK-07: Telegram Voice Message Support

**Status**: 📋 Planned
**Created**: 2026-01-26
**Assignee**: Manual

---

## Context

**Problem**:
Users cannot send voice messages via Telegram to Claude. Voice is often faster than typing, especially on mobile.

**Goal**:
Enable Telegram bot to receive voice messages, transcribe them to text, and process with Claude.

**Success Criteria**:
- [ ] Bot receives voice messages from Telegram
- [ ] Audio transcribed to text with high accuracy
- [ ] Transcription passed to Claude for processing
- [ ] Works for multiple languages (EN, RU, ZH at minimum)

---

## Research Findings

### Transcription Options Evaluated

| Solution | Speed | Cost | Accuracy | Self-Hosted |
|----------|-------|------|----------|-------------|
| **SenseVoice-Small** | 70ms/10s (15x Whisper) | Free | Excellent CN/EN | ✅ Yes |
| **Paraformer** | Fast | Free | SOTA Chinese | ✅ Yes |
| **faster-whisper** | 4x Whisper | Free | Good multilingual | ✅ Yes |
| **OpenAI Whisper API** | ~1s/10s | $0.006/min | Excellent | ❌ No |
| **Deepgram Nova-3** | Fastest cloud | $4.30/1k min | Best WER | ❌ No |

### Recommended Approach

**Primary: SenseVoice-Small** (self-hosted)
- 15x faster than Whisper Large
- Excellent Chinese/Cantonese recognition
- Small model (~200MB), runs on CPU
- Free, no API costs
- GitHub: https://github.com/FunAudioLLM/SenseVoice

**Fallback: OpenAI Whisper API**
- Simple HTTP API integration
- Reliable, well-documented
- Good for users who can't run local models

### Telegram Voice Format
- Voice messages: `.oga` (Opus in Ogg container)
- Audio messages: Various formats
- Need to convert to format supported by transcription model

---

## Implementation Plan

### Phase 1: Audio Download & Conversion
**Goal**: Get audio from Telegram in usable format

**Tasks**:
- [ ] Add voice message handling to handler.go (Message.Voice field)
- [ ] Download voice file via getFile API
- [ ] Convert .oga to .wav using ffmpeg (or native Go library)

**Files**:
- `internal/adapters/telegram/client.go` - Add GetFile, DownloadFile
- `internal/adapters/telegram/handler.go` - Handle Message.Voice

### Phase 2: Transcription Service
**Goal**: Implement transcription with SenseVoice

**Tasks**:
- [ ] Create transcription service interface
- [ ] Implement SenseVoice transcriber (Python subprocess or gRPC)
- [ ] Implement OpenAI Whisper fallback
- [ ] Add configuration for transcription backend

**Files**:
- `internal/transcription/transcriber.go` - Interface definition
- `internal/transcription/sensevoice.go` - SenseVoice implementation
- `internal/transcription/whisper_api.go` - OpenAI fallback
- `internal/config/config.go` - Add transcription config

### Phase 3: Integration
**Goal**: Wire transcription into message flow

**Tasks**:
- [ ] Transcribe voice → text in handler
- [ ] Pass transcribed text to Claude
- [ ] Show transcription to user before processing
- [ ] Handle transcription errors gracefully

**Files**:
- `internal/adapters/telegram/handler.go` - Integration
- `internal/adapters/telegram/formatter.go` - Voice message formatting

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Primary STT | Whisper API, SenseVoice, Deepgram | SenseVoice | 15x faster, free, great Chinese |
| Fallback STT | None, Whisper API | Whisper API | Simple, reliable fallback |
| Audio format | Keep .oga, convert to .wav | Convert to .wav | Better compatibility |
| SenseVoice hosting | Embedded, subprocess, gRPC | Subprocess | Simple, isolated |

---

## Dependencies

**System Requirements**:
- [ ] ffmpeg (for audio conversion)
- [ ] Python 3.8+ (for SenseVoice)
- [ ] ~500MB disk (SenseVoice model)

**Python Dependencies**:
```
funasr
torch
torchaudio
```

**Requires**:
- [ ] TASK-06: Image support (shares file download logic)

---

## Verify

```bash
# Install dependencies
brew install ffmpeg
pip install funasr torch torchaudio

# Run tests
make test

# Manual test
pilot telegram -p <project>
# Send voice message to bot
```

---

## Done

Observable outcomes that prove completion:

- [ ] Voice messages transcribed to text
- [ ] Transcription shown to user for confirmation
- [ ] Claude processes transcribed text
- [ ] Works for English, Russian, Chinese
- [ ] Fallback to Whisper API works
- [ ] Tests pass

---

## Notes

**SenseVoice Usage**:
```python
from funasr import AutoModel

model = AutoModel(model="iic/SenseVoiceSmall")
result = model.generate(input="audio.wav")
print(result[0]["text"])
```

**Telegram Voice API**:
- Voice messages have `file_id`, `duration`, `mime_type`
- Use `getFile` to get download path
- Download from `https://api.telegram.org/file/bot<token>/<file_path>`

**Audio Conversion**:
```bash
ffmpeg -i input.oga -ar 16000 -ac 1 output.wav
```

---

## References

- [SenseVoice GitHub](https://github.com/FunAudioLLM/SenseVoice)
- [FunASR Toolkit](https://github.com/modelscope/FunASR)
- [faster-whisper](https://github.com/SYSTRAN/faster-whisper)
- [Telegram Voice Messages](https://core.telegram.org/bots/api#voice)

---

**Last Updated**: 2026-01-26
