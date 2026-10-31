#!/usr/bin/env python3
"""Replay a real fix: put a contract on the code just before a commit that
fixed bugs, then have a judge score the findings against what the fix says it
fixed. Run inside tests/isolated.sh's namespace (it starts real professionals).

  replay.py --bin B --books DIR --repo R --commit C --cast a,b --target TEXT [--label L]

The repo's .administration/ is copied into the replay checkout, which sits beside
the repo (so roster paths like ../.claude/agents still resolve). The commit
message is the answer key, so pick commits whose messages say what was wrong.
Verdicts go into the books (matches the key: confirmed; real but not in the
key: likely; wrong: false), so stats and the Director see them.
"""
import argparse, json, os, re, shutil, subprocess, sys, time

ap = argparse.ArgumentParser()
for a in ("--bin", "--books", "--repo", "--commit", "--cast", "--target"):
    ap.add_argument(a, required=True)
ap.add_argument("--label", default="")
ap.add_argument("--judge-model", default="sonnet")
ap.add_argument("--keep", action="store_true")
ap.add_argument("--rejudge", help="score an existing contract again, without running anyone")
ap.add_argument("--intro", help="the commit that introduced the bugs: replay it as a --changes contract on its own diff")
o = ap.parse_args()

env = {**os.environ, "ADMINISTRATION_HOME": o.books}
git = lambda *a, **k: subprocess.run(["git", "-C", o.repo, *a], check=True, capture_output=True, text=True, **k).stdout
key = git("log", "-1", "--format=%s%n%n%b", o.commit)
wt = os.path.join(os.path.dirname(os.path.abspath(o.repo)), "replay-" + o.commit[:7] + (("-" + o.label) if o.label else ""))
if not o.rejudge:
    if os.path.exists(wt):
        subprocess.run(["git", "-C", o.repo, "worktree", "remove", "--force", wt], capture_output=True)
    git("worktree", "add", "--detach", wt, (o.intro or o.commit + "^"))
    shutil.copytree(os.path.join(o.repo, ".administration"), os.path.join(wt, ".administration"))
else:
    wt = o.repo

def adm(*a, check=True):
    p = subprocess.run([o.bin, *a], env=env, capture_output=True, text=True, cwd=wt)
    if check and p.returncode not in (0, 2):
        sys.exit(f"administration {a}: {p.returncode}\n{p.stdout}\n{p.stderr}")
    return p

t0 = time.time()
if o.rejudge:
    cid = o.rejudge
else:
    extra = ["--changes", "--base", o.intro + "^"] if o.intro else []
    cid = adm("place", o.target, "--cast", o.cast, "--project", wt, *extra).stdout.split()[1]
    adm("wait", cid, "--timeout", "40m")
    adm("close", cid)
status = json.loads(adm("status", cid, "--json").stdout)[0]
books_dir = os.path.join(o.books, "contracts", cid)

# The findings, numbered per professional as the report lists them.
findings = []
for p in status["professionals"]:
    rep = open(os.path.join(books_dir, p["name"], "report.md")).read()
    sec = rep.split("## Findings", 1)[1].split("## Could not reach", 1)[0]
    items = [l[2:].strip() for l in sec.splitlines() if l.startswith("- ")]
    for i, f in enumerate(items, 1):
        findings.append({"pro": p["name"], "n": i, "text": f})

def claude_json(prompt):
    jp = subprocess.run(["claude", "-p", prompt, "--model", o.judge_model, "--tools", "", "--strict-mcp-config",
                         "--no-session-persistence", "--output-format", "json"],
                        capture_output=True, text=True, stdin=subprocess.DEVNULL, timeout=600)
    jr = json.loads(jp.stdout)
    m = re.search(r"\{.*\}", jr.get("result", ""), re.S)
    return (json.loads(m.group(0)) if m else {}), jr.get("total_cost_usd", 0)

# Step 1, from the answer key alone: the bugs it fixed. The judge hasn't seen
# a finding yet, so it can't grow the key to fit them.
bugs_doc, c1 = claude_json(f"""Here is the commit message of a fix. List each distinct defect it says it fixed:
a behaviour that was wrong in the code before this commit. Leave out refactors, tests, comment or doc edits,
and changes it made for style. Reply with JSON only: {{"bugs": ["<one line each>"]}}
<<<
{key}
>>>""")
key_bugs = bugs_doc.get("bugs", [])

# Step 2: match each finding to those bugs, or call it plausible or false.
verdict, c2 = claude_json(f"""Score a code review against a fixed list of known bugs.

KNOWN BUGS (numbered):
{chr(10).join(f"[{i}] {b}" for i, b in enumerate(key_bugs))}

The commit message they came from, for context:
<<<
{key}
>>>

FINDINGS from the review of the code before the fix (numbered):
{chr(10).join(f"[{i}] ({f['pro']}) {f['text']}" for i, f in enumerate(findings))}

Reply with JSON only:
{{"findings": [{{"i": <finding>, "verdict": "key" | "plausible" | "false", "bug": <known bug number, for key only>, "why": "<a few words>"}}]}}
"key": it describes that same known defect. "plausible": a different, believable real problem. "false": wrong, not a defect, or only taste.
""")
judge_cost = c1 + c2
found_bugs = {v.get("bug") for v in verdict.get("findings", []) if v.get("verdict") == "key"}
bugs = [{"bug": b, "found_by": [v["i"] for v in verdict.get("findings", []) if v.get("verdict") == "key" and v.get("bug") == i]}
        for i, b in enumerate(key_bugs)]

# Into the books, as each finding's check.
by_pro = {}
for v in verdict.get("findings", []):
    if 0 <= v["i"] < len(findings):
        f = findings[v["i"]]
        by_pro.setdefault(f["pro"], []).append({"finding": f["n"], "why": v.get("why", ""),
            "verdict": {"key": "confirmed", "plausible": "likely"}.get(v["verdict"], "false")})
subprocess.run([o.bin, "verdicts", cid, "-"], input=json.dumps(by_pro), env=env, capture_output=True, text=True, check=True)

found = [b for b in bugs if b.get("found_by")]
cost = sum(s.get("cost_usd") or 0 for s in status["status"].values())
counts = {k: sum(1 for v in verdict.get("findings", []) if v["verdict"] == k) for k in ("key", "plausible", "false")}
result = {"commit": o.commit[:7], "intro": (o.intro or "")[:7], "label": o.label, "contract": cid, "cast": o.cast,
          "bugs_in_key": len(bugs), "bugs_found": len(found), "findings": len(findings), **counts,
          "cost_usd": round(cost, 4), "judge_usd": round(judge_cost, 4), "minutes": round((time.time() - t0) / 60, 1),
          "states": {n: s["state"] for n, s in status["status"].items()},
          "dossiers": {p["name"]: p.get("dossier_bytes", 0) for p in status["professionals"]},
          "bugs": [{"bug": b["bug"], "found": bool(b.get("found_by"))} for b in bugs]}
print(json.dumps(result, indent=1))
with open(os.path.join(o.books, "replays.jsonl"), "a") as f:
    f.write(json.dumps(result) + "\n")
if not o.keep and not o.rejudge:
    subprocess.run(["git", "-C", o.repo, "worktree", "remove", "--force", wt], capture_output=True)
