package intent

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

		// Research
		{"research article", "research this article", IntentResearch},
		{"analyze codebase", "analyze the codebase", IntentResearch},
		{"review PR", "review PR #123", IntentResearch},
		{"investigate issue", "investigate the memory leak", IntentResearch},
		{"summarize doc", "summarize this document", IntentResearch},
		{"compare options", "compare these two approaches", IntentResearch},
		{"evaluate solution", "evaluate this solution", IntentResearch},
		{"assess risk", "assess the security risk", IntentResearch},
		{"please research", "please research this topic", IntentResearch},
		{"can you analyze", "can you analyze the logs", IntentResearch},

		// Planning
		{"plan auth", "plan how to add auth", IntentPlanning},
		{"design api", "design the API", IntentPlanning},
		{"strategy scaling", "strategy for scaling", IntentPlanning},
		{"how should we", "how should we handle errors", IntentPlanning},
		{"approach for", "approach for caching", IntentPlanning},
		{"architect system", "architect the system", IntentPlanning},
		{"outline feature", "outline the feature", IntentPlanning},

		// Chat (without ? to avoid question priority)
		{"what do you think", "what do you think about Redis", IntentChat},
		{"should I use", "should i use TypeScript", IntentChat},
		{"opinion on", "opinion on microservices", IntentChat},
		{"thoughts about", "thoughts about this approach", IntentChat},
		{"do you recommend", "do you recommend using Go", IntentChat},
		{"is it better", "is it better to use SQL or NoSQL", IntentChat},
		{"discuss topic", "discuss the tradeoffs", IntentChat},
		{"lets talk about", "lets talk about performance", IntentChat},
		// Chat with ? still becomes chat (chat checked before question)
		{"chat with question mark", "what do you think about Redis?", IntentChat},

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
		{IntentCommand, "Command"},
		{IntentGreeting, "Greeting"},
		{IntentResearch, "Research"},
		{IntentPlanning, "Planning"},
		{IntentQuestion, "Question"},
		{IntentChat, "Chat"},
		{IntentTask, "Task"},
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
			got := IsGreeting(tt.message)
			if got != tt.expected {
				t.Errorf("IsGreeting(%q) = %v, want %v", tt.message, got, tt.expected)
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
			got := IsQuestion(tt.message)
			if got != tt.expected {
				t.Errorf("IsQuestion(%q) = %v, want %v", tt.message, got, tt.expected)
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
			got := ContainsTaskReference(tt.message)
			if got != tt.expected {
				t.Errorf("ContainsTaskReference(%q) = %v, want %v", tt.message, got, tt.expected)
			}
		})
	}
}

