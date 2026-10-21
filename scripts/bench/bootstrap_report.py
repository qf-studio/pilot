#!/usr/bin/env python3
"""Bootstrap cost harness: stream summarizer and report tool (TASK-581).

Two jobs, both offline and stdlib-only:

* ``summarize`` turns one finished session (a Claude Code stream-json capture,
  or the stdout of ``pilot navigator bootstrap``) plus the session metadata
  into the one-JSON-line record that scripts/bench/bootstrap-cost.sh appends to
  results/<date>.jsonl.
* ``report`` reads that JSONL, validates ``path:line`` refs against the clone
  when it is on disk, applies a prices JSON and prints a markdown table.

The CLI-reported USD (``total_cost_usd``) is the primary cost figure. The
estimate beside it is tokens x prices.json, so a stale price table can be
re-applied later without a rerun (the executor's own table is stale, see
TASK-581).

Nothing here runs ``claude`` or ``pilot``, and nothing writes under ``.agent/``:
the runner's scratch clones are the only place those directories are read.
Run with ``python3 -I`` (isolated mode); the tests are in
bootstrap_report_test.py.
"""
from __future__ import annotations

import argparse
import json
import math
import os
import re
import sys
from dataclasses import asdict, dataclass, field, fields
from typing import Any, Dict, Iterable, List, Optional, Sequence, Set, Tuple

TOKENS_PER_MILLION = 1_000_000

# path:line citation. The lookbehind keeps URL hosts and "a:b:c" fragments out.
REF_RE = re.compile(r"(?<![\w/.:-])([\w@+-][\w@+./-]*\.\w+):(\d+)(?!\d)")

BRIEF_RELPATH = os.path.join(".agent", "system", "bootstrap-research.md")
TICKETS_RELDIR = os.path.join(".agent", "tasks", "bootstrap")
FINDING_HEADING_RE = re.compile(r"^###\s+F\d+", re.MULTILINE)

# Directories that are never a coverage unit.
IGNORED_TOP_DIRS = {".git", ".agent", ".github", ".claude", "node_modules", "vendor"}


@dataclass
class Session:
    """One measured session: one line of the results JSONL."""

    repo: str
    stage: int
    cls: str = "?"  # JSON key "class"
    stack: str = ""
    loc: Optional[int] = None
    files: Optional[int] = None
    variant: str = "full"  # full | fallback | scoped
    run: int = 1
    model: str = ""
    effort: str = ""
    turn_cap: int = 0
    timeout_s: int = 0
    tokens_input: int = 0
    tokens_output: int = 0
    tokens_cache_write: int = 0
    tokens_cache_read: int = 0
    usd_reported: Optional[float] = None
    wall_s: float = 0.0
    turns: int = 0
    partial: bool = False
    stop_reason: str = "ok"  # ok | turns | budget | timeout | error
    files_read: int = 0
    coverage_touched: int = 0
    coverage_total: int = 0
    coverage_kind: str = "dirs"  # dirs | scope
    findings: Optional[int] = None
    tickets: Optional[int] = None
    item_refs: List[List[str]] = field(default_factory=list)
    scope: str = ""
    context_seeded: bool = False
    usage_source: str = "result"  # result | assistant_events | pilot_summary
    date: str = ""
    clone: str = ""

    def to_dict(self) -> Dict[str, Any]:
        data = asdict(self)
        data["class"] = data.pop("cls")
        return data

    @classmethod
    def from_dict(cls, data: Dict[str, Any]) -> "Session":
        data = dict(data)
        if "class" in data:
            data["cls"] = data.pop("class")
        known = {f.name for f in fields(cls)}
        return cls(**{k: v for k, v in data.items() if k in known})


