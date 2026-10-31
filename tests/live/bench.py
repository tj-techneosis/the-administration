#!/usr/bin/env python3
"""The Director bench: how well does each way of casting a contract find
real bugs? Every case is a real fix from a repo's history; the code just
before it is the contract, the fix's commit message the answer key.

  bench.py --bin B --books DIR --repo R --cases cases.json --variants a,b [--only case,case]
           [--roster a,b] [--trained DIR]

A variant is a way to cast (see VARIANTS). Results go to DIR/bench.jsonl, one
line per case and variant; `bench.py --board DIR` prints the standings.
Run inside tests/isolated.sh's namespace: it starts real professionals.
"""
import argparse, hashlib, json, os, re, shutil, subprocess, sys, time

# How a contract gets its cast. "director" casts with `director cast` (its
# model), then places those specialists (plus any roster names given).
# "roster" casts the repo's own professionals (--roster); "trained" places
# the same roles as trained by the Director from one-line requests (the .md
# files in --trained), to measure training against hand refining.
VARIANTS = {
    "roster": {"cast": None},
    "director-sonnet": {"director": "sonnet"},
    "director-opus": {"director": "opus"},
    "director-haiku": {"director": "haiku"},
    "trained": {"with": None},
}

ap = argparse.ArgumentParser()
ap.add_argument("--bin"); ap.add_argument("--books"); ap.add_argument("--repo")
ap.add_argument("--cases"); ap.add_argument("--variants", default="roster,director-sonnet")
ap.add_argument("--only", default=""); ap.add_argument("--pro-model", default="sonnet")
ap.add_argument("--judge-model", default="sonnet"); ap.add_argument("--board")
ap.add_argument("--reach-only", action="store_true", help="add in-reach counts to existing change-case rows")
ap.add_argument("--rejudge", default="", help="re-judge rows of these variants (comma list) with the current judge")
ap.add_argument("--label", default="", help="suffix for the variant's name on the board (a prompt version, a repeat)")
ap.add_argument("--roster", default="", help="the roster names the roster variant casts (comma list)")
ap.add_argument("--trained", default="", help="a folder of trained professionals for the trained variant")
o = ap.parse_args()
VARIANTS["roster"]["cast"] = o.roster
if o.trained:
    VARIANTS["trained"]["with"] = sorted(os.path.join(o.trained, f) for f in os.listdir(o.trained) if f.endswith(".md"))


def board(books):
    rows = [json.loads(l) for l in open(os.path.join(books, "bench.jsonl"))]
    errs = [r for r in rows if "error" in r]
    rows = [r for r in rows if "error" not in r]
    if errs:
        print(f"{len(errs)} runs errored and are left out: " + ", ".join(f"{r['variant']}/{r['case']}" for r in errs) + "\n")
    by = {}
    for r in rows:
        v = by.setdefault(r["variant"], {"cases": 0, "found": 0, "key": 0, "findings": 0, "false": 0, "minor": 0, "j2": 0, "minutes": 0.0, "rf": 0, "rk": 0})
        v["rf"] += r.get("found_in_reach", r["bugs_found"]); v["rk"] += r.get("in_reach", r["bugs_in_key"])
        v["cases"] += 1; v["found"] += r["bugs_found"]; v["key"] += r["bugs_in_key"]
        v["findings"] += r["findings"]; v["false"] += r["false"]; v["minutes"] += r["minutes"]
        v["minor"] += r.get("minor", 0); v["j2"] += r.get("judge") == 2
    fails = sum(len(r.get("failed") or {}) for r in rows)
    if fails:
        print(f"{fails} professionals failed (see each row's \"failed\"); their cases are still scored, so a failure costs its variant\n")
    print(f"{'variant':18} {'cases':>5} {'found':>11} {'in reach':>11} {'findings':>8} {'false':>5} {'minor':>5} {'min/case':>8}")
    for name, v in sorted(by.items(), key=lambda kv: -kv[1]["found"] / max(1, kv[1]["key"])):
        pct = 100 * v["found"] / max(1, v["key"])
        rp = 100 * v["rf"] / max(1, v["rk"])
        print(f"{name:18} {v['cases']:5} {v['found']:4}/{v['key']:<3} {pct:3.0f}% {v['rf']:4}/{v['rk']:<3} {rp:3.0f}% {v['findings']:8} {v['false']:5} {(str(v['minor']) if v['j2'] == v['cases'] else '-'):>5} {v['minutes'] / v['cases']:8.1f}")
    print()
    cases = sorted({r["case"] for r in rows})
    variants = sorted(by)
    print(f"{'case':10} " + " ".join(f"{v[:16]:>16}" for v in variants))
    for c in cases:
        cells = []
        for v in variants:
            rs = [r for r in rows if r["case"] == c and r["variant"] == v]
            cells.append(" ".join(f"{r['bugs_found']}/{r['bugs_in_key']}" for r in rs) or "-")
        print(f"{c:10} " + " ".join(f"{x:>16}" for x in cells))


