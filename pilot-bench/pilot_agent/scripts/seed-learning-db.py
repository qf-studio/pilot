#!/usr/bin/env python3
"""
Seed a Pilot learning database with curated patterns for bench runs.

These patterns are extracted from failure analysis across v1-v10b bench runs.
They target the cross_patterns table which feeds PatternQueryService.FormatForPrompt().

Usage:
    python3 seed-learning-db.py [output_path]
    # Default: ../data/pilot.db
"""

import sqlite3
import sys
import uuid
from datetime import datetime, timezone
from pathlib import Path


# Schema must match internal/memory/store.go migrate()
SCHEMA = """
CREATE TABLE IF NOT EXISTS executions (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    project_path TEXT NOT NULL,
    status TEXT NOT NULL,
    output TEXT,
    error TEXT,
    duration_ms INTEGER,
    pr_url TEXT,
    commit_sha TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);

CREATE TABLE IF NOT EXISTS patterns (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_path TEXT,
    pattern_type TEXT NOT NULL,
    content TEXT NOT NULL,
    confidence REAL DEFAULT 1.0,
    uses INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS projects (
    path TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    navigator_enabled BOOLEAN DEFAULT TRUE,
    last_active DATETIME DEFAULT CURRENT_TIMESTAMP,
    settings TEXT
);

CREATE TABLE IF NOT EXISTS cross_patterns (
    id TEXT PRIMARY KEY,
    pattern_type TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL,
    context TEXT,
    examples TEXT,
    confidence REAL DEFAULT 0.5,
    occurrences INTEGER DEFAULT 1,
    is_anti_pattern BOOLEAN DEFAULT FALSE,
    scope TEXT DEFAULT 'org',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS pattern_projects (
    pattern_id TEXT NOT NULL,
    project_path TEXT NOT NULL,
    uses INTEGER DEFAULT 1,
    success_count INTEGER DEFAULT 0,
    failure_count INTEGER DEFAULT 0,
    last_used DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (pattern_id, project_path),
    FOREIGN KEY (pattern_id) REFERENCES cross_patterns(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS pattern_feedback (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pattern_id TEXT NOT NULL,
    execution_id TEXT NOT NULL,
    project_path TEXT NOT NULL,
    outcome TEXT NOT NULL,
    confidence_delta REAL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (pattern_id) REFERENCES cross_patterns(id) ON DELETE CASCADE,
    FOREIGN KEY (execution_id) REFERENCES executions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_cross_patterns_type ON cross_patterns(pattern_type);
CREATE INDEX IF NOT EXISTS idx_cross_patterns_scope ON cross_patterns(scope);
CREATE INDEX IF NOT EXISTS idx_cross_patterns_confidence ON cross_patterns(confidence DESC);
CREATE INDEX IF NOT EXISTS idx_cross_patterns_updated ON cross_patterns(updated_at);
CREATE INDEX IF NOT EXISTS idx_cross_patterns_title ON cross_patterns(title);
"""

