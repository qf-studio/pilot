package transcription

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// convertToWav converts an audio file to WAV format suitable for transcription
// Returns the path to the converted WAV file (16kHz, mono)
func (s *Service) convertToWav(ctx context.Context, inputPath string) (string, error) {
	// Check if input already exists
	if _, err := os.Stat(inputPath); os.IsNotExist(err) {
		return "", fmt.Errorf("input file does not exist: %s", inputPath)
	}

	// Create temp file for output
	ext := filepath.Ext(inputPath)
	base := strings.TrimSuffix(filepath.Base(inputPath), ext)
	outputPath := filepath.Join(filepath.Dir(inputPath), base+".transcribe.wav")

	// Build ffmpeg command
	// -i: input file
	// -ar 16000: sample rate 16kHz (required by most STT models)
	// -ac 1: mono
	// -y: overwrite output
	cmd := exec.CommandContext(ctx, s.ffmpegPath,
		"-i", inputPath,
		"-ar", "16000",
		"-ac", "1",
		"-y",
		outputPath,
	)

	// Capture stderr for error messages
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ffmpeg conversion failed: %w\nOutput: %s", err, string(output))
	}

	return outputPath, nil
}

// CleanupWav removes a converted WAV file
func CleanupWav(wavPath string) {
	if strings.HasSuffix(wavPath, ".transcribe.wav") {
		_ = os.Remove(wavPath)
	}
}

// CheckFFmpeg checks if ffmpeg is available
func CheckFFmpeg(ffmpegPath string) error {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}

	cmd := exec.Command(ffmpegPath, "-version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg not found or not executable: %w", err)
	}
	return nil
}