if o.board:
    board(o.board); sys.exit(0)

env = {**os.environ, "ADMINISTRATION_HOME": o.books, "ADMINISTRATION_WATCH": "off"}
cases = json.load(open(o.cases))
only = set(filter(None, o.only.split(",")))


def git(*a):
    return subprocess.run(["git", "-C", o.repo, *a], check=True, capture_output=True, text=True).stdout


def adm(cwd, *a, ok=(0, 2)):
    p = subprocess.run([o.bin, *a], env=env, capture_output=True, text=True, cwd=cwd)
    if p.returncode not in ok:
        raise RuntimeError(f"administration {a[:3]}: {p.returncode}\n{p.stdout[-800:]}\n{p.stderr[-800:]}")
    return p


def claude_json(prompt, tries=3):
    for t in range(tries):
        jp = subprocess.run(["claude", "-p", prompt, "--model", o.judge_model, "--tools", "", "--strict-mcp-config",
                             "--no-session-persistence", "--output-format", "json"],
                            capture_output=True, text=True, stdin=subprocess.DEVNULL, timeout=600)
        try:
            jr = json.loads(jp.stdout)
            m = re.search(r"\{.*\}", jr.get("result", ""), re.S)
            return (json.loads(m.group(0)) if m else {}), jr.get("total_cost_usd", 0)
        except json.JSONDecodeError:
            if t == tries - 1:
                raise  # a judge that can't write JSON three times running is worth seeing


key_cache = {}


def key_bugs(commit):
    """Step 1 of the judge, from the answer key alone, once per case so every
    variant is scored against the same list."""
    if commit in key_cache:
        return key_cache[commit]
    path = os.path.join(o.books, "keys", commit + ".json")
    if os.path.exists(path):
        key_cache[commit] = json.load(open(path))
        return key_cache[commit]
    msg = git("log", "-1", "--format=%s%n%n%b", commit)
    doc, _ = claude_json(f"""Here is the commit message of a fix. List each distinct defect it says it fixed:
a behaviour that was wrong in the code before this commit. Leave out refactors, tests, comment or doc edits,
and changes it made for style. Reply with JSON only: {{"bugs": ["<one line each>"]}}
<<<
{msg}
>>>""")
    bugs = [b for b in doc.get("bugs") or [] if isinstance(b, str) and b.strip()]
    if not bugs:
        raise RuntimeError(f"the judge listed no bugs for {commit}; not cached, try again")
    key_cache[commit] = {"message": msg, "bugs": bugs}
    os.makedirs(os.path.dirname(path), exist_ok=True)
    json.dump(key_cache[commit], open(path, "w"), indent=1)
    return key_cache[commit]