@dataclass
class Prices:
    """Per-model USD per million tokens, from prices.json."""

    source: str
    as_of: str
    models: Dict[str, Dict[str, float]]

    @classmethod
    def load(cls, path: str) -> "Prices":
        with open(path, encoding="utf-8") as fh:
            raw = json.load(fh)
        return cls(
            source=str(raw.get("source", "")),
            as_of=str(raw.get("as_of", "")),
            models={k: {kk: float(vv) for kk, vv in v.items()} for k, v in raw["models"].items()},
        )

    def for_model(self, model: str) -> Optional[Dict[str, float]]:
        if model in self.models:
            return self.models[model]
        # A dated or suffixed id (claude-opus-5-5-20261001, ...[1m]) prices as its family.
        best = ""
        for name in self.models:
            if model.startswith(name) and len(name) > len(best):
                best = name
        return self.models[best] if best else None


def estimate_usd(session: Session, prices: Prices) -> Optional[float]:
    """Token-based estimate; None when the model is not in the price table."""
    table = prices.for_model(session.model)
    if table is None:
        return None
    return (
        session.tokens_input * table["input"]
        + session.tokens_output * table["output"]
        + session.tokens_cache_read * table["cache_read"]
        + session.tokens_cache_write * table["cache_write"]
    ) / TOKENS_PER_MILLION


def usd_for_guard(session: Session, prices: Optional[Prices]) -> float:
    """Reported USD, else the estimate, else 0: what the matrix guard sums."""
    if session.usd_reported is not None:
        return session.usd_reported
    if prices is not None:
        est = estimate_usd(session, prices)
        if est is not None:
            return est
    return 0.0


# ---------------------------------------------------------------- stream parsing


@dataclass
class StreamSummary:
    tokens_input: int = 0
    tokens_output: int = 0
    tokens_cache_write: int = 0
    tokens_cache_read: int = 0
    usd_reported: Optional[float] = None
    turns: int = 0
    subtype: str = ""  # result event subtype; "" when the stream has no result event
    usage_source: str = "result"
    files_read: Set[str] = field(default_factory=set)
    touched: Set[str] = field(default_factory=set)


def _relative_to(workdir: str, path: str) -> Optional[str]:
    """Path relative to workdir, or None when it is outside it."""
    if not path:
        return None
    if not os.path.isabs(path):
        path = os.path.join(workdir, path)
    rel = os.path.relpath(os.path.normpath(path), os.path.normpath(workdir))
    if rel == "." or rel == ".." or rel.startswith(".." + os.sep):
        return None
    return rel


def parse_stream(lines: Iterable[str], workdir: str) -> StreamSummary:
    """Fold a Claude Code stream-json capture into one StreamSummary.

    Tokens and cost come from the final ``result`` event. When the session was
    cut off before one (timeout, kill) the per-message usage of the assistant
    events is summed instead, once per message id, and the cost stays unknown.
    """
    out = StreamSummary()
    seen_messages: Dict[str, Dict[str, int]] = {}
    result: Optional[Dict[str, Any]] = None
    for raw in lines:
        raw = raw.strip()
        if not raw:
            continue
        try:
            event = json.loads(raw)
        except json.JSONDecodeError:
            continue  # a killed session can leave a truncated last line
        if not isinstance(event, dict):
            continue
        kind = event.get("type")
        if kind == "result":
            result = event
        elif kind == "assistant":
            message = event.get("message") or {}
            usage = message.get("usage")
            if isinstance(usage, dict) and message.get("id"):
                seen_messages[message["id"]] = usage
            for block in message.get("content") or []:
                if isinstance(block, dict) and block.get("type") == "tool_use":
                    _record_tool_use(out, block, workdir)

    if result is not None:
        usage = result.get("usage") or {}
        out.tokens_input = int(usage.get("input_tokens", 0) or 0)
        out.tokens_output = int(usage.get("output_tokens", 0) or 0)
        out.tokens_cache_write = int(usage.get("cache_creation_input_tokens", 0) or 0)
        out.tokens_cache_read = int(usage.get("cache_read_input_tokens", 0) or 0)
        cost = result.get("total_cost_usd")
        out.usd_reported = float(cost) if isinstance(cost, (int, float)) else None
        out.turns = int(result.get("num_turns", 0) or 0)
        out.subtype = str(result.get("subtype", ""))
    else:
        out.usage_source = "assistant_events"
        for usage in seen_messages.values():
            out.tokens_input += int(usage.get("input_tokens", 0) or 0)
            out.tokens_output += int(usage.get("output_tokens", 0) or 0)
            out.tokens_cache_write += int(usage.get("cache_creation_input_tokens", 0) or 0)
            out.tokens_cache_read += int(usage.get("cache_read_input_tokens", 0) or 0)
        out.turns = len(seen_messages)
    return out


