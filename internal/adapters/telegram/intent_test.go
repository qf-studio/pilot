package telegram

import (
	"testing"
)

func TestDetectIntent(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		expected Intent
	}{
		// Commands
		{"command /help", "/help", IntentCommand},
		{"command /start", "/start", IntentCommand},
		{"command /status", "/status", IntentCommand},
		{"command /cancel", "/cancel", IntentCommand},

		// Greetings
		{"greeting hi", "hi", IntentGreeting},
		{"greeting hello", "hello", IntentGreeting},
		{"greeting hey", "hey", IntentGreeting},
		{"greeting hello!", "hello!", IntentGreeting},
		{"greeting good morning", "good morning", IntentGreeting},
		{"greeting hi there", "hi there", IntentGreeting},
		{"greeting привет", "привет", IntentGreeting},
		{"greeting yo", "yo", IntentGreeting},

		// Questions
		{"question with ?", "what is the auth handler?", IntentQuestion},
		{"question what is", "what is the project structure", IntentQuestion},
		{"question how do", "how do I run tests", IntentQuestion},
		{"question where is", "where is the config file", IntentQuestion},
		{"question can you tell", "can you tell me about the api", IntentQuestion},
		{"question show me", "show me the error handlers", IntentQuestion},
		{"question explain", "explain the auth flow", IntentQuestion},

		// Tasks
		{"task create", "create a new file", IntentTask},
		{"task add", "add a function to handle auth", IntentTask},
		{"task fix", "fix the bug in login", IntentTask},
		{"task update", "update the readme", IntentTask},
		{"task implement", "implement user logout", IntentTask},
		{"task refactor", "refactor the auth module", IntentTask},
		{"task please add", "please add error handling", IntentTask},
		{"task pick", "pick 04", IntentTask},
		{"task with ID", "work on TASK-04", IntentTask},
		{"task with number", "do 04", IntentTask},

		// Meta-task actions (backlog management)
		{"meta-task review", "review tasks", IntentTask},
		{"meta-task prioritize", "prioritize the backlog", IntentTask},
		{"meta-task reorder", "reorder tasks by priority", IntentTask},
		{"meta-task triage", "triage issues", IntentTask},
		{"meta-task set priority", "set priority for tasks", IntentTask},
		{"meta-task review with context", "review tasks, set new priority by value", IntentTask},

		// Edge cases
		{"what does question", "what does the auth module do", IntentQuestion},
		{"ambiguous greeting", "hello world file", IntentGreeting}, // "hello" starts msg, <= 3 words
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectIntent(tt.message)
			if got != tt.expected {
				t.Errorf("DetectIntent(%q) = %v, want %v", tt.message, got, tt.expected)
			}
		})
	}
}

func TestIntentDescription(t *testing.T) {
	tests := []struct {
		intent   Intent
		expected string
	}{
		{IntentGreeting, "Greeting"},
		{IntentQuestion, "Question"},
		{IntentTask, "Task"},
		{IntentCommand, "Command"},
		{Intent("unknown"), "Unknown"},
	}

	for _, tt := range tests {
		t.Run(string(tt.intent), func(t *testing.T) {
			got := tt.intent.Description()
			if got != tt.expected {
				t.Errorf("%v.Description() = %v, want %v", tt.intent, got, tt.expected)
			}
		})
	}
}

func TestIsGreeting(t *testing.T) {
	tests := []struct {
		message  string
		expected bool
	}{
		{"hi", true},
		{"hello", true},
		{"hey", true},
		{"hi there", true},
		{"hello!", true},
		{"hello, how are you", false}, // Too long
		{"hi can you help", false},    // Too long
		{"hiya", false},               // Not exact match
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := isGreeting(tt.message)
			if got != tt.expected {
				t.Errorf("isGreeting(%q) = %v, want %v", tt.message, got, tt.expected)
			}
		})
	}
}

func TestIsQuestion(t *testing.T) {
	tests := []struct {
		message  string
		expected bool
	}{
		{"what is the structure?", true},
		{"how do I run this", true},
		{"where is config", true},
		{"explain the auth", true},
		{"show me the files", true},
		{"create a file", false}, // Task word takes precedence
		{"fix the bug", false},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := isQuestion(tt.message)
			if got != tt.expected {
				t.Errorf("isQuestion(%q) = %v, want %v", tt.message, got, tt.expected)
			}
		})
	}
}

func TestContainsTaskReference(t *testing.T) {
	tests := []struct {
		message  string
		expected bool
	}{
		{"work on task-04", true},
		{"pick 04", true},
		{"do #123", true},
		{"TASK-123", true},
		{"hello", false},
		{"what is this", false},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := containsTaskReference(tt.message)
			if got != tt.expected {
				t.Errorf("containsTaskReference(%q) = %v, want %v", tt.message, got, tt.expected)
			}
		})
	}
}