def reach(case):
    """For a change case, which key bugs the diff could have shown: the code
    the fix changed was in files the diff touched, or used what they changed.
    A bug that shipped before the base is out of reach of any sweep of it.
    Cached like the key; None for snapshot cases (everything is in reach)."""
    if not case.get("base"):
        return None
    key = key_bugs(case["commit"])
    # Keyed by the bug list too: its numbers index that list.
    tag = hashlib.sha1(("\n".join(key["bugs"]) + "\0" + case["base"]).encode()).hexdigest()[:12]
    path = os.path.join(o.books, "keys", f"{case['commit']}-{tag}.reach.json")
    if os.path.exists(path):
        return json.load(open(path))["reach"]
    fix = git("show", "--stat", "--format=", case["commit"])
    fixdiff = git("show", "--format=", case["commit"])[:60000]
    change = git("diff", "--stat", case["base"], case["commit"] + "^")
    changediff = git("diff", case["base"], case["commit"] + "^")[:60000]
    doc, _ = claude_json(f"""A reviewer was given only the CHANGE below (a diff) and asked to test it and everything it
affects. Later a FIX commit repaired the KNOWN BUGS. For each bug, decide if it was within reach of that review:
the change introduced it, or the change touched the code it lives in or code it depends on. A bug that existed
before the change in code the change didn't touch is out of reach.

KNOWN BUGS (numbered):
{chr(10).join(f"[{i}] {b}" for i, b in enumerate(key["bugs"]))}

CHANGE (files):
{change}
CHANGE (diff, maybe cut):
<<<
{changediff}
>>>

FIX (files):
{fix}
FIX (diff, maybe cut):
<<<
{fixdiff}
>>>

Reply with JSON only: {{"reach": [<bug numbers within reach>], "why": {{"<n>": "<a few words>"}}}}""")
    raw = doc.get("reach")
    if not isinstance(raw, list):
        raise RuntimeError(f"the judge gave no reach list for {case['id']}; not cached")
    doc["reach"] = sorted({n for n in raw if isinstance(n, int) and not isinstance(n, bool) and 0 <= n < len(key["bugs"])})
    json.dump(doc, open(path, "w"), indent=1)
    return doc["reach"]


def judge(findings, key):
    if not findings:
        return {"findings": []}
    try:
        return judge_once(findings, key)
    except json.JSONDecodeError:
        if len(findings) < 2:
            raise
    # A long list can defeat the judge's JSON; score it in halves.
    h = len(findings) // 2
    a, b = judge(findings[:h], key), judge(findings[h:], key)
    return {"findings": a.get("findings", []) + [{**x, "i": x["i"] + h} for x in b.get("findings", [])
                                                 if isinstance(x.get("i"), int)]}


def judge_once(findings, key):
    verdict, _ = claude_json(f"""Score a code review against a fixed list of known bugs.

KNOWN BUGS (numbered):
{chr(10).join(f"[{i}] {b}" for i, b in enumerate(key["bugs"]))}

The commit message they came from, for context:
<<<
{key["message"]}
>>>

FINDINGS from the review of the code before the fix (numbered):
{chr(10).join(f"[{i}] ({f['pro']}) {f['text']}" for i, f in enumerate(findings))}

Reply with JSON only:
{{"findings": [{{"i": <finding>, "verdict": "key" | "plausible" | "minor" | "false", "bug": <known bug number, for key only>, "why": "<a few words, no quotation marks>"}}]}}
"key": it describes that same known defect. "plausible": a different, believable real problem that matters.
"minor": real and as described, but small: cosmetic, still readable or usable, a scroll or an extra step away.
"false": wrong, not reproducible as described, not a defect, speculation with nothing behind it, or only taste.
""")
    return verdict


def read_findings(cdir, names):
    findings = []
    for name in names:
        rp = os.path.join(cdir, name, "report.md")
        if not os.path.exists(rp):
            continue  # failed before writing one; it shows under "failed"
        rep = open(rp).read()
        sec = rep.split("## Findings", 1)[1].split("## Could not reach", 1)[0]
        for i, line in enumerate((l[2:].strip() for l in sec.splitlines() if l.startswith("- ")), 1):
            findings.append({"pro": name, "n": i, "text": line})
    return findings