def _record_tool_use(out: StreamSummary, block: Dict[str, Any], workdir: str) -> None:
    name = block.get("name")
    args = block.get("input") or {}
    if name == "Read":
        rel = _relative_to(workdir, str(args.get("file_path", "")))
        if rel is not None:
            out.files_read.add(rel)
            out.touched.add(rel)
    elif name in ("Grep", "Glob", "LS"):
        rel = _relative_to(workdir, str(args.get("path", "")))
        if rel is not None:
            out.touched.add(rel)


def stop_reason_for(subtype: str, exit_code: int) -> str:
    """Map a result subtype and process exit code to the record's stop_reason."""
    if subtype == "success":
        return "ok"
    if subtype == "error_max_turns":
        return "turns"
    if subtype == "error_max_budget_usd":
        return "budget"
    if not subtype and exit_code == 124:
        return "timeout"
    return "error"


# --------------------------------------------------------------------- coverage


def top_level_dirs(workdir: str) -> List[str]:
    names = []
    for name in sorted(os.listdir(workdir)):
        if name.startswith(".") or name in IGNORED_TOP_DIRS:
            continue
        if os.path.isdir(os.path.join(workdir, name)):
            names.append(name)
    return names


def coverage(workdir: str, scope: Sequence[str], touched: Iterable[str]) -> Tuple[int, int, str]:
    """(units touched, units total, kind). Units are the scope entries when a
    scope is set, otherwise the top-level directories of the checkout."""
    touched_paths = [t.replace(os.sep, "/") for t in touched]
    if scope:
        units = [s.strip("/") for s in scope if s.strip("/")]
        kind = "scope"
    else:
        units = top_level_dirs(workdir)
        kind = "dirs"
    hit = 0
    for unit in units:
        if any(p == unit or p.startswith(unit + "/") for p in touched_paths):
            hit += 1
    return hit, len(units), kind


# ------------------------------------------------------------------ output parse


def extract_refs(text: str) -> List[str]:
    """Every distinct ``path:line`` citation in text, in first-seen order."""
    seen: List[str] = []
    for match in REF_RE.finditer(text):
        ref = f"{match.group(1)}:{match.group(2)}"
        if ref not in seen:
            seen.append(ref)
    return seen


def read_items(workdir: str, stage: int) -> Tuple[Optional[int], Optional[int], List[List[str]]]:
    """(findings, tickets, per-item refs) parsed from a stage's output files."""
    if stage == 1:
        path = os.path.join(workdir, BRIEF_RELPATH)
        if not os.path.isfile(path):
            return 0, None, []
        text = _read_text(path)
        starts = [m.start() for m in FINDING_HEADING_RE.finditer(text)]
        chunks = [text[a:b] for a, b in zip(starts, starts[1:] + [len(text)])]
        return len(chunks), None, [extract_refs(c) for c in chunks]
    if stage == 2:
        directory = os.path.join(workdir, TICKETS_RELDIR)
        if not os.path.isdir(directory):
            return None, 0, []
        names = sorted(n for n in os.listdir(directory) if re.fullmatch(r"ticket-\d+\.md", n))
        refs = [extract_refs(_read_text(os.path.join(directory, n))) for n in names]
        return None, len(names), refs
    return None, None, []


