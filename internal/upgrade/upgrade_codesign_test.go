package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallBinary_PrepareForExecution covers the codesign error-propagation path
// added in GH-3136: a non-zero codesign exit must surface a Gatekeeper-aware error.
func TestInstallBinary_PrepareForExecution(t *testing.T) {
	tests := []struct {
		name        string
		prepareErr  error
		wantErr     bool
		wantErrMsg  string
	}{
		{
			name:       "happy path — codesign succeeds",
			prepareErr: nil,
			wantErr:    false,
		},
		{
			name:       "codesign non-zero exit",
			prepareErr: errors.New("exit status 1"),
			wantErr:    true,
			wantErrMsg: "Gatekeeper",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			binaryPath := filepath.Join(dir, "pilot")
			downloadPath := filepath.Join(dir, "download")

			if err := os.WriteFile(downloadPath, []byte("fake binary"), 0755); err != nil {
				t.Fatal(err)
			}

			u := &Upgrader{
				binaryPath:          binaryPath,
				backupPath:          binaryPath + BackupSuffix,
				prepareForExecution: func(string) error { return tt.prepareErr },
			}

			err := u.installBinary(downloadPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("installBinary() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErrMsg != "" && err != nil && !strings.Contains(err.Error(), tt.wantErrMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrMsg)
			}
		})
	}
}
