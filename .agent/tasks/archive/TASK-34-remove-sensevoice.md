# TASK-34: Remove SenseVoice/FunASR - Use Whisper API Only

**Status**: ✅ Complete
**Priority**: P1 - Blocking voice functionality
**Created**: 2026-01-27
**Completed**: 2026-01-27

## Context

SenseVoice/FunASR local transcription has persistent issues:
- funasr dumps logs to stdout breaking JSON parsing
- torchcodec dependency issues
- Complex Python environment management

Decision: Remove SenseVoice entirely, use Whisper API as only backend.

## Files Modified

### DELETED
- [x] `internal/transcription/sensevoice.go` - entire file

### MODIFIED
- [x] `internal/transcription/transcriber.go` - removed SenseVoice backend, simplified to Whisper only
- [x] `internal/transcription/setup.go` - removed FunASRInstalled, InstallFunASR, updated instructions
- [x] `internal/health/health.go` - removed funasr checks, simplified voice status
- [x] `cmd/pilot/setup.go` - removed checkPythonModule, installSenseVoice, updated voice setup
- [x] `internal/adapters/telegram/handler.go` - removed SenseVoice messages and buttons
- [x] `README.md` - updated voice transcription docs

## Acceptance Criteria

- [x] `go build` succeeds
- [x] `go test ./...` passes
- [x] `pilot doctor` shows voice feature with Whisper API only
- [x] No SenseVoice/funasr references in Go code

## Notes

Historical task docs (.agent/tasks/TASK-07, TASK-30) retain SenseVoice references as they document the project history.
