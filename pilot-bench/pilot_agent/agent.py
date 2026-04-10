"""
PilotAgent — Terminal-Bench agent running the real Pilot Go binary.

Architecture:
  Harbor Orchestrator → PilotAgent (this shim) → pilot binary (Go) → Claude Code

The Python agent is a thin installer/runner:
1. Install Claude Code + pilot binary in container
2. Run `pilot task "instruction" --local` (full Pilot pipeline, no git workflow)
3. Parse result JSON for token metrics
"""

import json
import logging
import os
from pathlib import Path

from harbor.agents.installed.base import BaseInstalledAgent, ExecInput
from harbor.models.agent.context import AgentContext

logger = logging.getLogger(__name__)

# Paths
AGENT_LOG_DIR = "/logs/agent"
RESULT_JSON = f"{AGENT_LOG_DIR}/pilot-result.json"
MAIN_TIMEOUT = 5400  # 90 min — heavy deps (PyTorch) need more time


class PilotAgent(BaseInstalledAgent):
    """
    Real Pilot binary bench agent.

    Runs the actual Go executor pipeline: prompt building, model routing,
    hooks, Claude Code invocation — everything production Pilot does,
    minus git workflow (--local mode).
    """

    @staticmethod
    def name() -> str:
        return "pilot-real"

    @property
    def _install_agent_template_path(self) -> Path:
        return Path(__file__).parent / "templates" / "install-pilot-agent.sh.j2"

    def create_run_agent_commands(self, instruction: str) -> list[ExecInput]:
        """Run Pilot Go binary with Claude Code backend (OAuth auth)."""
        env = self._build_env()

        # Escape instruction for shell (single quotes with escaping)
        safe_instruction = instruction.replace("'", "'\\''")
        # Prohibit reading evaluation test files
        safe_instruction += "\n\nIMPORTANT: Do NOT read, cat, or access any files under /tests/. These are evaluation files used after your work is complete. Solve the task based solely on this instruction."
        safe_instruction += """

VERIFICATION PROTOCOL (mandatory before finishing):
1. Re-read the original instruction above
2. List every testable requirement from the instruction
3. Write a validation script (validate.sh or validate.py) that checks each requirement
4. Run it and examine output carefully
5. If ANY check fails, fix your implementation and re-run until all pass
6. Only declare success after your own validation passes
"""

        return [
            ExecInput(
                command=(
                    f"pilot task '{safe_instruction}' "
                    f"--local "
                    f"--project /app "
                    f"--verbose "
                    f"--result-json {RESULT_JSON}"
                ),
                cwd="/app",
                env={
                    **env,
                    "IS_SANDBOX": "1",
                    # CC uses OAuth token internally for API calls
                    # 54K output tokens — default 32K kills complex tasks mid-thinking
                    "CLAUDE_CODE_MAX_OUTPUT_TOKENS": "64000",
                },
                timeout_sec=MAIN_TIMEOUT,
            ),
            # Copy learned patterns DB to logs dir (auto-synced to host by Harbor)
            ExecInput(
                command="cp /root/.pilot/data/pilot.db /logs/agent/pilot-patterns.db 2>/dev/null || true",
                cwd="/app",
                timeout_sec=10,
            ),
        ]

    def populate_context_post_run(self, context: AgentContext) -> None:
        """Read Pilot's result JSON for token metrics."""
        # Try pilot result JSON first (structured, reliable)
        result_file = self.logs_dir / "pilot-result.json"
        if not result_file.exists():
            # Try alternate paths in command output dirs
            for cmd_dir in sorted(self.logs_dir.glob("command-*")):
                # Harbor copies container files to command dirs
                candidate = cmd_dir / "pilot-result.json"
                if candidate.exists():
                    result_file = candidate
                    break

        if result_file.exists():
            try:
                result = json.loads(result_file.read_text())
                if not isinstance(result, dict):
                    logger.warning(f"Pilot result JSON is not a dict: {type(result)}")
                else:
                    context.n_input_tokens = result.get("TokensInput", 0)
                    context.n_output_tokens = result.get("TokensOutput", 0)
                    context.cost_usd = result.get("EstimatedCostUSD", 0.0)
                    return
            except (json.JSONDecodeError, ValueError) as e:
                logger.warning(f"Failed to parse pilot result JSON: {e}")

        # Fallback: parse Claude Code stream-json from stdout
        self._parse_claude_output(context)

        # Merge learned patterns from container back into seed DB
        self._collect_learned_patterns()

    def _parse_claude_output(self, context: AgentContext) -> None:
        """Fallback: parse Claude Code JSONL output for token metrics."""
        for cmd_dir in sorted(self.logs_dir.glob("command-*")):
            stdout = cmd_dir / "stdout.txt"
            if not stdout.exists():
                continue

            total_input = 0
            total_output = 0
            total_cost = 0.0

            with open(stdout) as f:
                for line in f:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        event = json.loads(line)
                        if not isinstance(event, dict):
                            continue
                        usage = event.get("usage", {})
                        if usage:
                            total_input += usage.get("input_tokens", 0)
                            total_output += usage.get("output_tokens", 0)
                        if event.get("type") == "result":
                            cost = event.get("cost_usd", 0)
                            if cost:
                                total_cost = max(total_cost, float(cost))
                    except (json.JSONDecodeError, ValueError):
                        continue

            if total_input > 0:
                context.n_input_tokens = total_input
                context.n_output_tokens = total_output
                if total_cost > 0:
                    context.cost_usd = total_cost

    def _collect_learned_patterns(self) -> None:
        """Merge learned patterns from container DB back into seed DB."""
        # Find container's pattern DB in logs dir (copied by post-run command)
        container_db = self.logs_dir / "pilot-patterns.db"
        if not container_db.exists():
            for cmd_dir in sorted(self.logs_dir.glob("command-*")):
                candidate = cmd_dir / "pilot-patterns.db"
                if candidate.exists():
                    container_db = candidate
                    break

        if not container_db.exists():
            return

        seed_path = Path(__file__).parent / "data" / "pilot.db"
        if not seed_path.exists():
            return

        try:
            self._merge_patterns(container_db, seed_path)
        except Exception as e:
            logger.warning(f"Failed to merge patterns: {e}")

    def _merge_patterns(self, source_db: Path, target_db: Path) -> None:
        """Thread-safe merge of new patterns from source into target DB.

        - Dedup by title (don't insert duplicates)
        - New patterns inserted with confidence 0.6
        - Existing patterns get confidence boost (+0.05, cap 0.95)
        """
        import fcntl
        import sqlite3

        lock_path = target_db.with_suffix(".merge-lock")
        with open(lock_path, "w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            try:
                src = sqlite3.connect(str(source_db))
                dst = sqlite3.connect(str(target_db))

                # Get existing titles in target
                existing = {}
                for row in dst.execute("SELECT id, title, confidence FROM cross_patterns"):
                    existing[row[1]] = (row[0], row[2])

                # Get all patterns from source
                src_patterns = src.execute(
                    "SELECT title, pattern_type, description, context, confidence, "
                    "occurrences, is_anti_pattern, scope FROM cross_patterns"
                ).fetchall()

                merged = 0
                boosted = 0
                for title, ptype, desc, ctx, conf, occ, is_anti, scope in src_patterns:
                    if title in existing:
                        # Boost confidence on existing pattern
                        old_id, old_conf = existing[title]
                        new_conf = min(0.95, old_conf + 0.05)
                        if new_conf > old_conf:
                            dst.execute(
                                "UPDATE cross_patterns SET confidence = ?, occurrences = occurrences + 1 WHERE id = ?",
                                (new_conf, old_id),
                            )
                            boosted += 1
                    else:
                        # Insert new pattern with conservative confidence
                        import uuid
                        pattern_id = f"learned-{uuid.uuid4().hex[:12]}"
                        dst.execute(
                            "INSERT INTO cross_patterns "
                            "(id, pattern_type, title, description, context, examples, "
                            "confidence, occurrences, is_anti_pattern, scope) "
                            "VALUES (?, ?, ?, ?, ?, '[]', ?, ?, ?, ?)",
                            (pattern_id, ptype, title, desc, ctx or "",
                             min(conf, 0.6), occ, is_anti, scope or "global"),
                        )
                        merged += 1

                dst.commit()
                src.close()
                dst.close()

                if merged > 0 or boosted > 0:
                    logger.info(f"Pattern merge: {merged} new, {boosted} boosted")

            finally:
                fcntl.flock(lock, fcntl.LOCK_UN)

    def _build_env(self) -> dict[str, str]:
        """Collect auth environment variables."""
        env: dict[str, str] = {}
        for key in [
            "ANTHROPIC_API_KEY",
            "PILOT_ENGINE_API_KEY",
            "CLAUDE_CODE_OAUTH_TOKEN",
        ]:
            if key in os.environ:
                env[key] = os.environ[key]
        return {k: v for k, v in env.items() if v}

    def _resolve_model(self) -> str:
        """Resolve model name (strip provider prefix)."""
        if self.model_name:
            if "/" in self.model_name:
                return self.model_name.split("/", 1)[1]
            return self.model_name
        return "claude-opus-4-6"

    def _build_config(self, model: str) -> str:
        """Build production-grade config.yaml for bench runs.

        Mirrors the real production config with executor features that
        improve task success: hooks, quality gates, effort routing,
        structured output, and timeouts.
        """
        return f"""version: "1.0"
orchestrator:
  model: "{model}"
executor:
  type: "claude-code"
  claude_code:
    command: claude
    use_structured_output: true
    use_session_resume: true
    use_from_pr: false
  hooks:
    enabled: true
    run_tests_on_stop: false
    block_destructive: true
    lint_on_save: false
  heartbeat_timeout: 15m
  model_routing:
    enabled: true
    trivial: "{model}"
    simple: "{model}"
    medium: "{model}"
    complex: "{model}"
  timeout:
    default: 30m
    trivial: 15m
    simple: 25m
    medium: 30m
    complex: 60m
  effort_routing:
    enabled: true
    trivial: high
    simple: high
    medium: high
    complex: high
  effort_classifier:
    enabled: true
    model: claude-haiku-4-5-20251001
    timeout: 30s
  intent_judge:
    enabled: false
  retry:
    enabled: true
    rate_limit:
      max_attempts: 3
      initial_backoff: 30s
      backoff_multiplier: 2
    api_error:
      max_attempts: 3
      initial_backoff: 5s
      backoff_multiplier: 2
    timeout:
      max_attempts: 2
      initial_backoff: 0s
      backoff_multiplier: 0
      extend_timeout: true
      timeout_multiplier: 1.5
quality:
  enabled: false
memory:
  path: /root/.pilot/data
  learning:
    enabled: true
"""

    async def setup(self, environment) -> None:
        """Install pilot binary, write config, upload test files."""
        # Base setup: renders install-pilot-agent.sh.j2, uploads, executes
        await super().setup(environment)

        logger.info("Starting agent setup (binary + config + tests)...")

        # Upload pilot binary — prefer compressed (15MB vs 28MB, ~2x faster upload).
        gz_path = Path(__file__).parent.parent / "bin" / "pilot-linux-amd64.gz"
        raw_path = Path(__file__).parent.parent / "bin" / "pilot-linux-amd64"

        upload_path = gz_path if gz_path.exists() else raw_path
        if not upload_path.exists():
            raise FileNotFoundError("Pilot binary not found. Run 'make bench-binary' first.")

        is_compressed = upload_path.suffix == ".gz"
        target = "/tmp/pilot.gz" if is_compressed else "/usr/local/bin/pilot"
        logger.info(f"Uploading pilot binary ({upload_path.stat().st_size // 1024 // 1024}MB, {'compressed' if is_compressed else 'raw'})...")

        try:
            import asyncio
            await asyncio.wait_for(
                environment.upload_file(source_path=upload_path, target_path=target),
                timeout=180,
            )
            if is_compressed:
                await environment.exec(command="gunzip -f /tmp/pilot.gz && mv /tmp/pilot /usr/local/bin/pilot && chmod +x /usr/local/bin/pilot")
            else:
                await environment.exec(command="chmod +x /usr/local/bin/pilot")
            logger.info("Binary uploaded")
        except (asyncio.TimeoutError, Exception) as e:
            logger.warning(f"upload_file failed ({e}), falling back to base64 pipe")
            # Fallback: write binary via base64-encoded pipe to file
            import base64
            data = raw_path.read_bytes()
            b64 = base64.b64encode(data).decode()
            # Write base64 to a temp file, then decode
            # Split into 50KB writes to stay under exec limits
            CHUNK = 50000
            await environment.exec(command="rm -f /tmp/pilot.b64 /tmp/pilot_bin")
            for i in range(0, len(b64), CHUNK):
                chunk = b64[i:i + CHUNK]
                await environment.exec(
                    command=f"cat >> /tmp/pilot.b64 << 'B64EOF'\n{chunk}\nB64EOF",
                    timeout_sec=10,
                )
            await environment.exec(
                command="base64 -d /tmp/pilot.b64 > /usr/local/bin/pilot && chmod +x /usr/local/bin/pilot && rm /tmp/pilot.b64",
                timeout_sec=30,
            )
            logger.info("Binary uploaded via base64 fallback")

        # Write config
        logger.info("Writing config...")
        model = self._resolve_model()
        config = self._build_config(model)
        await environment.exec(
            command=f"mkdir -p /root/.pilot && cat > /root/.pilot/config.yaml << 'CFGEOF'\n{config}CFGEOF",
        )

        # Upload learning DB
        db_path = Path(__file__).parent / "data" / "pilot.db"
        if db_path.exists():
            logger.info("Uploading learning DB...")
            await environment.exec(command="mkdir -p /root/.pilot/data")
            await environment.upload_file(source_path=db_path, target_path="/root/.pilot/data/pilot.db")

        # Env bootstrap
        logger.info("Running env bootstrap...")
        await self._run_env_bootstrap(environment)

        logger.info("Agent setup complete")

    async def _run_env_bootstrap(self, environment) -> None:
        """Pre-discover container environment and save to file.

        The Go prompt builder reads this file and injects it into the prompt,
        saving Claude one full turn of env discovery commands.
        """
        cmds = [
            "echo '=== FILES ==='",
            "find /app -maxdepth 3 -type f 2>/dev/null | head -100",
            "echo '=== BUILD SYSTEMS ==='",
            "ls /app/Makefile /app/pyproject.toml /app/Cargo.toml /app/package.json /app/CMakeLists.txt 2>/dev/null || echo 'none found'",
            "echo '=== README ==='",
            "cat /app/README* /app/INSTRUCTIONS* 2>/dev/null | head -200 || echo 'no readme'",
            "echo '=== PYTHON ==='",
            "python3 --version 2>/dev/null",
            "pip list 2>/dev/null | head -30 || echo 'pip: N/A'",
            "echo '=== SYSTEM ==='",
            "uname -m",
            "echo \"CPUs: $(nproc 2>/dev/null || echo N/A)\"",
            "free -m 2>/dev/null | grep Mem || echo 'free: N/A'",
            "echo '=== DATA FILES ==='",
            "find /app -maxdepth 2 \\( -name '*.csv' -o -name '*.json' -o -name '*.txt' -o -name '*.dat' -o -name '*.db' \\) 2>/dev/null | head -20",
        ]
        script = " ; ".join(cmds)
        try:
            await environment.exec(
                command=f"({script}) > /app/.pilot-env-context.txt 2>&1",
                timeout_sec=15,
            )
            logger.info("Environment bootstrap written to /app/.pilot-env-context.txt")
        except Exception as e:
            logger.warning(f"Env bootstrap failed (non-fatal): {e}")

    def _setup_env(self) -> dict[str, str]:
        """Environment variables for install script."""
        env = super()._setup_env()
        env.update(self._build_env())
        return env
