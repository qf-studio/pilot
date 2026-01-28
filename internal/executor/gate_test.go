package executor

import (
	"testing"
)

func TestParseGateOutput(t *testing.T) {
	output := `
[1/5] Build
  [go build] ✓ (2s)

[2/5] Lint
  [golangci-lint] ✓ (5s)

[3/5] Test (short)
  [go test -short] ✗ (3s)

    FAIL: TestSomething
    Expected: true
    Got: false

[4/5] Secret Patterns
  [check-secrets] ✓ (1s)

[5/5] Integration
  [integration] ✓ (2s)
`

	checks := parseGateOutput(output)

	if len(checks) != 5 {
		t.Errorf("Expected 5 checks, got %d", len(checks))
	}

	// Check Build passed
	if !checks[0].Passed {
		t.Error("Expected Build to pass")
	}

	// Check Test failed
	if checks[2].Passed {
		t.Error("Expected Test to fail")
	}
	if checks[2].Name != "Test (short)" {
		t.Errorf("Expected check name 'Test (short)', got '%s'", checks[2].Name)
	}
}

func TestFormatGateResult(t *testing.T) {
	result := &GateResult{
		Passed: true,
		Checks: []GateCheck{
			{Name: "Build", Passed: true},
			{Name: "Lint", Passed: true},
			{Name: "Test", Passed: true},
		},
	}

	output := FormatGateResult(result)

	if output == "" {
		t.Error("Expected non-empty output")
	}

	// Check for pass indicator
	if !gateContains(output, "PASSED") {
		t.Error("Expected PASSED in output")
	}
}

func TestFormatGateResultFailed(t *testing.T) {
	result := &GateResult{
		Passed: false,
		Checks: []GateCheck{
			{Name: "Build", Passed: true},
			{Name: "Lint", Passed: false},
			{Name: "Test", Passed: false},
		},
	}

	output := FormatGateResult(result)

	// Check for fail indicator
	if !gateContains(output, "FAILED") {
		t.Error("Expected FAILED in output")
	}
}

func gateContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
