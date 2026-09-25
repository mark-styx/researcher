#!/usr/bin/env python3
# Bake-off wrapper: runs the real claude CLI with JSON output, logs cost and
# timing per call, and prints the plain result text so callers see no change.
import json, os, subprocess, sys, time

real = os.environ.get("BAKEOFF_REAL_CLAUDE", "/opt/homebrew/bin/claude")
log = os.environ.get("BAKEOFF_LOG")
args = sys.argv[1:]

if "-p" not in args and "--print" not in args:
    os.execv(real, [real] + args)

if "--output-format" in args:
    args[args.index("--output-format") + 1] = "json"
else:
    args += ["--output-format", "json"]
if "--max-budget-usd" not in args:
    args += ["--max-budget-usd", os.environ.get("BAKEOFF_MAX_BUDGET", "3.00")]

if os.environ.get("BAKEOFF_DENY_LOCAL") == "1":
    # From-scratch baseline: no local file access, so the finished book's
    # research on disk cannot leak into the run.
    args += ["--disallowedTools", "Bash", "Read", "Glob", "Grep"]

model = args[args.index("--model") + 1] if "--model" in args else ""
tools = [args[i + 1] for i, a in enumerate(args) if a == "--allowedTools"]

start = time.time()
p = subprocess.run([real] + args, stdin=sys.stdin, capture_output=True, text=True)
wall = time.time() - start

rec = {"arm": os.environ.get("BAKEOFF_ARM", ""), "topic": os.environ.get("BAKEOFF_TOPIC", ""),
       "model": model, "tools": tools, "wall_s": round(wall, 1), "exit": p.returncode}
result = ""
try:
    d = json.loads(p.stdout)
    result = d.get("result") or ""
    rec.update({k: d.get(k) for k in ("total_cost_usd", "num_turns", "duration_ms", "is_error", "subtype", "terminal_reason")})
    rec["web_searches"] = sum((m or {}).get("webSearchRequests", 0) for m in (d.get("modelUsage") or {}).values())
except json.JSONDecodeError:
    result = p.stdout
    rec["parse_error"] = True
if log:
    with open(log, "a") as f:
        f.write(json.dumps(rec) + "\n")

sys.stdout.write(result)
sys.stderr.write(p.stderr)
sys.exit(p.returncode if p.returncode else (1 if rec.get("is_error") else 0))