// TestIsTask tests the IsTask function
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
		{"i need fix for this", true}, // "i need <action>" pattern
		{"i want update docs", true},  // "i want <action>" pattern
		{"hello world", false},
		{"what is this", false},
		{"show me the code", false},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := IsTask(tt.message)
			if got != tt.expected {
				t.Errorf("IsTask(%q) = %v, want %v", tt.message, got, tt.expected)
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
		// Note: "review" moved to research patterns (GH-290)
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
		{"i need fix for this", true},    // "i need <action>" pattern (no "to" between)
		{"i want update the docs", true}, // "i want <action>" pattern (no "to" between)
		// Non-action messages
		{"hello", false},
		{"what is this", false},
		{"show me", false},
		{"explain how", false},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := ContainsActionWord(tt.message)
			if got != tt.expected {
				t.Errorf("ContainsActionWord(%q) = %v, want %v", tt.message, got, tt.expected)
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
			got := IsLikelyGreeting(tt.message)
			if got != tt.expected {
				t.Errorf("IsLikelyGreeting(%q) = %v, want %v", tt.message, got, tt.expected)
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
		{IntentCommand, "command"},
		{IntentGreeting, "greeting"},
		{IntentResearch, "research"},
		{IntentPlanning, "planning"},
		{IntentQuestion, "question"},
		{IntentChat, "chat"},
		{IntentTask, "task"},
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
	patterns := []string{
		"hi", "hello", "hey", "hola", "привет", "yo", "sup",
		"good morning", "good afternoon", "good evening",
		"howdy", "greetings", "what's up", "whats up",
	}
	for _, pattern := range patterns {
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
	// Note: "review" excluded as it's now a research pattern (see TestIsResearch)
	actions := []string{
		"create", "add", "make", "build", "implement",
		"fix", "update", "modify", "change", "edit",
		"delete", "remove", "refactor", "write",
		"generate", "setup", "configure", "install",
		"prioritize", "reprioritize", "reorder",
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
		{"07", IntentTask},    // task reference
		{"#5", IntentTask},    // issue reference
		{"fix", IntentTask},   // action word
		{"?", IntentQuestion}, // question mark triggers question
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
		{IntentCommand, "command"},
		{IntentGreeting, "greeting"},
		{IntentResearch, "research"},
		{IntentPlanning, "planning"},
		{IntentQuestion, "question"},
		{IntentChat, "chat"},
		{IntentTask, "task"},
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

// TestIsResearch tests research intent detection
func TestIsResearch(t *testing.T) {
	tests := []struct {
		message  string
		expected bool
	}{
		{"research this topic", true},
		{"analyze the codebase", true},
		{"review the PR", true},
		{"investigate the bug", true},
		{"summarize the document", true},
		{"compare these options", true},
		{"evaluate the solution", true},
		{"assess the risk", true},
		{"please research this", true},
		{"can you analyze this", true},
		{"i need research on this", true},
		{"hello", false},
		{"create a file", false},
		{"what is this", false},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := IsResearch(tt.message)
			if got != tt.expected {
				t.Errorf("IsResearch(%q) = %v, want %v", tt.message, got, tt.expected)
			}
		})
	}
}

// TestIsPlanning tests planning intent detection
func TestIsPlanning(t *testing.T) {
	tests := []struct {
		message  string
		expected bool
	}{
		{"plan how to add auth", true},
		{"design the API", true},
		{"strategy for scaling", true},
		{"how should we handle this", true},
		{"approach for caching", true},
		{"architect the system", true},
		{"outline the feature", true},
		{"hello", false},
		{"create a file", false},
		{"what is this", false},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := IsPlanning(tt.message)
			if got != tt.expected {
				t.Errorf("IsPlanning(%q) = %v, want %v", tt.message, got, tt.expected)
			}
		})
	}
}

// TestIsChat tests chat intent detection
func TestIsChat(t *testing.T) {
	tests := []struct {
		message  string
		expected bool
	}{
		{"what do you think about Redis", true},
		{"opinion on microservices", true},
		{"thoughts about this approach", true},
		{"do you recommend Go", true},
		{"should i use TypeScript", true},
		{"is it better to use SQL", true},
		{"discuss the tradeoffs", true},
		{"let's talk about performance", true},
		{"lets talk about caching", true},
		{"hello", false},
		{"create a file", false},
		{"how do I run tests", false},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := IsChat(tt.message)
			if got != tt.expected {
				t.Errorf("IsChat(%q) = %v, want %v", tt.message, got, tt.expected)
			}
		})
	}
}

// TestNewIntentPriority tests that new intents have correct priority
func TestNewIntentPriority(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		expected Intent
	}{
		// Research takes priority over task (review is both)
		{"research over task", "research this article", IntentResearch},

		// Planning takes priority over question (even with ?)
		{"planning over question", "how should we handle this?", IntentPlanning},

		// Chat without action words stays chat (no ? mark)
		{"chat without action", "what do you think about Go", IntentChat},

		// Chat with ? still becomes chat (chat checked before question)
		{"chat with question mark", "what do you think about Go?", IntentChat},

		// Questions with ? still work
		{"question with ?", "what is this?", IntentQuestion},

		// Tasks with action words still work
		{"task with action", "create a new file", IntentTask},

		// Research with can you prefix
		{"research with prefix", "can you analyze this code", IntentResearch},
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

// TestIsEphemeralTask tests ephemeral task detection for PR skipping (GH-265)
func TestIsEphemeralTask(t *testing.T) {
	tests := []struct {
		name        string
		description string
		expected    bool
	}{
		// Ephemeral: serve/run commands
		{"serve the app", "serve the app", true},
		{"run dev server", "run dev server", true},
		{"start the app", "start the app", true},
		{"launch dev", "launch dev", true},
		{"boot the server", "boot the server", true},

		// Ephemeral: with polite prefixes
		{"please serve", "please serve the app", true},
		{"can you run", "can you run the server", true},
		{"could you start", "could you start dev", true},
		{"i need to run", "i need to run the app", true},
		{"i want to serve", "i want to serve locally", true},

		// Ephemeral: package manager commands
		{"npm run dev", "npm run dev", true},
		{"yarn dev", "yarn dev", true},
		{"pnpm start", "pnpm start", true},
		{"cargo run", "cargo run", true},
		{"go run main.go", "go run main.go", true},
		{"python -m flask", "python -m flask run", true},

		// Ephemeral: make commands
		{"make dev", "make dev", true},
		{"make serve", "make serve", true},
		{"make run", "make run", true},
		{"make start", "make start", true},

		// Ephemeral: dev server keywords
		{"dev server", "start the dev server", true},
		{"local server", "run local server", true},
		{"localhost", "serve on localhost", true},
		{"development server", "boot development server", true},
		{"preview server", "launch preview server", true},

		// Ephemeral: standalone check/test (short descriptions)
		{"test short", "test", true},
		{"check short", "check", true},
		{"lint short", "lint", true},
		{"build short", "build", true},
		{"format code", "format code", true},
		{"validate schema", "validate schema", true},

		// NOT ephemeral: modification tasks
		{"fix the login bug", "fix the login bug", false},
		{"add user auth", "add user authentication", false},
		{"update readme", "update the readme", false},
		{"create handler", "create a new handler", false},
		{"implement feature", "implement user logout", false},
		{"refactor auth", "refactor the auth module", false},

		// NOT ephemeral: test with modification intent
		{"fix the test", "fix the test", false},
		{"add test for auth", "add test for auth", false},
		{"update test cases", "update test cases", false},
		{"write tests", "write tests for login", false},

		// NOT ephemeral: longer descriptions even with ephemeral words
		{"run but long", "run the migration and update schema", false},
		{"check but modify", "check and fix the linting errors", false},

		// Edge cases
		{"empty string", "", false},
		{"whitespace", "   ", false},
		{"mixed case serve", "SERVE the app", true},
		{"mixed case run", "Run Dev Server", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsEphemeralTask(tt.description)
			if got != tt.expected {
				t.Errorf("IsEphemeralTask(%q) = %v, want %v", tt.description, got, tt.expected)
			}
		})
	}
}

