package telegram

import (
	"regexp"
	"strings"
)

// Intent represents the detected intent of a message
type Intent string

const (
	IntentCommand  Intent = "command"
	IntentGreeting Intent = "greeting"
	IntentResearch Intent = "research"
	IntentPlanning Intent = "planning"
	IntentQuestion Intent = "question"
	IntentChat     Intent = "chat"
	IntentTask     Intent = "task"
)

// Common greeting patterns
var greetingPatterns = []string{
	"hi", "hello", "hey", "hola", "привет", "yo", "sup",
	"good morning", "good afternoon", "good evening",
	"howdy", "greetings", "what's up", "whats up",
}

// Question indicators
var questionPatterns = []string{
	"what is", "what are", "what's", "whats", "what does", "what do",
	"how do", "how does", "how can", "how to",
	"where is", "where are", "where's",
	"why is", "why are", "why does",
	"when is", "when does", "when will",
	"which", "who is", "who are",
	"can you tell", "could you explain",
	"do you know", "is there", "are there",
}

// Research patterns - indicate research/analysis requests
var researchPatterns = []string{
	"research", "analyze", "review", "investigate",
	"summarize", "compare", "evaluate", "assess",
}

// Planning patterns - indicate planning/design requests
var planningPatterns = []string{
	"plan", "design", "strategy", "how should we",
	"approach for", "architect", "outline",
}

// Chat patterns - indicate conversational/opinion requests
var chatPatterns = []string{
	"what do you think", "opinion on", "thoughts about",
	"do you recommend", "should i", "is it better",
	"discuss", "let's talk about", "lets talk about",
}

// Task action words that indicate a task request
var taskActionWords = []string{
	"create", "add", "make", "build", "implement",
	"fix", "update", "modify", "change", "edit",
	"delete", "remove", "refactor", "write",
	"generate", "setup", "configure", "install",
	// Meta-task actions (managing backlog, priorities, etc.)
	// Note: "review" moved to researchPatterns per GH-290
	"prioritize", "reprioritize", "reorder",
	"sort", "organize", "rank", "triage", "set priority",
}

// DetectIntent analyzes a message and returns the detected intent
// Priority order: Command > Greeting > Research > Planning > Question > Chat > Task
func DetectIntent(message string) Intent {
	// Normalize message
	msg := strings.ToLower(strings.TrimSpace(message))

	// 1. Commands start with /
	if strings.HasPrefix(msg, "/") {
		return IntentCommand
	}

	// 2. Check for greetings (short messages that are just greetings)
	if isGreeting(msg) {
		return IntentGreeting
	}

	// 3. Check for research requests (research patterns with topic/URL)
	if isResearch(msg) {
		return IntentResearch
	}

	// 4. Check for planning requests
	if isPlanning(msg) {
		return IntentPlanning
	}

	// 5. Check for chat/conversational (opinion-seeking, no action words)
	// Checked before questions because "what do you think" starts with "what"
	if isChat(msg) && !containsActionWord(msg) {
		return IntentChat
	}

	// 6. Check for questions (ends with ? or question starters)
	if isQuestion(msg) {
		return IntentQuestion
	}

	// 7. Check for task action words
	if isTask(msg) {
		return IntentTask
	}

	// Check for task-like references (numbers, IDs, file names)
	if containsTaskReference(msg) {
		return IntentTask
	}

	// Default: if message is very short AND looks like a greeting, treat as greeting
	// Otherwise treat as task (will get confirmation anyway)
	if len(msg) < 15 && isLikelyGreeting(msg) {
		return IntentGreeting
	}

	return IntentTask
}

// containsTaskReference checks if message references a task, file, or specific item
func containsTaskReference(msg string) bool {
	// Task IDs, issue numbers, file names
	patterns := []string{
		`task[- ]?\d+`, // TASK-01, task 01
		`#\d+`,         // #123
		`\d{2,}`,       // numbers like 04, 123
		`\.\w{2,4}$`,   // file extensions
		`pick|select|open|show|do|run|work on|start`,
	}
	for _, pattern := range patterns {
		if matched, _ := regexp.MatchString(pattern, msg); matched {
			return true
		}
	}
	return false
}

// isLikelyGreeting checks if a short message is likely just a greeting
func isLikelyGreeting(msg string) bool {
	words := strings.Fields(msg)
	if len(words) > 3 {
		return false
	}
	for _, pattern := range greetingPatterns {
		if msg == pattern || strings.HasPrefix(msg, pattern+" ") ||
			strings.HasPrefix(msg, pattern+"!") || strings.HasPrefix(msg, pattern+",") {
			return true
		}
	}
	return false
}

// isGreeting checks if the message is a greeting
func isGreeting(msg string) bool {
	// Very short messages that are just greetings
	words := strings.Fields(msg)
	if len(words) <= 3 {
		for _, pattern := range greetingPatterns {
			if msg == pattern || strings.HasPrefix(msg, pattern+" ") || strings.HasPrefix(msg, pattern+"!") || strings.HasPrefix(msg, pattern+",") {
				return true
			}
		}
	}
	return false
}

// isQuestion checks if the message is a question
func isQuestion(msg string) bool {
	// Ends with question mark
	if strings.HasSuffix(msg, "?") {
		return true
	}

	// Starts with question patterns
	for _, pattern := range questionPatterns {
		if strings.HasPrefix(msg, pattern) {
			return true
		}
	}

	// Quick-info keywords that should be treated as questions (fast-path eligible)
	quickInfoKeywords := []string{
		"issues", "tasks", "backlog", "todos", "fixmes",
		"status", "progress", "state",
	}
	for _, keyword := range quickInfoKeywords {
		if strings.Contains(msg, keyword) && !containsActionWord(msg) {
			return true
		}
	}

	// Contains question-like phrases
	questionPhrases := []string{
		"tell me about", "explain", "describe",
		"show me", "list all", "find all", "list",
	}
	for _, phrase := range questionPhrases {
		if strings.Contains(msg, phrase) && !containsActionWord(msg) {
			return true
		}
	}

	return false
}

