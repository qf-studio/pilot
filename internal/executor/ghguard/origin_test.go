package ghguard

import "testing"

func TestIsTestProcessArgv(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want bool
	}{
		{"empty", nil, false},
		{"compiled test binary", []string{"/tmp/go-build123/b001/autopilot.test", "-test.timeout=10m0s"}, true},
		{"test binary no flags", []string{"/tmp/x/executor.test"}, true},
		{"go test", []string{"/usr/bin/go", "test", "./..."}, true},
		{"test flag only", []string{"./bin", "-test.run", "TestFoo"}, true},
		{"claude bash tool shell", []string{"/bin/bash", "-c", "gh issue close 1"}, false},
		{"plain go build", []string{"go", "build", "./..."}, false},
		{"pilot itself", []string{"/usr/local/bin/pilot", "start"}, false},
		{"arg merely mentions test", []string{"/bin/sh", "-c", "make test"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTestProcessArgv(tt.argv); got != tt.want {
				t.Errorf("IsTestProcessArgv(%v) = %v, want %v", tt.argv, got, tt.want)
			}
		})
	}
}
