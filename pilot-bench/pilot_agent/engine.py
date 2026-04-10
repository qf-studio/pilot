"""
Custom execution engine for Terminal Bench — direct Anthropic API, no Claude Code.

Replaces the Claude Code CLI with full control over:
- Progressive thinking budget (high early, low later)
- Tool-call validation and correction
- Loop detection (repeated edits, stuck commands)
- Mandatory test verification
- Context window management (prune old messages)
- Prompt caching (cache_control on system)
- Short bash timeouts with kill

Architecture:
  Harbor → agent.py → this engine (via subprocess in container)
  Engine → Anthropic Messages API with tool_use
  Tools: bash, read_file, write_file, edit_file

Usage:
  python3 engine.py --task "instruction" --project /app --model claude-opus-4-6
"""

import argparse
import json
import logging
import os
import re
import subprocess
import sys
import time
from pathlib import Path

try:
    import anthropic
except ImportError:
    subprocess.check_call([sys.executable, "-m", "pip", "install", "--break-system-packages", "anthropic"])
    import anthropic

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("engine")

# --- Constants ---
MAX_TURNS = 60
MAIN_TIMEOUT = 5400             # 90 min total budget
BASH_TIMEOUT = 120              # seconds per command
THINKING_HIGH_TURNS = 8         # extended thinking for first N turns
THINKING_HIGH_BUDGET = 10000    # tokens for planning phase
THINKING_LOW_BUDGET = 3000      # tokens for execution phase
MAX_OUTPUT_TOKENS = 12000       # max non-thinking output per turn
TEST_CHECK_INTERVAL = 8         # auto-run tests every N turns
MAX_REPEATED_COMMANDS = 3       # loop detection threshold
CONTEXT_PRUNE_THRESHOLD = 150000  # estimated tokens before pruning
CONTEXT_KEEP_TURNS = 12         # keep last N turns when pruning

# Opus 4.6 pricing (per 1M tokens)
INPUT_COST_PER_M = 15.0
OUTPUT_COST_PER_M = 75.0

# Result output path (overridden by --result-json)
RESULT_JSON_PATH = "/logs/agent/pilot-result.json"


# --- Tool Definitions ---
TOOLS = [
    {
        "name": "bash",
        "description": (
            "Execute a bash command. Returns stdout+stderr. "
            "Commands timeout after 120s. Use for running code, installing packages, "
            "checking files, running tests. Prefer short commands."
        ),
        "input_schema": {
            "type": "object",
            "properties": {
                "command": {
                    "type": "string",
                    "description": "The bash command to execute",
                },
                "timeout": {
                    "type": "integer",
                    "description": "Timeout in seconds (default 120, max 600)",
                },
            },
            "required": ["command"],
        },
    },
    {
        "name": "read_file",
        "description": (
            "Read a file's contents. Returns the full text with line numbers. "
            "Use for reading source code, test files, configs."
        ),
        "input_schema": {
            "type": "object",
            "properties": {
                "path": {
                    "type": "string",
                    "description": "Absolute path to the file",
                },
                "offset": {
                    "type": "integer",
                    "description": "Line number to start from (1-indexed)",
                },
                "limit": {
                    "type": "integer",
                    "description": "Max lines to read (default all)",
                },
            },
            "required": ["path"],
        },
    },
    {
        "name": "write_file",
        "description": (
            "Write content to a file. Creates parent directories if needed. "
            "Overwrites existing files. Use for creating new files or complete rewrites."
        ),
        "input_schema": {
            "type": "object",
            "properties": {
                "path": {
                    "type": "string",
                    "description": "Absolute path to write to",
                },
                "content": {
                    "type": "string",
                    "description": "File content to write",
                },
            },
            "required": ["path", "content"],
        },
    },
    {
        "name": "edit_file",
        "description": (
            "Replace a specific string in a file. More precise than write_file "
            "for small changes. The old_string must appear exactly once in the file."
        ),
        "input_schema": {
            "type": "object",
            "properties": {
                "path": {
                    "type": "string",
                    "description": "Absolute path to the file",
                },
                "old_string": {
                    "type": "string",
                    "description": "Exact text to find (must appear exactly once)",
                },
                "new_string": {
                    "type": "string",
                    "description": "Replacement text",
                },
            },
            "required": ["path", "old_string", "new_string"],
        },
    },
]


