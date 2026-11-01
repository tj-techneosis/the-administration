#!/usr/bin/env python3
"""A stand-in for `claude -p` that the tests put on ADMINISTRATION_CLAUDE.

It records how it was called next to the persona file, then behaves as the
persona's body says: a line "FAKE: <mode>" picks the mode.
  ok         writes a finding and a report (the default)
  slow       writes a finding, then never finishes (for coins and close)
  crash      exits 1 with no report
  error      ends with an error result
  nobrowser  every browser call fails (a user whose browser won't start)
  noload     the browser answers, but no page is ever loaded
  release    findings marked NEW, CHANGE and OLD, as on a release check
  plan       a plan of two, answers one; answers the other when sent back
  retag      a NEW finding the code had before, and one marked "New:"
  old        retag's first finding, marked OLD
  partial    a plan of four, one covered and the rest unreached
  record     keeps its record with the house's tools, not FINDING: lines

With --input-format stream-json the brief comes as the first message on
stdin; each later message (the Administration sending it back) is recorded
in fake-sent-back.jsonl and answered with another closing event.
"""
import json, os, sys, time

args = sys.argv[1:]
persona = args[args.index("--append-system-prompt-file") + 1]
here = os.path.dirname(persona)
body = open(persona).read()
mode = "ok"
for line in body.splitlines():
    if line.startswith("FAKE:"):
        mode = line.split(":", 1)[1].strip()

streaming = "--input-format" in args and args[args.index("--input-format") + 1] == "stream-json"
def message_text(line):
    m = json.loads(line)
    return "".join(c.get("text", "") for c in m["message"]["content"])
prompt = message_text(sys.stdin.readline()) if streaming else args[args.index("-p") + 1]

with open(os.path.join(here, "fake-call.json"), "w") as f:
    json.dump({"argv": args, "prompt": prompt, "cwd": os.getcwd(), "env": dict(os.environ), "pid": os.getpid()}, f)

def say(obj):
    print(json.dumps(obj), flush=True)

if os.path.basename(persona) == "verifier.md":
    # A verifier: walled to reading code. The save button is confirmed, the
    # error is false; anything else it leaves out.
    assert args[args.index("--tools") + 1] == "Read,Grep,Glob", "a verifier only reads"
    data = json.loads(prompt)
    assert data["project"] in args, "it can read the project"
    out = []
    with open(os.path.join(os.getcwd(), "..", "..", "fake-verifier-seen.jsonl"), "a") as seen:
        seen.write(json.dumps(data) + "\n")
    for f in data["findings"]:
        a = None
        if f.get("already_checked"):
            a = {"id": f["id"], "verdict": f["already_checked"], "why": "as before"}
        elif "save button" in f["text"]:
            a = {"id": f["id"], "verdict": "confirmed", "why": "settings.js:12 never calls save()"}
        elif "error says" in f["text"]:
            a = {"id": f["id"], "verdict": "false", "why": "errors.js:4 shows the message"}
        elif "$5 too high" in f["text"]:
            # Marked NEW, but the code was so before the changes.
            a = {"id": f["id"], "verdict": "confirmed", "why": "cart.js:8 added it before the change", "tag": "OLD"}
        elif "prints twice" in f["text"]:
            a = {"id": f["id"], "verdict": "confirmed", "why": "receipt.js:3 prints in both handlers", "tag": "new"}
        # A proposed link: the same when the words are the same.
        for m in f.get("maybe_same_as", []):
            if a is None:
                continue
            a.setdefault("also" if m["text"] == f["text"] else "not_also", []).append(m["ref"])
            a["link_why"] = "same words, same code"
        if a:
            out.append(a)
    say({"type": "result", "subtype": "success", "is_error": False, "num_turns": 3, "result": "Checked.\n" + json.dumps(out)})
    sys.exit(0)

if os.path.basename(persona) == "matcher.md":
    # The finding matcher: pairs whose words are the same, and nothing else.
    # What it was given is kept beside the books, for the tests to read.
    data = json.loads(prompt)
    assert args[args.index("--tools") + 1] == "", "the matcher has no tools"
    contract = os.getcwd()
    while not os.path.exists(os.path.join(contract, "contract.json")):
        contract = os.path.dirname(contract)
    with open(os.path.join(contract, "fake-matcher-seen.jsonl"), "a") as f:
        f.write(json.dumps(data) + "\n")
    pairs = [{"these": t["ref"], "others": o["ref"], "why": "same words, same page"}
             for t in data["these"] for o in data["others"] if t["text"] == o["text"]]
    # A model thinking aloud: no reason, so no link.
    pairs += [{"these": t["ref"], "others": o["ref"], "why": "Hmm"}
              for t in data["these"] for o in data["others"] if t["text"] != o["text"]]
    pairs.append({"these": "nobody #9", "others": "zzzz nobody #9", "why": "made up"})  # refs that don't exist are dropped
    say({"type": "result", "subtype": "success", "is_error": False, "num_turns": 1, "result": json.dumps(pairs)})
    sys.exit(0)

