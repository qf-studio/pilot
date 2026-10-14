package executor

import (
	"fmt"
	"strings"
)

// navigatorDirName is the scaffold directory Initialize creates. The
// bootstrap session may write only below it.
const navigatorDirName = ".agent"

// navigatorBootstrapPersona keeps the chat persona but lifts its "never edit
// files" clause: the bootstrap is the one session that writes, and only under
// the Navigator directory.
var navigatorBootstrapPersona = strings.TrimSuffix(chatPersonaLine, " You never edit files.") +
	" This is the one-time bootstrap session: you read the repository and write its Navigator knowledge base."

// BuildNavigatorBootstrapPrompt builds the prompt for the one-time Navigator
// bootstrap session. date is the memory creation date (YYYY-MM-DD).
func BuildNavigatorBootstrapPrompt(date string) string {
	dir := navigatorDirName

	var sb strings.Builder
	sb.WriteString(navigatorBootstrapPersona)
	sb.WriteString("\n\n")
	sb.WriteString(chatResearchMethod)
	sb.WriteString("\n\n")

	sb.WriteString("Boundaries (non-negotiable):\n")
	fmt.Fprintf(&sb, "- Write ONLY under %s/. Every Write and Edit path must start with %s/.\n", dir, dir)
	sb.WriteString("- Never touch tracked files: no source, config, docs or tests outside the Navigator directory.\n")
	sb.WriteString("- Never open a PR, never run git commit, git push, git add or gh. Do not create branches.\n")
	sb.WriteString("- Your other tools stay read-only.\n\n")

	sb.WriteString("Your job, in this order:\n")
	sb.WriteString("1. Read the repository: its top-level layout, build files, entry points and the main packages or modules. Sample representative files instead of reading everything.\n")
	fmt.Fprintf(&sb, "2. Edit %s/DEVELOPMENT-README.md and replace the placeholder body of exactly these three sections with real content, keeping each header line unchanged:\n", dir)
	sb.WriteString("   - \"### Key Components\": a table or list of the major components with one-line responsibilities.\n")
	sb.WriteString("   - \"## Key Files\": the files a new session should read first, each with a path and a one-line reason.\n")
	sb.WriteString("   - \"## Project Structure\": the real source tree (two levels deep) in a fenced block.\n")
	sb.WriteString("   No section may keep text such as \"fill in during the first task session\", \"fill in as the project grows\" or \"Fill in the real source tree\". Do not touch other sections.\n")
	fmt.Fprintf(&sb, "3. Write a short architecture note (under 80 lines) to %s/system/architecture.md: layers, data flow, external dependencies, how to build and test.\n", dir)
	fmt.Fprintf(&sb, "4. Write three to ten pattern memories to %s/knowledge/memories/patterns/<slug>.md, each a convention or recurring structure a new session must follow, backed by a path:line citation. Memory file format:\n\n", dir)
	sb.WriteString("---\n")
	sb.WriteString("name: <slug, equal to the file name without .md>\n")
	sb.WriteString("description: <one sentence stating the rule and when it applies>\n")
	sb.WriteString("type: pattern\n")
	sb.WriteString("---\n\n")
	sb.WriteString("<body: the pattern, where it is established (path:line), and a \"How to apply\" line>\n\n")
	fmt.Fprintf(&sb, "5. Write or update %s/knowledge/graph.json. Keep valid JSON and keep every existing node. Shape: {\"version\": 1, \"nodes\": {\"concepts\": {...}, \"memories\": {...}}, \"edges\": []}. Add one concepts node per top-level package or module: \"<id>\": {\"label\": \"...\", \"description\": \"...\", \"related\": [\"<other concept id>\"]}. Add one memories node per memory file from step 4: \"<slug>\": {\"type\": \"pattern\", \"summary\": \"...\", \"file\": \"%s/knowledge/memories/patterns/<slug>.md\", \"concepts\": [\"<concept id>\"], \"confidence\": 0.7, \"created\": \"%s\"}.\n\n", dir, dir, date)

	sb.WriteString("Finish with a reply under 100 words that lists the files you wrote. Never state that you opened a PR or changed a tracked file.")
	return sb.String()
}