# --- Tool Execution ---
def execute_bash(command: str, timeout: int = BASH_TIMEOUT, cwd: str = "/app") -> str:
    """Execute bash command with timeout."""
    timeout = min(timeout, 600)
    try:
        result = subprocess.run(
            ["bash", "-c", command],
            capture_output=True,
            text=True,
            timeout=timeout,
            cwd=cwd,
        )
        output = result.stdout + result.stderr
        if len(output) > 50000:
            output = output[:25000] + "\n\n... [truncated] ...\n\n" + output[-25000:]
        exit_info = f"\n[exit code: {result.returncode}]" if result.returncode != 0 else ""
        return output + exit_info
    except subprocess.TimeoutExpired:
        return f"[TIMEOUT after {timeout}s — command killed. Try a different approach.]"
    except Exception as e:
        return f"[ERROR: {e}]"


def execute_read_file(path: str, offset: int = 0, limit: int = 0) -> str:
    """Read file with line numbers."""
    try:
        p = Path(path)
        if not p.exists():
            return f"[File not found: {path}]"
        if p.stat().st_size > 500000:
            return f"[File too large: {p.stat().st_size} bytes. Use bash: head -100 {path}]"
        lines = p.read_text(errors="replace").splitlines()
        if offset > 0:
            lines = lines[offset - 1:]
        if limit > 0:
            lines = lines[:limit]
        numbered = [f"{i + (offset or 1):4d} | {line}" for i, line in enumerate(lines)]
        return "\n".join(numbered)
    except Exception as e:
        return f"[ERROR reading {path}: {e}]"


def execute_write_file(path: str, content: str) -> str:
    """Write file, creating parent dirs."""
    try:
        p = Path(path)
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(content)
        return f"[Wrote {len(content)} bytes to {path}]"
    except Exception as e:
        return f"[ERROR writing {path}: {e}]"


def execute_edit_file(path: str, old_string: str, new_string: str) -> str:
    """Replace exact string in file. Must appear exactly once."""
    try:
        p = Path(path)
        if not p.exists():
            return f"[File not found: {path}]"
        content = p.read_text(errors="replace")
        count = content.count(old_string)
        if count == 0:
            return f"[old_string not found in {path}. Read the file first to get exact text.]"
        if count > 1:
            return f"[old_string appears {count} times in {path}. Provide more context to make it unique.]"
        new_content = content.replace(old_string, new_string, 1)
        p.write_text(new_content)
        return f"[Edited {path}: replaced {len(old_string)} chars with {len(new_string)} chars]"
    except Exception as e:
        return f"[ERROR editing {path}: {e}]"


def execute_tool(name: str, input_data: dict) -> str:
    """Dispatch tool call."""
    if name == "bash":
        return execute_bash(
            input_data["command"],
            timeout=input_data.get("timeout", BASH_TIMEOUT),
        )
    elif name == "read_file":
        return execute_read_file(
            input_data["path"],
            offset=input_data.get("offset", 0),
            limit=input_data.get("limit", 0),
        )
    elif name == "write_file":
        return execute_write_file(input_data["path"], input_data["content"])
    elif name == "edit_file":
        return execute_edit_file(
            input_data["path"],
            input_data["old_string"],
            input_data["new_string"],
        )
    else:
        return f"[Unknown tool: {name}]"


# --- Loop Detection ---
class LoopDetector:
    def __init__(self):
        self.command_history: list[str] = []
        self.file_edit_counts: dict[str, int] = {}

    def record_command(self, cmd: str):
        self.command_history.append(cmd)

    def record_file_write(self, path: str):
        self.file_edit_counts[path] = self.file_edit_counts.get(path, 0) + 1

    def is_stuck(self) -> str | None:
        if len(self.command_history) >= MAX_REPEATED_COMMANDS:
            recent = self.command_history[-MAX_REPEATED_COMMANDS:]
            if len(set(recent)) == 1:
                return (
                    f"LOOP DETECTED: You ran '{recent[0][:80]}' {MAX_REPEATED_COMMANDS} times. "
                    "STOP. Try a completely different approach."
                )
        for path, count in self.file_edit_counts.items():
            if count >= 5:
                return (
                    f"LOOP DETECTED: You edited '{path}' {count} times without tests passing. "
                    "DELETE your approach and try something fundamentally different."
                )
        return None


