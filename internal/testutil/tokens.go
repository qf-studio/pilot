// Package testutil provides testing utilities for the pilot project.
package testutil

// Safe test tokens that won't trigger GitHub's push protection.
// These are intentionally simple and obviously fake to avoid secret scanning.
//
// ❌ DON'T use patterns like: xoxb-123456789012-1234567890123-abcdefghij
// ✅ DO use these constants or similarly obvious fakes.
const (
	// FakeSlackBotToken is a safe test token for Slack bot authentication.
	FakeSlackBotToken = "test-slack-bot-token"

	// FakeSlackAppToken is a safe test token for Slack app authentication.
	FakeSlackAppToken = "test-slack-app-token"

	// FakeSlackWebhookURL is a safe test URL for Slack webhooks.
	FakeSlackWebhookURL = "https://hooks.slack.test/services/TEST/WEBHOOK/URL"

	// FakeGitHubToken is a safe test token for GitHub API authentication.
	FakeGitHubToken = "test-github-token"

	// FakeGitHubPAT is a safe test personal access token for GitHub.
	FakeGitHubPAT = "test-github-pat"

	// FakeOpenAIKey is a safe test API key for OpenAI.
	FakeOpenAIKey = "test-openai-api-key"

	// FakeAnthropicKey is a safe test API key for Anthropic.
	FakeAnthropicKey = "test-anthropic-api-key"

	// FakeLinearAPIKey is a safe test API key for Linear.
	FakeLinearAPIKey = "test-linear-api-key"

	// FakeAWSAccessKeyID is a safe test AWS access key ID.
	FakeAWSAccessKeyID = "test-aws-access-key-id"

	// FakeAWSSecretKey is a safe test AWS secret access key.
	FakeAWSSecretKey = "test-aws-secret-key"

	// FakeJWT is a safe test JWT token.
	FakeJWT = "test.jwt.token"

	// FakeBearerToken is a safe test bearer token.
	FakeBearerToken = "test-bearer-token"
)