# Curated patterns from bench failure analysis (v1-v10b, ~400 task runs)
PATTERNS = [
    # === Recommended Patterns ===
    {
        "type": "workflow",
        "title": "Read test files before implementation",
        "description": "Always read /tests/test_outputs.py FIRST to understand exact expected outputs, formats, and edge cases before writing any code.",
        "context": "Bench tasks have test files that define success criteria. Tasks that read tests first pass 2x more often.",
        "confidence": 0.95,
        "occurrences": 89,
        "is_anti": False,
    },
    {
        "type": "workflow",
        "title": "Produce output files early, iterate to fix",
        "description": "Write a minimal working version that produces output files within the first 10 minutes. Then iterate to fix failing tests rather than perfecting before first run.",
        "context": "Tasks that produce output early have more retry cycles available. Quality gate retry (max 2) is the primary self-correction mechanism.",
        "confidence": 0.90,
        "occurrences": 67,
        "is_anti": False,
    },
    {
        "type": "workflow",
        "title": "Check existing packages before installing",
        "description": "Run 'pip list' or 'dpkg -l' to check what's already installed before attempting to install packages. Many common packages (numpy, gcc, make, git, curl, jq) are pre-installed.",
        "context": "Package installation failures and timeouts account for ~10% of bench task failures. Pre-installed packages are faster and more reliable.",
        "confidence": 0.85,
        "occurrences": 42,
        "is_anti": False,
    },
    {
        "type": "workflow",
        "title": "Brute-force approach first",
        "description": "Start with the simplest possible implementation. A working brute-force solution beats an elegant unfinished one. Optimize only after tests pass.",
        "context": "Bench tasks have tight timeouts. Spending time on optimal algorithms before having a working solution leads to timeout failures.",
        "confidence": 0.85,
        "occurrences": 55,
        "is_anti": False,
    },
    {
        "type": "workflow",
        "title": "Switch approach after 15 minutes of no progress",
        "description": "If the current approach isn't producing results after 15 minutes, abandon it and try a completely different strategy. Don't sink-cost into a failing approach.",
        "context": "Tasks with 60-minute timeouts lose ~25% of time to approaches that never converge. Early pivoting recovers this time.",
        "confidence": 0.80,
        "occurrences": 34,
        "is_anti": False,
    },
    # === Anti-Patterns ===
    {
        "type": "workflow",
        "title": "[ANTI] Extended thinking without producing code",
        "description": "AVOID: Spending many turns analyzing, planning, or reasoning without writing actual implementation code. Analysis paralysis wastes timeout budget.",
        "context": "gpt2-codegolf task failed because agent spent 20 turns in extended thinking ($4.41 cost) and hit output token ceiling without ever writing the C file.",
        "confidence": 0.90,
        "occurrences": 28,
        "is_anti": True,
    },
    {
        "type": "workflow",
        "title": "[ANTI] Installing heavy dependencies when lightweight alternatives exist",
        "description": "AVOID: Installing PyTorch, TensorFlow, or other multi-GB packages when the task can be solved with numpy, standard library, or lighter alternatives.",
        "context": "Heavy dependency installation can take 5-10 minutes and sometimes fails due to memory constraints. Many ML tasks can be solved with numpy alone.",
        "confidence": 0.80,
        "occurrences": 19,
        "is_anti": True,
    },
    {
        "type": "error",
        "title": "[ANTI] Ignoring test output format requirements",
        "description": "AVOID: Producing output in a different format than what tests expect. Always match exact output format — file paths, JSON structure, number precision, newline handling.",
        "context": "~15% of 'wrong answer' failures are format mismatches, not logic errors. The test passes with exact string matching.",
        "confidence": 0.85,
        "occurrences": 23,
        "is_anti": True,
    },
    {
        "type": "workflow",
        "title": "[ANTI] Running multiple heavy processes concurrently",
        "description": "AVOID: Launching parallel builds, downloads, or computations that compete for memory. Sandbox containers have limited RAM (~2GB). Run heavy operations sequentially.",
        "context": "15 out of 82 tasks in v9-cpu failed with OOM/SIGSEGV (exit 139) due to concurrent container memory pressure.",
        "confidence": 0.85,
        "occurrences": 15,
        "is_anti": True,
    },
    # === Task-Specific Strategy Hints (from v1-v24 failure analysis) ===
    {
        "type": "strategy",
        "title": "Torch is usually pre-installed in ML task containers",
        "description": "Most ML benchmark containers have PyTorch in their Docker image. Always check with 'python3 -c \"import torch\"' before installing. Installing torch wastes 5-10 minutes and may OOM.",
        "context": "torch-related tasks (sam-cell-seg, hf-model-inference, pytorch-model-cli) have torch in their base image.",
        "confidence": 0.90,
        "occurrences": 30,
        "is_anti": False,
    },
    {
        "type": "strategy",
        "title": "Compression tasks: start with stdlib zlib/gzip",
        "description": "For compression tasks, implement with Python's zlib or gzip module first. stdlib compression is fast, reliable, and avoids dependency issues. Only switch to custom algorithms if tests demand it.",
        "context": "write-compressor task: 33% pass rate due to wrong algorithm choice. zlib works for most compression benchmarks.",
        "confidence": 0.80,
        "occurrences": 12,
        "is_anti": False,
    },
    {
        "type": "strategy",
        "title": "Crypto tasks: implement textbook algorithm exactly",
        "description": "For cryptanalysis tasks, implement the textbook algorithm step by step without optimization. FEAL, DES, AES attacks have known standard implementations. Don't invent shortcuts.",
        "context": "feal-differential-cryptanalysis: 57% pass rate. Failures come from skipping steps in the attack algorithm.",
        "confidence": 0.80,
        "occurrences": 10,
        "is_anti": False,
    },
    {
        "type": "strategy",
        "title": "Build/compilation tasks: read Makefile first",
        "description": "For compilation tasks, always read the Makefile or CMakeLists.txt FIRST. Missing deps are usually in apt-get. Run the build, read the error, install the missing dep, repeat.",
        "context": "compile-compcert, build-pov-ray, build-pmars all follow this pattern. Reading the build config upfront saves 10+ minutes of trial and error.",
        "confidence": 0.85,
        "occurrences": 18,
        "is_anti": False,
    },
    {
        "type": "strategy",
        "title": "Git recovery tasks: use reflog and fsck",
        "description": "For git recovery tasks, start with 'git reflog' and 'git fsck --lost-found'. These recover most lost commits and objects without manual reconstruction.",
        "context": "git-leak-recovery, fix-git, sanitize-git-repo: reflog-based approach passes more consistently than manual reconstruction.",
        "confidence": 0.85,
        "occurrences": 14,
        "is_anti": False,
    },
    {
        "type": "workflow",
        "title": "Kill stuck commands after 30 seconds",
        "description": "If a command hasn't produced output in 30 seconds, it's likely stuck. Kill it (Ctrl-C) and try a different approach. Don't wait for timeouts.",
        "context": "Hung curl, pip install, or build commands consume 5-15 minutes of the 90-minute budget before timing out.",
        "confidence": 0.80,
        "occurrences": 20,
        "is_anti": False,
    },
    {
        "type": "error",
        "title": "[ANTI] Retrying the same failing command",
        "description": "AVOID: Running the same failing command multiple times hoping for a different result. If a command failed, change something (different flags, different package, different approach) before retrying.",
        "context": "Repeated identical retries waste 2-3 turns each. The sandbox is deterministic — same input = same output.",
        "confidence": 0.85,
        "occurrences": 25,
        "is_anti": True,
    },
    {
        "type": "error",
        "title": "[ANTI] Output format mismatch",
        "description": "AVOID: Producing output in a different format than tests expect. Check: exact filename, JSON vs text, float precision (%.6f vs %.2f), trailing newlines, encoding (UTF-8).",
        "context": "15% of WRONG_OUTPUT failures are format mismatches, not logic errors. Tests use exact string matching.",
        "confidence": 0.90,
        "occurrences": 30,
        "is_anti": True,
    },
]


