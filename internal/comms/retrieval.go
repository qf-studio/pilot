package comms

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RetrievalConfig controls the bounded file retrieval used by Responder.Answer.
type RetrievalConfig struct {
	Enabled  bool
	MaxFiles int // default 8
	MaxBytes int // default 24000
}

// skipDirs contains directory names that should never be walked.
var skipDirs = map[string]bool{
	".git":         true,
	"vendor":       true,
	"node_modules": true,
	".idea":        true,
	".vscode":      true,
	"__pycache__":  true,
	"dist":         true,
	"build":        true,
	"bin":          true,
	"tmp":          true,
}

// textExts lists file extensions treated as readable text (value false = skip).
var textExts = map[string]bool{
	".go":   true,
	".md":   true,
	".yaml": true,
	".yml":  true,
	".json": true,
	".ts":   true,
	".tsx":  true,
	".js":   true,
	".jsx":  true,
	".py":   true,
	".sh":   true,
	".toml": true,
	".mod":  true,
	".txt":  true,
	".html": true,
	".css":  true,
	".sum":  false, // go.sum — large and unreadable for questions
	".pb.go": false,
}

// namedTextFiles are specific filenames (no extension) that are readable.
var namedTextFiles = map[string]bool{
	"Makefile":   true,
	"Dockerfile": true,
	"README":     true,
}

// broadPhrases trigger an immediate tooBroad=true when present in the question.
var broadPhrases = []string{
	"whole repo",
	"entire codebase",
	"all files",
	"everything in",
	"all of the",
	"the whole",
	"whole codebase",
	"entire project",
	"all the files",
	"explain the whole",
	"explain everything",
	"describe everything",
	"describe the whole",
	"overview of everything",
}

// stopWords are filtered out during keyword extraction.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "in": true, "it": true,
	"to": true, "do": true, "of": true, "and": true, "for": true, "on": true,
	"how": true, "what": true, "where": true, "when": true, "why": true,
	"who": true, "are": true, "was": true, "were": true, "be": true,
	"been": true, "has": true, "have": true, "had": true, "not": true,
	"this": true, "that": true, "with": true, "from": true, "can": true,
	"does": true, "did": true, "will": true, "would": true, "could": true,
	"which": true, "about": true, "work": true, "works": true, "use": true,
	"used": true, "get": true, "set": true, "its": true, "all": true,
}

// retrieve does a bounded WalkDir of projectPath, scores files by keyword
// relevance to the question, and assembles a context block from the top matches.
//
// Returns ("", true) when the question is too broad to retrieve meaningfully,
// when retrieval is disabled, or when no relevant files are found.
// The caller should fall back to the executor in those cases.
func retrieve(projectPath, question string, cfg RetrievalConfig) (contextBlock string, tooBroad bool) {
	if !cfg.Enabled || projectPath == "" {
		return "", true
	}

	maxFiles := cfg.MaxFiles
	if maxFiles <= 0 {
		maxFiles = 8
	}
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 24000
	}

	if isBroadQuestion(question) {
		return "", true
	}

	keywords := extractKeywords(question)
	if len(keywords) == 0 {
		return "", true
	}

	type fileScore struct {
		path  string
		score int
	}

	var candidates []fileScore

	_ = filepath.WalkDir(projectPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}

		name := d.Name()

		if d.IsDir() {
			if strings.HasPrefix(name, ".") || skipDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip hidden files.
		if strings.HasPrefix(name, ".") {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(name))
		if ok, exists := textExts[ext]; exists {
			if !ok {
				return nil
			}
		} else {
			// No extension entry — allow only known named files.
			if !namedTextFiles[name] {
				return nil
			}
		}

		relPath, _ := filepath.Rel(projectPath, path)
		score := scoreByPath(relPath, keywords)
		if score > 0 {
			candidates = append(candidates, fileScore{path: path, score: score})
		}
		return nil
	})

	// Too many matches → question is too broad for focused retrieval.
	if len(candidates) > maxFiles*4 {
		return "", true
	}
	if len(candidates) == 0 {
		return "", true
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	if len(candidates) > maxFiles {
		candidates = candidates[:maxFiles]
	}

	var sb strings.Builder
	remaining := maxBytes
	for _, c := range candidates {
		if remaining <= 0 {
			break
		}
		content, err := readFileCapped(c.path, remaining)
		if err != nil || content == "" {
			continue
		}
		relPath, _ := filepath.Rel(projectPath, c.path)
		fmt.Fprintf(&sb, "// File: %s\n%s\n\n", relPath, content)
		remaining -= len(content)
	}

	if sb.Len() == 0 {
		return "", true
	}
	return sb.String(), false
}

// isBroadQuestion returns true for questions asking about the whole codebase.
func isBroadQuestion(q string) bool {
	lower := strings.ToLower(q)
	for _, phrase := range broadPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// extractKeywords returns meaningful, de-duped lowercase words from the question.
func extractKeywords(q string) []string {
	words := strings.Fields(q)
	seen := make(map[string]bool)
	var out []string
	for _, w := range words {
		// Strip common punctuation.
		w = strings.Trim(w, ".,!?;:\"'()/\\[]{}=<>")
		lower := strings.ToLower(w)
		if len(lower) >= 3 && !stopWords[lower] && !seen[lower] {
			seen[lower] = true
			out = append(out, lower)
		}
	}
	return out
}

// scoreByPath scores a file by how many keywords appear in its relative path.
func scoreByPath(relPath string, keywords []string) int {
	lower := strings.ToLower(relPath)
	score := 0
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			score++
		}
	}
	return score
}

// readFileCapped reads up to capBytes from the file at path.
func readFileCapped(path string, capBytes int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, capBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", err
	}
	return string(buf[:n]), nil
}