def _read_text(path: str) -> str:
    with open(path, encoding="utf-8", errors="replace") as fh:
        return fh.read()


def parse_pilot_summary(text: str) -> StreamSummary:
    """Parse the summary ``pilot navigator bootstrap`` prints on exit.

    The summary rounds tokens ("1.2K", "3.40M") and has no cache split, so
    stage 0 token columns are approximate and the reported USD is the figure to
    trust. A partial run prints the "stopped on its turn or time limit" line.
    """
    out = StreamSummary(usage_source="pilot_summary")
    m = re.search(r"\$([0-9.]+) \(reported\)", text)
    if m:
        out.usd_reported = float(m.group(1))
    m = re.search(r"Tokens:\s+(\S+) in / (\S+) out", text)
    if m:
        out.tokens_input = _parse_token_count(m.group(1))
        out.tokens_output = _parse_token_count(m.group(2))
    if "stopped on its turn or time limit" in text:
        out.subtype = "error_max_turns"
    elif m or out.usd_reported is not None:
        out.subtype = "success"
    return out


def _parse_token_count(text: str) -> int:
    m = re.fullmatch(r"([0-9.]+)([KkMm]?)", text)
    if not m:
        return 0
    scale = {"": 1, "k": 1_000, "m": 1_000_000}[m.group(2).lower()]
    return int(round(float(m.group(1)) * scale))


def parse_loc_json(text: str) -> Tuple[Optional[int], Optional[int]]:
    """(code lines, file count) from ``scc --format json`` or ``cloc --json``."""
    try:
        data = json.loads(text)
    except json.JSONDecodeError:
        return None, None
    if isinstance(data, list):  # scc: one object per language
        code = sum(int(x.get("Code", 0)) for x in data if isinstance(x, dict))
        files = sum(int(x.get("Count", 0)) for x in data if isinstance(x, dict))
        return code, files
    if isinstance(data, dict) and isinstance(data.get("SUM"), dict):  # cloc
        return int(data["SUM"].get("code", 0)), int(data["SUM"].get("nFiles", 0))
    return None, None


# ----------------------------------------------------------------- ref validation


def line_count(path: str) -> int:
    with open(path, "rb") as fh:
        data = fh.read()
    if not data:
        return 0
    return data.count(b"\n") + (0 if data.endswith(b"\n") else 1)


def ref_valid(ref: str, clone_root: str) -> bool:
    """True when the path exists inside clone_root and the line is in range."""
    path, _, line = ref.rpartition(":")
    root = os.path.realpath(clone_root)
    target = os.path.realpath(os.path.join(root, path[2:] if path.startswith("./") else path))
    # The refs are model output: never follow one out of the clone.
    if not (target == root or target.startswith(root + os.sep)) or not os.path.isfile(target):
        return False
    try:
        number = int(line)
    except ValueError:
        return False
    return 1 <= number <= line_count(target)


@dataclass
class Evidence:
    total: int = 0
    valid: int = 0
    items: int = 0
    items_ok: int = 0  # items with >= 3 valid refs
    density: Optional[float] = None

    @property
    def validity(self) -> Optional[float]:
        return None if self.total == 0 else self.valid / self.total


MIN_VALID_REFS_PER_TICKET = 3


def evidence_for(session: Session, clone_root: Optional[str]) -> Optional[Evidence]:
    """Evidence stats for a session, or None when there is no clone to check."""
    if clone_root is None or not os.path.isdir(clone_root):
        return None
    ev = Evidence(items=len(session.item_refs))
    for refs in session.item_refs:
        ok = sum(1 for r in refs if ref_valid(r, clone_root))
        ev.total += len(refs)
        ev.valid += ok
        if ok >= MIN_VALID_REFS_PER_TICKET:
            ev.items_ok += 1
    if ev.items:
        ev.density = ev.total / ev.items
    return ev


# --------------------------------------------------------------------- JSONL io