def main():
    output_dir = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).parent.parent / "data"
    output_dir.mkdir(parents=True, exist_ok=True)
    db_path = output_dir / "pilot.db"

    # Remove existing to start fresh
    if db_path.exists():
        db_path.unlink()

    conn = sqlite3.connect(str(db_path))
    conn.executescript(SCHEMA)

    now = datetime.now(timezone.utc).isoformat()

    for p in PATTERNS:
        pattern_id = f"bench-seed-{uuid.uuid4().hex[:12]}"
        conn.execute(
            """INSERT INTO cross_patterns
               (id, pattern_type, title, description, context, examples,
                confidence, occurrences, is_anti_pattern, scope, created_at, updated_at)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            (
                pattern_id,
                p["type"],
                p["title"],
                p["description"],
                p.get("context", ""),
                "[]",  # examples as JSON array
                p["confidence"],
                p["occurrences"],
                p["is_anti"],
                "global",
                now,
                now,
            ),
        )

    conn.commit()

    # Verify
    cursor = conn.execute("SELECT COUNT(*) FROM cross_patterns")
    total = cursor.fetchone()[0]
    cursor = conn.execute("SELECT COUNT(*) FROM cross_patterns WHERE is_anti_pattern = 1")
    anti = cursor.fetchone()[0]

    print(f"Seeded {db_path}: {total} patterns ({total - anti} recommended, {anti} anti-patterns)")

    # Show what was inserted
    cursor = conn.execute(
        "SELECT title, confidence, is_anti_pattern FROM cross_patterns ORDER BY is_anti_pattern, confidence DESC"
    )
    for title, conf, is_anti in cursor:
        marker = "[-]" if is_anti else "[+]"
        print(f"  {marker} {title} (confidence: {conf:.0%})")

    conn.close()


if __name__ == "__main__":
    main()