def score(vs, key):
    found = sorted({x.get("bug") for x in vs if x.get("verdict") == "key" and isinstance(x.get("bug"), int)
                    and not isinstance(x.get("bug"), bool) and 0 <= x.get("bug") < len(key["bugs"])})
    count = lambda v: sum(1 for x in vs if x.get("verdict") == v)
    return found, {"false": count("false"), "plausible": count("plausible"), "minor": count("minor"), "judge": 2}


def run_case(case, vname):
    v = VARIANTS[vname]
    wt = os.path.join(os.path.dirname(os.path.abspath(o.repo)), f"bench-{case['id']}-{vname}")
    if os.path.exists(wt):
        subprocess.run(["git", "-C", o.repo, "worktree", "remove", "--force", wt], capture_output=True)
    git("worktree", "add", "--detach", wt, case["commit"] + "^")
    shutil.copytree(os.path.join(o.repo, ".administration"), os.path.join(wt, ".administration"),
                    ignore=shutil.ignore_patterns("dossiers"))
    t0 = time.time()
    run_id, cast_note = "", ""
    try:
        changes = ["--changes", "--base", case["base"]] if case.get("base") else []
        args = ["place", case["target"], "--project", wt, "--model", o.pro_model, *changes]
        if "director" in v:
            out = adm(wt, "director", "cast", case["target"], "--project", wt, "--model", v["director"], "--coins", "15m", *changes).stdout
            m = re.search(r"Director (d[0-9-]+-[a-z0-9]{4})", out)
            run_id = m.group(1) if m else ""
            if not run_id:
                raise RuntimeError("no Director run id:\n" + out[-600:])
            rd = os.path.join(o.books, "director", run_id)
            cast_note = open(os.path.join(rd, "out", "cast.md")).read() if os.path.exists(os.path.join(rd, "out", "cast.md")) else ""
            also_text = cast_note.split("also cast", 1)[1] if "also cast" in cast_note else ""
            args += ["--with", run_id]
            roster = [p["name"] for p in json.loads(adm(wt, "roster", "--json", "--project", wt).stdout)["professionals"]]
            also = [a for a in roster if re.search(r"(?<![a-z0-9-])" + re.escape(a) + r"(?![a-z0-9-])", also_text)]
            if also:
                args += ["--cast", ",".join(also)]
        elif "with" in v:
            if not v["with"]:
                raise RuntimeError("the trained variant needs --trained DIR")
            args += ["--with", ",".join(v["with"])]
        else:
            if not v["cast"]:
                raise RuntimeError("the roster variant needs --roster a,b")
            args += ["--cast", v["cast"]]
        cid = adm(wt, *args).stdout.split()[1]
        adm(wt, "wait", cid, "--timeout", "60m")
        adm(wt, "close", cid)
        status = json.loads(adm(wt, "status", cid, "--json").stdout)[0]
        cdir = os.path.join(o.books, "contracts", cid)
        findings = read_findings(cdir, [p["name"] for p in status["professionals"]])
        key = key_bugs(case["commit"])
        verdict = judge(findings, key)
        vs = verdict.get("findings", [])
        found, counts = score(vs, key)
        by_pro = {}
        for x in vs:
            if 0 <= x.get("i", -1) < len(findings):
                f = findings[x["i"]]
                by_pro.setdefault(f["pro"], []).append({"finding": f["n"], "why": x.get("why", ""),
                    "verdict": {"key": "confirmed", "plausible": "likely", "minor": "likely"}.get(x.get("verdict"), "false")})
        subprocess.run([o.bin, "verdicts", cid, "-"], input=json.dumps(by_pro), env=env, capture_output=True, text=True)
        row = {"variant": vname + o.label, "case": case["id"], "commit": case["commit"], "contract": cid, "director_run": run_id,
               "cast": [p["name"] for p in status["professionals"]],
               "kinds": [p["kind"] for p in status["professionals"]],
               "failed": {n: x.get("reason", "") for n, x in status["status"].items() if x.get("state") == "failed"},
               "bugs_in_key": len(key["bugs"]), "bugs_found": len(found), "found": found,
               **({"in_reach": len(rc), "found_in_reach": len(set(found) & set(rc))} if (rc := reach(case)) is not None else {}),
               "findings": len(findings), **counts,
               "minutes": round((time.time() - t0) / 60, 1),
               "usage_usd": round(sum(s.get("cost_usd") or 0 for s in status["status"].values()), 3)}
    except Exception as e:
        try:
            nkey = len(key_bugs(case["commit"])["bugs"])
        except Exception:
            nkey = 0
        row = {"variant": vname + o.label, "case": case["id"], "commit": case["commit"], "error": str(e)[:1500],
               "bugs_in_key": nkey, "bugs_found": 0, "findings": 0, "false": 0,
               "plausible": 0, "minutes": round((time.time() - t0) / 60, 1)}
    finally:
        subprocess.run(["git", "-C", o.repo, "worktree", "remove", "--force", wt], capture_output=True)
    with open(os.path.join(o.books, "bench.jsonl"), "a") as fh:
        fh.write(json.dumps(row) + "\n")
    print(json.dumps({k: row[k] for k in ("variant", "case", "bugs_found", "bugs_in_key", "findings", "false") if k in row}
                     | ({"error": row["error"][:200]} if "error" in row else {})), flush=True)