@dataclass
class Results:
    sessions: List[Session] = field(default_factory=list)
    matrix_stop: Optional[Dict[str, Any]] = None
    bad_lines: int = 0


def load_results(path: str) -> Results:
    """Read a results JSONL. Tolerates a truncated last line and unknown
    record types, so a run killed mid-write is still reportable."""
    res = Results()
    with open(path, encoding="utf-8") as fh:
        for raw in fh:
            raw = raw.strip()
            if not raw:
                continue
            try:
                data = json.loads(raw)
            except json.JSONDecodeError:
                res.bad_lines += 1
                continue
            if not isinstance(data, dict):
                res.bad_lines += 1
                continue
            kind = data.get("type", "session")
            if kind == "matrix_stop":
                res.matrix_stop = data
            elif kind == "session":
                try:
                    res.sessions.append(Session.from_dict(data))
                except TypeError:
                    res.bad_lines += 1
    return res


def matrix_guard_hit(sessions: Sequence[Session], limit: float, prices: Optional[Prices] = None) -> Optional[int]:
    """Index of the session after which the summed USD first reaches limit
    (the runner stops there), or None if it never does."""
    total = 0.0
    for i, s in enumerate(sessions):
        total += usd_for_guard(s, prices)
        if total >= limit:
            return i
    return None


def latest_results_file(directory: str) -> Optional[str]:
    if not os.path.isdir(directory):
        return None
    names = sorted(n for n in os.listdir(directory) if n.endswith(".jsonl"))
    return os.path.join(directory, names[-1]) if names else None


# ------------------------------------------------------------------- rendering


def percentile(values: Sequence[float], pct: float) -> float:
    """Nearest-rank percentile; with n=2 the p90 is the larger run."""
    ordered = sorted(values)
    rank = max(1, math.ceil(pct / 100 * len(ordered)))
    return ordered[rank - 1]


def _usd(value: Optional[float]) -> str:
    return "n/a" if value is None else f"${value:,.2f}"


def _pct(value: Optional[float]) -> str:
    return "n/a" if value is None else f"{value * 100:.0f}%"


def _tok(value: int) -> str:
    return f"{value:,}"


def _clone_for(session: Session, clones_dir: Optional[str]) -> Optional[str]:
    if clones_dir:
        candidate = os.path.join(clones_dir, session.repo)
        if os.path.isdir(candidate):
            return candidate
    if session.clone and os.path.isdir(session.clone):
        return session.clone
    return None


