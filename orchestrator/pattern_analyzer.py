"""
Pattern Analyzer

Uses LLM to extract meaningful patterns from execution outputs.
Designed to identify code patterns, anti-patterns, and workflow patterns.
"""

import json
import sys
from dataclasses import dataclass, asdict
from typing import List, Optional, Dict, Any


@dataclass
class Pattern:
    """Represents an extracted pattern."""
    type: str  # code, structure, naming, workflow, error
    title: str
    description: str
    context: str  # e.g., "Go handlers", "React components"
    examples: List[str]
    confidence: float


@dataclass
class AnalysisResult:
    """Result of pattern analysis."""
    patterns: List[Pattern]
    anti_patterns: List[Pattern]


class PatternAnalyzer:
    """Analyzes execution outputs for patterns."""

    # Common pattern signatures to look for
    CODE_PATTERNS = [
        {
            "signature": "context.Context",
            "type": "code",
            "title": "Use context.Context for cancellation",
            "description": "Pass context.Context to functions for proper cancellation and timeout handling",
            "context": "Go handlers",
        },
        {
            "signature": "slog.",
            "type": "code",
            "title": "Use structured logging",
            "description": "Use slog or similar structured logging instead of fmt.Printf",
            "context": "Go services",
        },
        {
            "signature": "defer ",
            "type": "code",
            "title": "Use defer for cleanup",
            "description": "Use defer for resource cleanup (close files, unlock mutex)",
            "context": "Go functions",
        },
        {
            "signature": "interface{}",
            "type": "structure",
            "title": "Define interfaces",
            "description": "Define interfaces for dependency injection and testing",
            "context": "Go packages",
        },
        {
            "signature": "_test.go",
            "type": "workflow",
            "title": "Co-locate tests",
            "description": "Keep test files next to implementation files",
            "context": "Go testing",
        },
    ]

    ERROR_PATTERNS = [
        {
            "signature": "nil pointer",
            "type": "error",
            "title": "Check for nil before dereference",
            "description": "Always validate pointers are not nil before dereferencing",
            "context": "Go code",
        },
        {
            "signature": "sql: no rows",
            "type": "error",
            "title": "Handle sql.ErrNoRows",
            "description": "Check for sql.ErrNoRows when querying database",
            "context": "Go database",
        },
        {
            "signature": "context deadline",
            "type": "error",
            "title": "Handle context timeouts",
            "description": "Gracefully handle context deadline exceeded errors",
            "context": "Async operations",
        },
        {
            "signature": "import cycle",
            "type": "structure",
            "title": "Avoid import cycles",
            "description": "Restructure packages to prevent circular imports",
            "context": "Go packages",
        },
    ]

    def __init__(self):
        """Initialize the pattern analyzer."""
        pass

    def analyze(self, request: Dict[str, Any]) -> AnalysisResult:
        """
        Analyze execution output for patterns.

        Args:
            request: Dictionary with execution_id, project_path, output, error, diff_content

        Returns:
            AnalysisResult with patterns and anti-patterns
        """
        output = request.get("output", "")
        error = request.get("error", "")
        diff_content = request.get("diff_content", "")

        patterns = []
        anti_patterns = []

        # Analyze output for code patterns
        combined_text = f"{output}\n{diff_content}"
        for pattern_def in self.CODE_PATTERNS:
            if pattern_def["signature"].lower() in combined_text.lower():
                patterns.append(Pattern(
                    type=pattern_def["type"],
                    title=pattern_def["title"],
                    description=pattern_def["description"],
                    context=pattern_def["context"],
                    examples=self._extract_examples(combined_text, pattern_def["signature"]),
                    confidence=0.7,
                ))

        # Analyze errors for anti-patterns
        if error:
            for pattern_def in self.ERROR_PATTERNS:
                if pattern_def["signature"].lower() in error.lower():
                    anti_patterns.append(Pattern(
                        type=pattern_def["type"],
                        title=pattern_def["title"],
                        description=pattern_def["description"],
                        context=pattern_def["context"],
                        examples=[error[:200]],
                        confidence=0.8,
                    ))

        # Deduplicate patterns
        patterns = self._deduplicate_patterns(patterns)
        anti_patterns = self._deduplicate_patterns(anti_patterns)

        return AnalysisResult(patterns=patterns, anti_patterns=anti_patterns)

    def _extract_examples(self, text: str, signature: str) -> List[str]:
        """Extract example snippets containing the signature."""
        examples = []
        lines = text.split("\n")

        for i, line in enumerate(lines):
            if signature.lower() in line.lower():
                # Get surrounding context
                start = max(0, i - 1)
                end = min(len(lines), i + 2)
                snippet = "\n".join(lines[start:end])
                if len(snippet) <= 200:
                    examples.append(snippet)

                if len(examples) >= 3:
                    break

        return examples

    def _deduplicate_patterns(self, patterns: List[Pattern]) -> List[Pattern]:
        """Remove duplicate patterns based on title."""
        seen = set()
        unique = []

        for p in patterns:
            if p.title not in seen:
                seen.add(p.title)
                unique.append(p)

        return unique


def analyze_patterns(request_json: str) -> str:
    """
    Entry point for Go orchestrator.

    Args:
        request_json: JSON string with analysis request

    Returns:
        JSON string with analysis result
    """
    request = json.loads(request_json)
    analyzer = PatternAnalyzer()
    result = analyzer.analyze(request)

    return json.dumps({
        "patterns": [asdict(p) for p in result.patterns],
        "anti_patterns": [asdict(p) for p in result.anti_patterns],
    })


if __name__ == "__main__":
    # Read from stdin if called directly
    if len(sys.argv) > 1:
        request_json = sys.argv[1]
    else:
        request_json = sys.stdin.read()

    result = analyze_patterns(request_json)
    print(result)
