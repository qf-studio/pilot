package telegram

import (
	"regexp"
	"strings"
)

// Intent represents the detected intent of a message
type Intent string

const (
	IntentGreeting Intent = "greeting"
	IntentQuestion Intent = "question"
	IntentTask     Intent = "task"
	IntentCommand  Intent = "command"
)

// Common greeting patterns
var greetingPatterns = []string{
	"hi", "hello", "hey", "hola", "привет", "yo", "sup",
	"good morning", "good afternoon", "good evening",
	"howdy", "greetings", "what's up", "whats up",
}

// Question indicators
var questionPatterns = []string{
	"what is", "what are", "what's", "whats",
	"how do", "how does", "how can", "how to",
	"where is", "where are", "where's",
	"why is", "why are", "why does",
	"when is", "when does", "when will",
	"which", "who is", "who are",
	"can you tell", "could you explain",
	"do you know", "is there", "are there",
}

// Task action words that indicate a task request
var taskActionWords = []string{
	"create", "add", "make", "build", "implement",
	"fix", "update", "modify", "change", "edit",
	"delete", "remove", "refactor", "write",
	"generate", "setup", "configure", "install",
}

// DetectIntent analyzes a message and returns the detected intent
func DetectIntent(message string) Intent {
	// Normalize message
	msg := strings.ToLower(strings.TrimSpace(message))

	// Commands start with /
	if strings.HasPrefix(msg, "/") {
		return IntentCommand
	}

	// Check for greetings (short messages that are just greetings)
	if isGreeting(msg) {
		return IntentGreeting
	}

	// Check for questions
	if isQuestion(msg) {
		return IntentQuestion
	}

	// Check for task action words
	if isTask(msg) {
		return IntentTask
	}

	// Default: if message is short and doesn't match patterns, treat as greeting
	// If longer, treat as task
	if len(msg) < 20 && !containsActionWord(msg) {
		return IntentGreeting
	}

	return IntentTask
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

	// Contains question-like phrases
	questionPhrases := []string{
		"tell me about", "explain", "describe",
		"show me", "list all", "find all",
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

// IntentDescription returns a human-readable description of the intent
func (i Intent) Description() string {
	switch i {
	case IntentGreeting:
		return "Greeting"
	case IntentQuestion:
		return "Question"
	case IntentTask:
		return "Task"
	case IntentCommand:
		return "Command"
	default:
		return "Unknown"
	}
}
