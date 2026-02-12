// Package mocks provides mock implementations for E2E testing of Pilot.
//
// This package provides:
//   - GitHubMock: A mock GitHub API server that simulates GitHub behavior
//   - ClaudeCodeMock: A mock Claude Code executable for testing execution
//
// Example usage:
//
//	// Create GitHub mock
//	ghMock := mocks.NewGitHubMock()
//	defer ghMock.Close()
//
//	// Create issue with pilot label
//	issue := ghMock.CreateIssue("Test task", "Do something", []string{"pilot"})
//
//	// Set CI to pass
//	ghMock.SetCIPassing("sha123", []string{"build", "test"})
//
//	// Create Claude mock
//	claudeMock, _ := mocks.NewClaudeCodeMock()
//	defer claudeMock.Close()
package mocks