if o.rejudge:
    # Score existing rows again with the current judge, from the reports in
    # the books. Not while a bench runs: it rewrites bench.jsonl.
    want = set(o.rejudge.split(","))
    cases_by = {c["id"]: c for f in o.cases.split(",") for c in json.load(open(f))}
    rows = [json.loads(l) for l in open(os.path.join(o.books, "bench.jsonl"))]

    def save():
        tmp = os.path.join(o.books, "bench.jsonl.new")
        open(tmp, "w").write("".join(json.dumps(r) + "\n" for r in rows))
        os.replace(tmp, os.path.join(o.books, "bench.jsonl"))
    for r in rows:
        if r["variant"] not in want or "error" in r or r.get("judge") == 2:
            continue  # rows already judged this way are kept, so a stopped run picks up where it was
        key = key_bugs(r["commit"])
        findings = read_findings(os.path.join(o.books, "contracts", r["contract"]), r["cast"])
        found, counts = score(judge(findings, key).get("findings", []), key)
        r.update(bugs_found=len(found), found=found, findings=len(findings), **counts)
        if "in_reach" in r:
            r["found_in_reach"] = len(set(found) & set(reach(cases_by[r["case"]])))
        save()
        print(r["variant"], r["case"], len(found), counts, flush=True)
    sys.exit(0)

if o.reach_only:
    # Fill in reach for rows written before it existed. Not while a bench runs:
    # it rewrites bench.jsonl.
    cs = {c["id"]: c for c in json.load(open(o.cases))}
    rows = [json.loads(l) for l in open(os.path.join(o.books, "bench.jsonl"))]
    for r in rows:
        c = cs.get(r["case"])
        if c and c.get("base") and "found" in r:
            rc = reach(c)
            r["in_reach"], r["found_in_reach"] = len(rc), len(set(r["found"]) & set(rc))
    tmp = os.path.join(o.books, "bench.jsonl.new")
    open(tmp, "w").write("".join(json.dumps(r) + "\n" for r in rows))
    os.replace(tmp, os.path.join(o.books, "bench.jsonl"))
    sys.exit(0)

for case in cases:
    if only and case["id"] not in only:
        continue
    for vname in o.variants.split(","):
        run_case(case, vname)
board(o.books)
