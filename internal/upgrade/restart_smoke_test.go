//go:build !windows

package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunSmokeTest covers the pre-exec smoke test added in GH-3136.
// Uses shell scripts as stand-ins for real binaries so CI does not need a
// compiled Pilot binary.
func TestRunSmokeTest(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		wantErr    bool
		wantErrMsg string
	}{
		{
			name:    "happy path — exits zero",
			script:  "#!/bin/sh\necho v1.0.0\nexit 0\n",
			wantErr: false,
		},
		{
			name:    "smoke test non-zero exit",
			script:  "#!/bin/sh\nexit 1\n",
			wantErr: true,
		},
		{
			name:       "smoke test timeout",
			script:     "#!/bin/sh\nsleep 10\n",
			wantErr:    true,
			wantErrMsg: "timed out",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "pilot")
			if err := os.WriteFile(bin, []byte(tt.script), 0755); err != nil {
				t.Fatal(err)
			}

			err := runSmokeTest(bin)
			if (err != nil) != tt.wantErr {
				t.Errorf("runSmokeTest() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErrMsg != "" && err != nil && !strings.Contains(err.Error(), tt.wantErrMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrMsg)
			}
		})
	}
}