def render_report(
    results: Results, prices: Prices, clones_dir: Optional[str] = None
) -> str:
    sessions = results.sessions
    out: List[str] = []
    out.append("# Bootstrap cost matrix")
    out.append("")
    out.append(
        f"Prices: {prices.source} (as of {prices.as_of}). "
        "USD reported is the CLI's `total_cost_usd` and is the figure to trust; "
        "USD est. is tokens x prices.json. Stage 0 tokens come from a rounded "
        "summary with no cache split, so its USD est. is approximate."
    )
    out.append("")
    header = (
        "| Repo | Class | LOC | Stage | Variant | Run | Model | Turns | Wall | Partial "
        "| USD reported | USD est. | In | Out | Cache write | Cache read "
        "| Files read | Coverage | Findings / tickets | Refs per item | Evidence valid | Tickets >=3 valid |"
    )
    out.append(header)
    out.append("|" + "---|" * (header.count("|") - 1))

    for s in sessions:
        ev = evidence_for(s, _clone_for(s, clones_dir))
        cov = f"{s.coverage_touched}/{s.coverage_total} {s.coverage_kind}" if s.coverage_total else "n/a"
        items = s.findings if s.findings is not None else s.tickets
        partial = f"yes ({s.stop_reason})" if s.partial else "no"
        out.append(
            "| "
            + " | ".join(
                [
                    s.repo,
                    s.cls,
                    "n/a" if s.loc is None else f"{s.loc:,}",
                    str(s.stage),
                    s.variant,
                    str(s.run),
                    s.model or "n/a",
                    str(s.turns),
                    f"{s.wall_s / 60:.1f} min",
                    partial,
                    _usd(s.usd_reported),
                    _usd(estimate_usd(s, prices)),
                    _tok(s.tokens_input),
                    _tok(s.tokens_output),
                    _tok(s.tokens_cache_write),
                    _tok(s.tokens_cache_read),
                    str(s.files_read),
                    cov,
                    "n/a" if items is None else str(items),
                    "n/a" if ev is None or ev.density is None else f"{ev.density:.1f}",
                    _pct(ev.validity) if ev else "n/a",
                    f"{ev.items_ok}/{ev.items}" if ev and s.stage == 2 else "n/a",
                ]
            )
            + " |"
        )

    out.append("")
    out.append("## USD reported per repo, stage and variant")
    out.append("")
    out.append("| Repo | Stage | Variant | Runs | p50 | p90 | Max |")
    out.append("|---|---|---|---|---|---|---|")
    groups: Dict[Tuple[str, int, str], List[float]] = {}
    for s in sessions:
        if s.usd_reported is not None:
            groups.setdefault((s.repo, s.stage, s.variant), []).append(s.usd_reported)
    for (repo, stage, variant), vals in sorted(groups.items()):
        out.append(
            f"| {repo} | {stage} | {variant} | {len(vals)} | {_usd(percentile(vals, 50))} "
            f"| {_usd(percentile(vals, 90))} | {_usd(max(vals))} |"
        )

    total = sum(usd_for_guard(s, prices) for s in sessions)
    out.append("")
    out.append(f"Sessions: {len(sessions)}. Summed spend (reported, estimate where unreported): {_usd(total)}.")
    if results.matrix_stop:
        ms = results.matrix_stop
        out.append("")
        out.append(
            f"**Matrix guard hit**: stopped at {_usd(ms.get('usd_total'))} against a limit of "
            f"{_usd(ms.get('limit_usd'))} after {ms.get('sessions_run')} of {ms.get('sessions_planned')} planned sessions."
        )
    if results.bad_lines:
        out.append("")
        out.append(f"{results.bad_lines} unreadable line(s) skipped (a truncated last line is expected after a kill).")
    return "\n".join(out) + "\n"


# -------------------------------------------------------------------------- CLI


def build_session(args: argparse.Namespace) -> Session:
    exit_code = args.exit_code
    scope = [s for s in args.scope.split(",") if s]
    if args.kind == "pilot":
        summary = parse_pilot_summary(_read_text(args.stream))
        findings, tickets, item_refs = None, None, []
    else:
        with open(args.stream, encoding="utf-8", errors="replace") as fh:
            summary = parse_stream(fh, args.workdir)
        findings, tickets, item_refs = read_items(args.workdir, args.stage)
    stop = stop_reason_for(summary.subtype, exit_code)
    touched, total, kind = (0, 0, "dirs")
    if args.kind != "pilot":
        touched, total, kind = coverage(args.workdir, scope, summary.touched)
    return Session(
        repo=args.repo,
        stage=args.stage,
        cls=args.cls,
        stack=args.stack,
        loc=args.loc,
        files=args.files,
        variant=args.variant,
        run=args.run,
        model=args.model,
        effort=args.effort,
        turn_cap=args.turn_cap,
        timeout_s=args.timeout_s,
        tokens_input=summary.tokens_input,
        tokens_output=summary.tokens_output,
        tokens_cache_write=summary.tokens_cache_write,
        tokens_cache_read=summary.tokens_cache_read,
        usd_reported=summary.usd_reported,
        wall_s=args.wall_s,
        turns=summary.turns,
        partial=stop != "ok",
        stop_reason=stop,
        files_read=len(summary.files_read),
        coverage_touched=touched,
        coverage_total=total,
        coverage_kind=kind,
        findings=findings,
        tickets=tickets,
        item_refs=item_refs,
        scope=args.scope,
        context_seeded=args.context_seeded == "1",
        usage_source=summary.usage_source,
        date=args.date,
        clone=args.clone,
    )


