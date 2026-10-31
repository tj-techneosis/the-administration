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

def text(t):
    say({"type": "assistant", "message": {"content": [{"type": "text", "text": t}]}})

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
        say({"type": "result", "subtype": "success", "is_error": False, "num_turns": 1, "result": answer})