// TestIsEphemeralTaskPatterns tests specific pattern groups
func TestIsEphemeralTaskPatterns(t *testing.T) {
	// All start patterns should be detected
	startPatterns := []string{
		"serve", "run", "start", "launch", "boot",
		"npm run", "yarn", "pnpm", "cargo run", "go run", "python -m",
		"make dev", "make serve", "make run", "make start",
	}

	for _, pattern := range startPatterns {
		t.Run("start_"+pattern, func(t *testing.T) {
			desc := pattern + " something"
			if !IsEphemeralTask(desc) {
				t.Errorf("IsEphemeralTask(%q) = false, expected true for start pattern", desc)
			}
		})
	}

	// Contains patterns should be detected
	containsPatterns := []string{
		"dev server", "local server", "localhost",
		"development server", "preview server",
	}

	for _, pattern := range containsPatterns {
		t.Run("contains_"+pattern, func(t *testing.T) {
			desc := "start the " + pattern
			if !IsEphemeralTask(desc) {
				t.Errorf("IsEphemeralTask(%q) = false, expected true for contains pattern", desc)
			}
		})
	}
}

// TestIsClearQuestion tests the IsClearQuestion function for LLM pre-check (GH-382)
func TestIsClearQuestion(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		// Should be clear questions - ends with ?
		{"What's in roadmap?", true},
		{"What's in the backlog?", true},
		{"How does auth work?", true},
		{"Where is the config file?", true},
		{"Why is this failing?", true},
		{"Can you explain the architecture?", true},

		// Should be clear questions - question starters (no ?)
		{"What's in roadmap", true},
		{"What is in the backlog", true},
		{"How does the auth system work", true},
		{"How do I run tests", true},
		{"How can I debug this", true},
		{"Where is the config", true},
		{"Where are the tests", true},
		{"Why is this failing", true},
		{"Why are we using Go", true},
		{"Why does it crash", true},
		{"When is the release", true},
		{"When does it deploy", true},
		{"When will it be ready", true},
		{"Who is the maintainer", true},
		{"Who are the contributors", true},
		{"Which library should I use", true},
		{"Can you explain the flow", true},
		{"Could you explain the architecture", true},

		// Should NOT be clear questions (need LLM)
		{"Add a logout button", false},
		{"Fix the auth bug", false},
		{"What do you think about adding X", false}, // chat, not question
		{"Create a new endpoint", false},
		{"Implement feature X", false},
		{"Hello", false},
		{"Hi there", false},
		{"Research authentication methods", false},
		{"Plan the migration", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := IsClearQuestion(tt.input)
			if got != tt.expected {
				t.Errorf("IsClearQuestion(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

// TestContainsModificationIntent tests modification intent detection
func TestContainsModificationIntent(t *testing.T) {
	tests := []struct {
		description string
		expected    bool
	}{
		{"fix the bug", true},
		{"add new feature", true},
		{"update config", true},
		{"change settings", true},
		{"modify handler", true},
		{"write tests", true},
		{"create file", true},
		{"implement auth", true},
		{"refactor code", true},
		{"serve the app", false},
		{"run tests", false},
		{"check status", false},
		{"build project", false},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			got := ContainsModificationIntent(tt.description)
			if got != tt.expected {
				t.Errorf("ContainsModificationIntent(%q) = %v, want %v", tt.description, got, tt.expected)
			}
		})
	}
}
