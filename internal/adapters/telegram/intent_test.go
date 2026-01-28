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

// TestIsTask tests the isTask function
func TestIsTask(t *testing.T) {
	tests := []struct {
		message  string
		expected bool
	}{
		{"create a new file", true},
		{"add authentication", true},
		{"fix the bug", true},
		{"update the readme", true},
		{"implement feature x", true},
		{"refactor the code", true},
		{"delete old files", true},
		{"remove unused imports", true},
		{"please create a file", true},
		{"can you add a test", true},
		{"i need fix for this", true},  // "i need <action>" pattern
		{"i want update docs", true},   // "i want <action>" pattern
		{"hello world", false},
		{"what is this", false},
		{"show me the code", false},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := isTask(tt.message)
			if got != tt.expected {
				t.Errorf("isTask(%q) = %v, want %v", tt.message, got, tt.expected)
			}
		})
	}
}

// TestContainsActionWord tests action word detection
func TestContainsActionWord(t *testing.T) {
	tests := []struct {
		message  string
		expected bool
	}{
		// Starts with action
		{"create file", true},
		{"add test", true},
		{"fix bug", true},
		{"update docs", true},
		{"implement feature", true},
		{"refactor code", true},
		{"delete file", true},
		{"remove line", true},
		{"generate report", true},
		{"setup project", true},
		{"configure settings", true},
		{"install package", true},
		{"write test", true},
		{"build project", true},
		{"make changes", true},
		{"modify file", true},
		{"change config", true},
		{"edit code", true},
		// Meta-task actions
		{"review tasks", true},
		{"prioritize backlog", true},
		{"reorder items", true},
		{"sort list", true},
		{"organize files", true},
		{"rank tasks", true},
		{"triage issues", true},
		{"set priority high", true},
		// With prefixes
		{"please create a file", true},
		{"can you add a test", true},
		{"i need fix for this", true},      // "i need <action>" pattern (no "to" between)
		{"i want update the docs", true},   // "i want <action>" pattern (no "to" between)
		// Non-action messages
		{"hello", false},
		{"what is this", false},
		{"show me", false},
		{"explain how", false},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := containsActionWord(tt.message)
			if got != tt.expected {
				t.Errorf("containsActionWord(%q) = %v, want %v", tt.message, got, tt.expected)
			}
		})
	}
}

// TestIsLikelyGreeting tests greeting detection for short messages
func TestIsLikelyGreeting(t *testing.T) {
	tests := []struct {
		message  string
		expected bool
	}{
		{"hi", true},
		{"hello", true},
		{"hey", true},
		{"hi there", true},
		{"hello!", true},
		{"hello,", true},
		{"good morning", true},
		{"good afternoon", true},
		{"good evening", true},
		{"howdy", true},
		{"greetings", true},
		{"what's up", true},
		{"whats up", true},
		{"hola", true},
		{"привет", true},
		{"yo", true},
		{"sup", true},
		{"hello how are you today", false}, // too long
		{"hi can you help me with this task", false}, // too long
		{"create file", false},
		{"fix bug", false},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := isLikelyGreeting(tt.message)
			if got != tt.expected {
				t.Errorf("isLikelyGreeting(%q) = %v, want %v", tt.message, got, tt.expected)
			}
		})
	}
}