def _int_or_none(text: str) -> Optional[int]:
    return int(text) if text not in ("", "null", "None") else None


def cmd_summarize(args: argparse.Namespace) -> int:
    print(json.dumps(build_session(args).to_dict(), sort_keys=True))
    return 0


def cmd_usd(args: argparse.Namespace) -> int:
    """Print the guard USD of the one JSONL session line on stdin."""
    prices = Prices.load(args.prices) if args.prices else None
    data = json.loads(sys.stdin.readline())
    print(f"{usd_for_guard(Session.from_dict(data), prices):.6f}")
    return 0


def cmd_loc(args: argparse.Namespace) -> int:
    """Print 'LOC FILES' from scc/cloc JSON on stdin ('0 0' if unparseable)."""
    code, files = parse_loc_json(sys.stdin.read())
    print(f"{code or 0} {files or 0}")
    return 0


def cmd_report(args: argparse.Namespace) -> int:
    path = args.input
    if not path:
        path = latest_results_file(args.results_dir)
        if path is None:
            print(f"No results yet in {args.results_dir}; run scripts/bench/bootstrap-cost.sh first.")
            return 0
    prices = Prices.load(args.prices)
    sys.stdout.write(render_report(load_results(path), prices, args.clones))
    return 0


def build_parser() -> argparse.ArgumentParser:
    here = os.path.dirname(os.path.abspath(__file__))
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    sub = parser.add_subparsers(dest="command", required=True)

    rep = sub.add_parser("report", help="print the markdown table for a results JSONL")
    rep.add_argument("--input", help="results JSONL (default: latest in --results-dir)")
    rep.add_argument("--results-dir", default=os.path.join(here, "results"))
    rep.add_argument("--prices", default=os.path.join(here, "prices.json"))
    rep.add_argument("--clones", help="directory holding one checkout per repo name, for ref validation")
    rep.set_defaults(func=cmd_report)

    summ = sub.add_parser("summarize", help="turn one finished session into a JSONL record")
    summ.add_argument("--kind", choices=("claude", "pilot"), default="claude")
    summ.add_argument("--stream", required=True, help="stream-json capture, or pilot stdout")
    summ.add_argument("--workdir", required=True)
    summ.add_argument("--clone", default="")
    summ.add_argument("--repo", required=True)
    summ.add_argument("--class", dest="cls", default="?")
    summ.add_argument("--stack", default="")
    summ.add_argument("--loc", type=_int_or_none, default=None)
    summ.add_argument("--files", type=_int_or_none, default=None)
    summ.add_argument("--stage", type=int, required=True)
    summ.add_argument("--variant", default="full")
    summ.add_argument("--run", type=int, default=1)
    summ.add_argument("--model", default="")
    summ.add_argument("--effort", default="")
    summ.add_argument("--turn-cap", type=int, default=0)
    summ.add_argument("--timeout-s", type=int, default=0)
    summ.add_argument("--wall-s", type=float, default=0.0)
    summ.add_argument("--exit-code", type=int, default=0)
    summ.add_argument("--scope", default="")
    summ.add_argument("--context-seeded", default="0")
    summ.add_argument("--date", default="")
    summ.set_defaults(func=cmd_summarize)

    usd = sub.add_parser("usd", help="print the guard USD of one session line on stdin")
    usd.add_argument("--prices", default=os.path.join(here, "prices.json"))
    usd.set_defaults(func=cmd_usd)

    loc = sub.add_parser("loc", help="print 'LOC FILES' from scc/cloc JSON on stdin")
    loc.set_defaults(func=cmd_loc)
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = build_parser().parse_args(argv)
    return int(args.func(args))


if __name__ == "__main__":
    sys.exit(main())
