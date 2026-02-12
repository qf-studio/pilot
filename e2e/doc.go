// Package e2e provides end-to-end tests for the Pilot issue-to-merge cycle.
//
// These tests verify the complete workflow:
//  1. Issue created with `pilot` label
//  2. Executor picks up and runs (mocked)
//  3. PR created
//  4. CI passes (mocked)
//  5. PR merged
//  6. Issue closed
//
// Run with: go test -v ./e2e/...
//
// Skip in short mode: go test -short ./...
//
// The tests use mocked GitHub API (via httptest.NewServer) and optionally
// mocked Claude Code execution. This allows testing the full autopilot
// state machine without external dependencies.
//
// # Test Structure
//
//   - workflow_test.go: Main E2E workflow tests
//   - mocks/github.go: Mock GitHub API server
//   - mocks/claude.go: Mock Claude Code executable
//
// # Running E2E Tests
//
//	make test-e2e
//
// Or directly:
//
//	go test -v -count=1 ./e2e/...
package e2e