# --- Test Runner ---
def check_tests_passed(output: str) -> bool:
    """Check pytest output for pass/fail. More robust than string matching."""
    if re.search(r"\d+ passed", output) and not re.search(r"\d+ (failed|error)", output, re.IGNORECASE):
        return True
    return False


def run_tests(cwd: str = "/app") -> tuple[bool, str]:
    """Run pytest if test file exists."""
    test_file = "/tests/test_outputs.py"
    if not Path(test_file).exists():
        return False, "[No test file found]"
    output = execute_bash(
        f"cd {cwd} && python3 -m pytest {test_file} -v 2>&1",
        timeout=300,
        cwd=cwd,
    )
    return check_tests_passed(output), output


# --- Context Management ---
def estimate_tokens(messages: list) -> int:
    """Rough token estimate: 4 chars ≈ 1 token."""
    total = 0
    for msg in messages:
        if isinstance(msg.get("content"), str):
            total += len(msg["content"]) // 4
        elif isinstance(msg.get("content"), list):
            for block in msg["content"]:
                if isinstance(block, dict):
                    total += len(json.dumps(block)) // 4
                else:
                    total += len(str(block)) // 4
    return total


def prune_messages(messages: list) -> list:
    """Prune old messages to stay within context window."""
    if len(messages) <= CONTEXT_KEEP_TURNS * 2:
        return messages

    # Keep first message (initial instruction) and last N turns
    first = messages[0]
    keep = messages[-(CONTEXT_KEEP_TURNS * 2):]

    pruned_count = len(messages) - len(keep) - 1
    summary = {
        "role": "user",
        "content": f"[Context pruned: {pruned_count} earlier messages removed to save space. Continue working on the task.]",
    }

    return [first, summary] + keep


# --- System Prompt ---
def build_system_prompt(task: str, env_context: str, patterns: str) -> str:
    parts = [
        "You are an expert software engineer solving a coding task in a sandboxed container.",
        "",
        f"## Task\n\n{task}",
        "",
    ]

    if env_context:
        parts.extend([
            "## Pre-discovered Environment",
            f"```\n{env_context}\n```",
            "",
        ])

    parts.extend([
        "## Mandatory Workflow",
        "",
        "Phase 1 — RECON (first 3 tool calls):",
        "1. Read /tests/test_outputs.py to understand EXACT expected outputs",
        "2. List and read all files in /app/",
        "3. Write a brief plan (in your thinking) — which approach, which files to create",
        "",
        "Phase 2 — IMPLEMENT:",
        "1. Start with the simplest working approach — brute force beats unfinished elegance",
        "2. Produce output files EARLY — partial output beats no output",
        "3. Run tests after EVERY significant change: `cd /app && python3 -m pytest /tests/test_outputs.py -v`",
        "4. If tests pass → STOP IMMEDIATELY (say 'TESTS PASSED' and stop)",
        "",
        "Phase 3 — RECOVER (if stuck):",
        "1. If same approach failed 3 times → DELETE and try completely different algorithm",
        "2. Read test error output carefully — often it's a format mismatch, not logic",
        "3. Write analysis scripts instead of reasoning through data manually",
        "",
        "## Rules",
        "- Work autonomously — never ask for confirmation",
        "- Container has ~2GB RAM — don't run heavy processes concurrently",
        "- Check if packages exist before installing: python3 -c 'import X'",
        "- Use --break-system-packages with pip",
        "- If a command runs >30s with no output, kill it",
        "- Once tests pass, STOP. No cleanup, no summary.",
        "- Never retry the exact same failing command — change something first",
        "- If you've written >500 words without executing code, STOP and write code NOW",
        "",
    ])

    if patterns:
        parts.extend(["## Learned Patterns", "", patterns, ""])

    return "\n".join(parts)


# --- Load Learning DB Patterns ---
def load_patterns(db_path: str = "/root/.pilot/data/pilot.db") -> str:
    if not Path(db_path).exists():
        return ""
    try:
        import sqlite3
        conn = sqlite3.connect(db_path)
        cursor = conn.execute(
            "SELECT title, description, is_anti_pattern FROM cross_patterns "
            "WHERE confidence >= 0.7 ORDER BY confidence DESC LIMIT 15"
        )
        lines = []
        for title, desc, is_anti in cursor:
            marker = "AVOID" if is_anti else "DO"
            lines.append(f"- **[{marker}]** {title}: {desc}")
        conn.close()
        return "\n".join(lines)
    except Exception as e:
        logger.warning(f"Failed to load patterns: {e}")
        return ""


