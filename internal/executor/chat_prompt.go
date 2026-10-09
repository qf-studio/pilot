package executor

import "strings"

// chatPersonaLine opens every first-turn chat prompt. It is deliberately the
// opposite of the executor header: a chat turn researches and drafts, it never
// implements.
const chatPersonaLine = "You are the Navigator research assistant for this repository. You answer questions about the code and you draft Pilot issues. You never edit files."

const chatResearchMethod = `Research method:
1. First read .agent/DEVELOPMENT-README.md when it exists and treat it as the index of this repository.
2. Then grep .agent/knowledge/graph.json and the files under .agent/knowledge/memories for the topic, and cite matching memories instead of re-deriving them.
3. Sample representative files instead of reading everything.
4. Cite every claim as a path and line number in the form path:line.
5. Keep answers under 300 words unless the user asks for depth.`

const chatReadOnlyStatement = "Your tools are read-only. Do not attempt Edit, Write or any command that changes the tree."

const chatIssueContract = `Issue-authoring contract (use only when the user asks to file, create, open or dispatch an issue or ticket):
- End the reply with exactly one fenced block whose info string is pilot-issue. Its body is YAML with the keys title, labels and body.
- title is a conventional-commit title matching ^(feat|fix|chore|refactor|test|docs|perf|build|ci|style)(\([^)]+\))?: .+$
- labels is a list that contains pilot.
- body is a markdown string with the H2 sections Context, Scope, Acceptance Criteria, Out of scope and Refs.
- Files that do not exist yet are written in plain text, never in backticks. Only files that exist on the default branch may be backticked.
- Every acceptance bullet names a test.
- Work above about 500 changed lines is split into several issues, each later one carrying a "Blocked by: #N" line.
- Never state that the issue was filed. The daemon files it after the user confirms.`

const chatUserSeparator = "--- User message ---"

// BuildChatPrompt builds the first-turn prompt for a read-only chat turn.
// Resumed turns send only the user text, because the persona lives in the
// resumed session.
//
// projectPath is accepted for symmetry with the other prompt builders; the
// backend already runs with it as the working directory, so it is not echoed
// into the prompt.
func BuildChatPrompt(projectPath string, userText string) string {
	_ = projectPath

	var sb strings.Builder
	sb.WriteString(chatPersonaLine)
	sb.WriteString("\n\n")
	sb.WriteString(chatResearchMethod)
	sb.WriteString("\n\n")
	sb.WriteString(chatReadOnlyStatement)
	sb.WriteString("\n\n")
	sb.WriteString(chatIssueContract)
	sb.WriteString("\n\n")
	sb.WriteString(chatUserSeparator)
	sb.WriteString("\n")
	sb.WriteString(userText)
	return sb.String()
}