// TestDetectIntentEdgeCases tests edge cases in intent detection
func TestDetectIntentEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		expected Intent
	}{
		// Commands always win
		{"slash command with text", "/help me with something", IntentCommand},
		{"slash command uppercase", "/STATUS", IntentCommand},

		// Short greetings
		{"just hi", "hi", IntentGreeting},
		{"hi with punctuation", "hi!", IntentGreeting},

		// Questions with question patterns
		{"what with question mark", "what is this?", IntentQuestion},
		{"how question", "how do I run tests", IntentQuestion},
		{"where question", "where is the config", IntentQuestion},
		{"why question", "why is this failing", IntentQuestion},
		{"explain phrase", "explain the auth flow", IntentQuestion},
		{"show me phrase", "show me the handlers", IntentQuestion},
		{"list phrase", "list all endpoints", IntentQuestion},

		// Questions with quick-info keywords
		{"issues keyword", "what are the issues", IntentQuestion},
		{"backlog keyword", "show backlog", IntentQuestion},
		{"todos keyword", "show me the todos", IntentQuestion},
		{"status keyword", "check status", IntentQuestion},

		// Tasks with action words
		{"create task", "create a new handler", IntentTask},
		{"fix task", "fix the login bug", IntentTask},
		{"add task", "add error handling", IntentTask},

		// Task references
		{"task id reference", "TASK-07", IntentTask},
		{"number reference", "07", IntentTask},
		{"pick command", "pick 04", IntentTask},

		// Ambiguous (defaults to task)
		{"ambiguous long", "something about the code that is unclear", IntentTask},
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

// TestIntentConstants tests intent constant values
func TestIntentConstants(t *testing.T) {
	// Verify intent constants have expected string values
	tests := []struct {
		intent   Intent
		expected string
	}{
		{IntentGreeting, "greeting"},
		{IntentQuestion, "question"},
		{IntentTask, "task"},
		{IntentCommand, "command"},
	}

	for _, tt := range tests {
		t.Run(string(tt.intent), func(t *testing.T) {
			if string(tt.intent) != tt.expected {
				t.Errorf("Intent = %q, want %q", string(tt.intent), tt.expected)
			}
		})
	}
}

// TestGreetingPatterns tests that all greeting patterns are recognized
func TestGreetingPatterns(t *testing.T) {
	// Each pattern should be recognized as greeting when alone
	for _, pattern := range greetingPatterns {
		t.Run(pattern, func(t *testing.T) {
			intent := DetectIntent(pattern)
			if intent != IntentGreeting {
				t.Errorf("DetectIntent(%q) = %v, want %v", pattern, intent, IntentGreeting)
			}
		})
	}
}

// TestQuestionPatterns tests that question patterns work
func TestQuestionPatterns(t *testing.T) {
	// Each pattern should trigger question detection
	testMessages := []string{
		"what is the project structure?",
		"how do I run the tests?",
		"where is the config file?",
		"why is this failing?",
		"when is the release?",
		"who is the maintainer?",
		"can you tell me about auth?",
		"do you know how this works?",
		"is there a test file?",
	}

	for _, msg := range testMessages {
		t.Run(msg, func(t *testing.T) {
			intent := DetectIntent(msg)
			if intent != IntentQuestion {
				t.Errorf("DetectIntent(%q) = %v, want %v", msg, intent, IntentQuestion)
			}
		})
	}
}

// TestTaskActionWords tests task action word patterns
func TestTaskActionWords(t *testing.T) {
	// All action words should be recognized
	actions := []string{
		"create", "add", "make", "build", "implement",
		"fix", "update", "modify", "change", "edit",
		"delete", "remove", "refactor", "write",
		"generate", "setup", "configure", "install",
		"review", "prioritize", "reprioritize", "reorder",
		"sort", "organize", "rank", "triage",
	}

	for _, action := range actions {
		t.Run(action, func(t *testing.T) {
			msg := action + " something"
			intent := DetectIntent(msg)
			if intent != IntentTask {
				t.Errorf("DetectIntent(%q) = %v, want %v", msg, intent, IntentTask)
			}
		})
	}
}

// TestQuestionKeywordsWithoutActions tests question keywords
func TestQuestionKeywordsWithoutActions(t *testing.T) {
	tests := []struct {
		message  string
		expected Intent
	}{
		{"what are the issues", IntentQuestion},
		{"show me the tasks", IntentQuestion},
		{"list the backlog", IntentQuestion},
		{"check the status", IntentQuestion},
		{"show todos", IntentQuestion},
		{"what are the fixmes", IntentQuestion},
		{"tell me about the project", IntentQuestion},
		{"describe the architecture", IntentQuestion},
		{"find all handlers", IntentQuestion},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := DetectIntent(tt.message)
			if got != tt.expected {
				t.Errorf("DetectIntent(%q) = %v, want %v", tt.message, got, tt.expected)
			}
		})
	}
}

// TestShortMessages tests classification of very short messages
func TestShortMessages(t *testing.T) {
	tests := []struct {
		message  string
		expected Intent
	}{
		{"hi", IntentGreeting},
		{"yo", IntentGreeting},
		{"07", IntentTask},       // task reference
		{"#5", IntentTask},       // issue reference
		{"fix", IntentTask},      // action word
		{"?", IntentQuestion},    // question mark triggers question
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := DetectIntent(tt.message)
			if got != tt.expected {
				t.Errorf("DetectIntent(%q) = %v, want %v", tt.message, got, tt.expected)
			}
		})
	}
}

// TestIntentStringConversion tests intent to string conversion
func TestIntentStringConversion(t *testing.T) {
	tests := []struct {
		intent Intent
		want   string
	}{
		{IntentGreeting, "greeting"},
		{IntentQuestion, "question"},
		{IntentTask, "task"},
		{IntentCommand, "command"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := string(tt.intent)
			if got != tt.want {
				t.Errorf("string(%v) = %q, want %q", tt.intent, got, tt.want)
			}
		})
	}
}