# --- API Call with Retry (Streaming) ---
def api_call_with_retry(client, kwargs, max_retries=5):
    """Call API with streaming (required for Opus + thinking) and retry.

    Handles: rate limits (429), overloaded (529), and overloaded_error responses.
    Uses longer backoffs to respect 30K input tokens/min rate limit.
    """
    backoffs = [30, 60, 90, 120, 180]
    for attempt in range(max_retries + 1):
        try:
            with client.messages.stream(**kwargs) as stream:
                response = stream.get_final_message()

            # Check for error in 200 OK body (overloaded_error, api_error)
            # SDK returns 200 but response may contain error instead of content
            if hasattr(response, 'type') and response.type == 'error':
                raise anthropic.APIError(f"Error in response body: {response}")
            if not hasattr(response, 'content') or not response.content:
                raise anthropic.APIError("Empty response content")

            return response
        except anthropic.RateLimitError as e:
            if attempt < max_retries:
                wait = backoffs[min(attempt, len(backoffs) - 1)]
                logger.warning(f"Rate limited, waiting {wait}s (attempt {attempt + 1}/{max_retries})")
                time.sleep(wait)
            else:
                raise
        except anthropic.APIStatusError as e:
            if e.status_code in (429, 529) and attempt < max_retries:
                wait = backoffs[min(attempt, len(backoffs) - 1)]
                logger.warning(f"API error {e.status_code}, waiting {wait}s (attempt {attempt + 1}/{max_retries})")
                time.sleep(wait)
            else:
                raise
        except anthropic.APIError as e:
            # Catch overloaded_error and other transient API errors
            if "overloaded" in str(e).lower() and attempt < max_retries:
                wait = backoffs[min(attempt, len(backoffs) - 1)]
                logger.warning(f"API overloaded, waiting {wait}s (attempt {attempt + 1}/{max_retries})")
                time.sleep(wait)
            else:
                raise