def text(t):
    say({"type": "assistant", "message": {"content": [{"type": "text", "text": t}]}})

def record(tool, **inp):
    say({"type": "assistant", "message": {"content": [{"type": "tool_use", "id": "r" + tool, "name": "mcp__house__" + tool, "input": inp}]}})

def browser(ok, tool="browser_navigate", id="t1"):
    say({"type": "assistant", "message": {"content": [{"type": "tool_use", "id": id, "name": "mcp__playwright__" + tool, "input": {}}]}})
    say({"type": "user", "message": {"content": [{"type": "tool_result", "tool_use_id": id, "is_error": not ok,
         "content": [{"type": "text", "text": "done" if ok else "Error: Socket directory path is too long"}]}]}})

say({"type": "system", "subtype": "init"})
if mode == "noload":
    browser(True, "browser_tabs", "t1")
    browser(True, "browser_close", "t2")
else:
    browser(mode != "nobrowser")
if mode == "crash":
    print("boom", file=sys.stderr)
    sys.exit(1)
if mode == "write":
    os.makedirs("outbox", exist_ok=True)
    notes = sorted(os.listdir("inbox")) if os.path.isdir("inbox") else []
    with open("outbox/tasks.md", "w") as f:
        f.write("# Tasks\n" + "".join(f"- from {n}\n" for n in notes))
if mode == "hoard":
    # Reads and reads, writes nothing down, and never finishes on its own:
    # what it's sent is kept, for the tests to see.
    for line in sys.stdin:
        if line.strip():
            with open(os.path.join(here, "fake-sent-back.jsonl"), "a") as f:
                f.write(json.dumps(message_text(line)) + "\n")
    sys.exit(0)
text("Looking around.\n**FINDING:** the save button does nothing on /settings\n")
if mode == "slow":
    text("UNREACHED: the billing page, ran out of time")
    while True:
        time.sleep(1)
if mode == "error":
    say({"type": "result", "subtype": "error_max_turns", "is_error": True, "result": ""})
    sys.exit(1)
if mode == "release":
    text("FINDING: NEW: the total on /cart is $5 too high since the change\nFINDING: OLD: the logo is blurry on both\nFINDING: CHANGE: the comment on total() still says tax is added later")
if mode == "retag":
    text("FINDING: NEW: the total on /cart is $5 too high since the change\nFINDING: New: the receipt prints twice after paying")
if mode == "record":
    record("record_plan", steps=["the settings screen", "the profile screen"])
    record("record_finding", what="the export downloads an empty file; expected the orders in it", where="/orders, Export",
           mark="NEW", evidence="orders.csv is 0 bytes")
    record("record_step", step=1, done=True)
    record("record_finding", what="the photo upload hangs at 99 percent", where="/profile")
    record("record_step", step=2, done=False, why="the profile page needs a photo")
    record("record_unreached", what="billing", why="no link to it")
    # A restatement in prose at the end is folded into the recorded one.
    text("What broke:\nFINDING: NEW: the export downloads an empty file; expected the orders in it (at /orders, Export)")
if mode == "partial":
    text("PLAN 1: the upload screen\nPLAN 2: a batch of files\nPLAN 3: a dropped link\nPLAN 4: a reload mid-upload\n"
         "COVERED 1\nUNREACHED: 2 no way to make a file\nUNREACHED: 3 no file to upload\nUNREACHED: 4 no file to upload")
if mode == "old":
    text("FINDING: OLD: the total on /cart is $5 too high since the change")
if mode == "plan":
    text("PLAN 1: the settings screen\nPLAN 2: the profile screen\nCOVERED 1")
text("FINDING: the save button does nothing on /settings\nFINDING: the error says 'undefined'")
say({"type": "result", "subtype": "success", "is_error": False, "total_cost_usd": 0.0125, "num_turns": 7,
     "usage": {"input_tokens": 10, "cache_read_input_tokens": 900, "cache_creation_input_tokens": 90, "output_tokens": 300},
     "result": "Tried settings and profile. Settings is broken.\nFINDING: the error says 'undefined'\n\nUNREACHED:\n- billing, no link to it\n- 2. logout, not found\n\nThat's all."})
# Sent back: answer each message with another closing event, until stdin ends.
if streaming:
    for line in sys.stdin:
        if not line.strip():
            continue
        sent = message_text(line)
        with open(os.path.join(here, "fake-sent-back.jsonl"), "a") as f:
            f.write(json.dumps(sent) + "\n")
        answer = "COVERED 2" if mode == "plan" and "PLAN 2" in sent else "Nothing more."
        # Each answer's own usage, as claude reports it: only that answer's.
        say({"type": "result", "subtype": "success", "is_error": False, "num_turns": 1, "result": answer,
             "usage": {"input_tokens": 5, "cache_read_input_tokens": 50, "output_tokens": 20}})
