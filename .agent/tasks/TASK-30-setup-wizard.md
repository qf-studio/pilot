# TASK-30: Setup Wizard & Dependency Check

**Status**: Complete ✅
**Priority**: High
**Created**: 2026-01-27
**Completed**: 2026-01-27

---

## Problem

Features get implemented but users can't use them because:
1. Missing dependencies (ffmpeg, Python packages)
2. Missing config (API keys, tokens)
3. No clear feedback on what's missing
4. Features silently fail instead of guiding user

**Example**: Voice transcription implemented but user gets "not enabled" with no guidance on how to enable.

---

## Solution

### 1. Startup Health Check

On `pilot telegram` or `pilot server` start:

```
🚀 Pilot v1.0.0

Checking dependencies...
✅ Claude Code 2.1.17
✅ Git
❌ ffmpeg (needed for voice transcription)
   → brew install ffmpeg
⚠️ SenseVoice not installed (voice will use Whisper API fallback)
   → pip install funasr torch torchaudio

Checking config...
✅ Telegram bot token
✅ Projects configured (3)
❌ OpenAI API key (needed for voice fallback)
   → export OPENAI_API_KEY="sk-..."
⚠️ Daily briefs enabled but no schedule set
   → Add schedule: "0 8 * * *" to config

Features:
✅ Task execution
✅ Image analysis
⚠️ Voice transcription (missing: ffmpeg, OPENAI_API_KEY)
✅ Daily briefs
✅ Alerting
✅ Cross-project memory

Ready to start? [Y/n]
```

### 2. `pilot doctor` Command

```bash
$ pilot doctor

Pilot Health Check
==================

System Dependencies:
  ✅ claude (2.1.17)
  ✅ git (2.43.0)
  ❌ ffmpeg - brew install ffmpeg
  ⚠️ python3 (3.12) - funasr not installed

Configuration:
  ✅ ~/.pilot/config.yaml exists
  ✅ telegram.bot_token set
  ❌ transcription.openai_api_key missing
  ✅ projects: 3 configured

Features Status:
  ✅ Core execution
  ✅ Telegram bot
  ⚠️ Voice (degraded - no ffmpeg)
  ✅ Images
  ✅ Daily briefs
  ✅ Alerts

Recommendations:
  1. Install ffmpeg: brew install ffmpeg
  2. Set OPENAI_API_KEY for voice fallback
```

### 3. `pilot setup` Interactive Wizard

```bash
$ pilot setup

Welcome to Pilot Setup! 🚀

Step 1/5: Telegram Bot
  Do you have a Telegram bot token? [Y/n]: y
  Enter token: 8597...
  ✅ Bot connected: @PilotDevBot

Step 2/5: Projects
  Add project path: ~/Projects/startups/pilot
  Project name [pilot]:
  Has Navigator? [Y/n]: y
  ✅ Added: pilot
  Add another? [y/N]: n

Step 3/5: Voice Transcription
  Install SenseVoice (local, fast, free)? [Y/n]: y
  Installing ffmpeg... ✅
  Installing funasr... ✅

  Set up Whisper API backup? [Y/n]: y
  OpenAI API key: sk-...
  ✅ Voice transcription ready

Step 4/5: Daily Briefs
  Enable morning briefs? [Y/n]: y
  What time? [8:00]:
  Timezone [Europe/Berlin]:
  ✅ Daily briefs at 8:00 Berlin time

Step 5/5: Alerts
  Enable failure alerts? [Y/n]: y
  ✅ Alerts → Telegram

Setup complete! 🎉

Run: pilot telegram
```

### 4. In-Bot Guidance

When feature fails, provide actionable help:

```
❌ Voice transcription failed

Missing: ffmpeg

To enable voice messages:
1. brew install ffmpeg
2. Restart bot

Or set OPENAI_API_KEY for cloud fallback.
```

---

## Implementation

### health.go
```go
package health

type Check struct {
    Name     string
    Status   Status // OK, Warning, Error
    Message  string
    Fix      string // How to fix
}

type Status int
const (
    OK Status = iota
    Warning
    Error
)

func RunAll() []Check {
    return []Check{
        checkClaude(),
        checkGit(),
        checkFFmpeg(),
        checkSenseVoice(),
        checkConfig(),
        checkTelegram(),
        checkOpenAI(),
    }
}
```

### Checks to implement:
- [x] `claude --version`
- [x] `git --version`
- [x] `which ffmpeg`
- [x] `python3 -c "import funasr"`
- [x] Config file exists
- [x] Required keys set
- [x] Projects valid paths
- [x] Telegram bot token valid

---

## Acceptance Criteria

- [x] `pilot doctor` shows all deps and config status
- [x] `pilot setup` interactive wizard works
- [x] `pilot telegram` shows health summary on start
- [x] Missing deps show fix commands
- [x] Features gracefully degrade with clear messaging
- [x] No silent failures

---

## Dependencies

- None (foundational improvement)

---

## Notes

Learn from pitfalls:
- Voice: Implemented but user couldn't use (missing deps)
- Config: Features disabled by default with no guidance
- Errors: "Not enabled" without explaining how to enable

**Principle**: Every feature should either work out of the box OR clearly explain what's needed.