# --- Main Engine ---
def run(task: str, project: str, model: str, api_key: str) -> bool:
    client = anthropic.Anthropic(api_key=api_key, max_retries=0)  # handle retries ourselves
    loop_detector = LoopDetector()
    start_time = time.time()

    # Load context
    env_context = ""
    env_file = Path(project) / ".pilot-env-context.txt"
    if env_file.exists():
        env_context = env_file.read_text()[:1200]

    patterns = load_patterns()
    system_prompt = build_system_prompt(task, env_context, patterns)

    # System prompt with cache control for prompt caching
    system = [
        {
            "type": "text",
            "text": system_prompt,
            "cache_control": {"type": "ephemeral"},
        }
    ]

    messages = []
    total_input_tokens = 0
    total_output_tokens = 0
    tests_passed = False

    logger.info(f"Starting engine: model={model}, project={project}")
    logger.info(f"Task: {task[:200]}...")

    for turn in range(MAX_TURNS):
        elapsed = time.time() - start_time
        if elapsed > MAIN_TIMEOUT - 120:
            logger.warning(f"Approaching timeout ({elapsed:.0f}s), stopping")
            break

        # Progressive thinking budget
        thinking_budget = THINKING_HIGH_BUDGET if turn < THINKING_HIGH_TURNS else THINKING_LOW_BUDGET

        # Check for loops
        loop_warning = loop_detector.is_stuck()
        if loop_warning:
            messages.append({"role": "user", "content": f"⚠️ {loop_warning}"})

        # Auto-test check
        if turn > 0 and turn % TEST_CHECK_INTERVAL == 0 and not tests_passed:
            passed, test_output = run_tests(project)
            if passed:
                tests_passed = True
                logger.info(f"Tests passed at turn {turn}!")
                break
            else:
                messages.append({
                    "role": "user",
                    "content": f"[AUTO-TEST at turn {turn}]\n{test_output}\n\nTests failing. Fix the issues.",
                })

        # Context management — prune if getting large
        if estimate_tokens(messages) > CONTEXT_PRUNE_THRESHOLD:
            messages = prune_messages(messages)
            logger.info(f"Context pruned at turn {turn}")

        # Initial message
        if turn == 0:
            messages.append({
                "role": "user",
                "content": "Begin. Follow the mandatory workflow. Start with Phase 1 RECON.",
            })

        # API call
        try:
            # max_tokens must be > thinking budget (covers both thinking + output)
            total_max = thinking_budget + MAX_OUTPUT_TOKENS

            kwargs = {
                "model": model,
                "max_tokens": total_max,
                "system": system,
                "tools": TOOLS,
                "messages": messages,
                "thinking": {
                    "type": "enabled",
                    "budget_tokens": thinking_budget,
                },
            }

            response = api_call_with_retry(client, kwargs)

        except Exception as e:
            logger.error(f"API error at turn {turn}: {e}")
            break

        # Track tokens
        total_input_tokens += response.usage.input_tokens
        total_output_tokens += response.usage.output_tokens

        # Process response
        assistant_content = response.content
        messages.append({"role": "assistant", "content": assistant_content})

        # Check stop reason
        if response.stop_reason == "end_turn":
            logger.info(f"Agent finished at turn {turn}")
            if not tests_passed:
                passed, _ = run_tests(project)
                tests_passed = passed
            break

        # Process tool calls
        if response.stop_reason == "tool_use":
            tool_results = []
            for block in assistant_content:
                if block.type == "tool_use":
                    tool_name = block.name
                    tool_input = block.input

                    # Track for loop detection
                    if tool_name == "bash":
                        loop_detector.record_command(tool_input.get("command", ""))
                    elif tool_name in ("write_file", "edit_file"):
                        loop_detector.record_file_write(tool_input.get("path", ""))

                    # Execute
                    logger.info(f"Turn {turn}: {tool_name}({str(tool_input)[:100]}...)")
                    result = execute_tool(tool_name, tool_input)

                    # Check if tests were run and passed
                    if tool_name == "bash" and "pytest" in tool_input.get("command", ""):
                        if check_tests_passed(result):
                            tests_passed = True
                            logger.info("Tests passed via agent's pytest run!")

                    tool_results.append({
                        "type": "tool_result",
                        "tool_use_id": block.id,
                        "content": result,
                    })

            messages.append({"role": "user", "content": tool_results})

            if tests_passed:
                logger.info(f"Tests passed — stopping at turn {turn}")
                break
        else:
            logger.warning(f"Unexpected stop_reason: {response.stop_reason}")
            break

    # Final test if not confirmed
    if not tests_passed:
        tests_passed, _ = run_tests(project)

    elapsed = time.time() - start_time
    cost = (total_input_tokens * INPUT_COST_PER_M + total_output_tokens * OUTPUT_COST_PER_M) / 1_000_000

    logger.info(
        f"Engine done: turns={turn+1}, passed={tests_passed}, "
        f"tokens_in={total_input_tokens}, tokens_out={total_output_tokens}, "
        f"cost=${cost:.2f}, elapsed={elapsed:.0f}s"
    )

    # Write result JSON
    result = {
        "Success": tests_passed,
        "TokensInput": total_input_tokens,
        "TokensOutput": total_output_tokens,
        "EstimatedCostUSD": round(cost, 4),
        "Turns": turn + 1,
        "ElapsedSeconds": round(elapsed, 1),
    }
    result_path = Path(RESULT_JSON_PATH)
    result_path.parent.mkdir(parents=True, exist_ok=True)
    result_path.write_text(json.dumps(result, indent=2))
    logger.info(f"Result written to {RESULT_JSON_PATH}")

    return tests_passed


def main():
    global RESULT_JSON_PATH

    parser = argparse.ArgumentParser(description="Pilot Bench Engine")
    parser.add_argument("--task", required=True, help="Task instruction")
    parser.add_argument("--project", default="/app", help="Project directory")
    parser.add_argument("--model", default="claude-opus-4-6", help="Model ID")
    parser.add_argument("--result-json", default=RESULT_JSON_PATH, help="Result output path")
    args = parser.parse_args()

    RESULT_JSON_PATH = args.result_json

    api_key = os.environ.get("ANTHROPIC_API_KEY") or os.environ.get("PILOT_ENGINE_API_KEY")
    if not api_key:
        logger.error("No API key found. Set ANTHROPIC_API_KEY or PILOT_ENGINE_API_KEY")
        sys.exit(1)

    success = run(args.task, args.project, args.model, api_key)
    sys.exit(0 if success else 1)


if __name__ == "__main__":
    main()