// isTask checks if the message looks like a task request
func isTask(msg string) bool {
	return containsActionWord(msg)
}

// isResearch checks if the message is a research/analysis request
func isResearch(msg string) bool {
	for _, pattern := range researchPatterns {
		// Check for pattern at start or after common prefixes
		patterns := []string{
			"^" + pattern + "\\b",           // starts with pattern
			"\\bplease " + pattern + "\\b",  // please + pattern
			"\\bcan you " + pattern + "\\b", // can you + pattern
			"\\bi need " + pattern + "\\b",  // i need + pattern
			"\\bi want " + pattern + "\\b",  // i want + pattern
		}
		for _, p := range patterns {
			if matched, _ := regexp.MatchString(p, msg); matched {
				return true
			}
		}
	}
	return false
}

// isPlanning checks if the message is a planning/design request
func isPlanning(msg string) bool {
	for _, pattern := range planningPatterns {
		// Use word boundary matching to avoid "architect" matching "architecture"
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(pattern) + `\b`)
		if re.MatchString(msg) {
			return true
		}
	}
	return false
}

// isChat checks if the message is conversational/opinion-seeking
func isChat(msg string) bool {
	for _, pattern := range chatPatterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}

// containsActionWord checks if message contains task action words
func containsActionWord(msg string) bool {
	for _, action := range taskActionWords {
		// Check for action word at start or after common prefixes
		patterns := []string{
			"^" + action + "\\b",           // starts with action
			"\\bplease " + action + "\\b",  // please + action
			"\\bcan you " + action + "\\b", // can you + action
			"\\bi need " + action + "\\b",  // i need + action
			"\\bi want " + action + "\\b",  // i want + action
		}
		for _, pattern := range patterns {
			if matched, _ := regexp.MatchString(pattern, msg); matched {
				return true
			}
		}
	}
	return false
}

// isClearQuestion checks if message is unambiguously a question.
// These patterns are high-confidence and don't need LLM verification.
// Used as pre-check before LLM classification to avoid false Task classifications.
func isClearQuestion(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))

	// Ends with question mark - very clear signal
	if strings.HasSuffix(lower, "?") {
		return true
	}

	// Clear question starters that rarely indicate tasks
	clearPatterns := []string{
		"what's in", "what is in", "whats in",
		"what's the", "what is the", "whats the",
		"how does", "how do", "how can",
		"where is", "where are", "where's",
		"why is", "why are", "why does",
		"when is", "when does", "when will",
		"who is", "who are",
		"which", "can you explain", "could you explain",
	}

	for _, pattern := range clearPatterns {
		if strings.HasPrefix(lower, pattern) {
			return true
		}
	}

	return false
}

// IntentDescription returns a human-readable description of the intent
func (i Intent) Description() string {
	switch i {
	case IntentCommand:
		return "Command"
	case IntentGreeting:
		return "Greeting"
	case IntentResearch:
		return "Research"
	case IntentPlanning:
		return "Planning"
	case IntentQuestion:
		return "Question"
	case IntentChat:
		return "Chat"
	case IntentTask:
		return "Task"
	default:
		return "Unknown"
	}
}

// Ephemeral task patterns - commands that run something but don't produce code changes
var ephemeralStartPatterns = []string{
	"serve", "run", "start", "launch", "boot",
	"npm run", "yarn", "pnpm", "cargo run", "go run", "python -m",
	"make dev", "make serve", "make run", "make start",
}

var ephemeralContainsPatterns = []string{
	"dev server", "local server", "localhost",
	"development server", "preview server",
}

var ephemeralStandalonePatterns = []string{
	"check", "test", "validate", "verify", "lint", "format",
	"build", "compile", "bundle",
}

// IsEphemeralTask checks if a task description represents an ephemeral/run command
// that shouldn't create a PR (e.g., "serve the app", "run dev server", "npm run dev")
func IsEphemeralTask(description string) bool {
	desc := strings.ToLower(strings.TrimSpace(description))

	// Early exit: if there's a modification intent, it's not ephemeral
	if containsModificationIntent(desc) {
		return false
	}

	// Check start patterns (commands that begin with serve/run/etc.)
	for _, pattern := range ephemeralStartPatterns {
		if strings.HasPrefix(desc, pattern) {
			return true
		}
		// Also check with common prefixes
		prefixes := []string{"please ", "can you ", "could you ", "i need to ", "i want to "}
		for _, prefix := range prefixes {
			if strings.HasPrefix(desc, prefix+pattern) {
				return true
			}
		}
	}

	// Check contains patterns (dev server, localhost, etc.)
	for _, pattern := range ephemeralContainsPatterns {
		if strings.Contains(desc, pattern) {
			return true
		}
	}

	// Check standalone patterns - only if the description is short and focused
	// (to avoid false positives like "fix the test" which should create a PR)
	words := strings.Fields(desc)
	if len(words) <= 4 {
		for _, pattern := range ephemeralStandalonePatterns {
			// Match exact word at start: "test", "check status", "lint code"
			if strings.HasPrefix(desc, pattern+" ") || desc == pattern {
				return true
			}
		}
	}

	return false
}

// containsModificationIntent checks if the description implies code changes
func containsModificationIntent(desc string) bool {
	modWords := []string{"fix", "add", "update", "change", "modify", "write", "create", "implement", "refactor"}
	for _, word := range modWords {
		if strings.Contains(desc, word) {
			return true
		}
	}
	return false
}
