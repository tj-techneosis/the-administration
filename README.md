# The Administration

*The office upstairs. You place a contract; the
professionals on the roster take it, each in a room of their own, and come
back with evidence.*

The Administration tests software the way people use it. A newcomer on a
phone, staff in a hurry, someone who only uses the keyboard: each is a
**professional**, an agent trained for one kind of work. You don't run them
one by one. You place a **contract** in plain words, the **Director** finds
who on the roster is trained for it, and they take it.

```
/the-administration:redteam the sign-up flow
/the-administration:contract these changes
/the-administration:hire someone who only uses the keyboard
/the-administration:roster
```

Or just ask in conversation: *"put a contract on these changes"* or *"try
the new intake form on a phone"*.

It keeps the house rules: every stay is limited, every room is walled, and
the books are kept. It borrows a few ideas from the films (contracts,
professionals, gold coins, the Director, consecrated ground) and leaves the
rest for the tools you build next.

## Checking In

You need macOS or Linux, **the standalone Claude Code CLI** (`claude` on
PATH: professionals run it; the VS Code extension's private copy doesn't
count, though you can place contracts from the extension) and Node for the
browser. The repo carries a built binary for macOS and Linux (Apple
silicon, ARM, Intel and AMD); anywhere else it builds from source with Go
(or mise, which reads `mise.toml`).

```
git clone https://github.com/tj-techneosis/the-administration ~/.local/share/the-administration
~/.local/share/the-administration/install.sh
```

That puts `administration` in `~/.local/bin` and installs Playwright's
Chromium. Then add the plugin in Claude Code. In VS Code, type the same
commands in the chat, or use the `/plugins` panel, then quit and reopen VS
Code:

```
/plugin marketplace add tj-techneosis/the-administration
/plugin install the-administration@tj-techneosis
```

The plugin puts a small launcher on Claude's PATH, so it finds the binary
even when the extension's shell doesn't read your profile.

**Upgrading** is `administration upgrade`: it pulls your copy and installs
from it. The plugin's skill reads its instructions from the binary
(`administration guide`), so open sessions use the new version from their
next task, with no restart. Only a change to the plugin itself (rare) needs
`/reload-plugins`, and `upgrade` says when.

---

## The Rules

**1. No business on consecrated ground.** Production is never touched: not
read, not queried, not deployed to, not signed in to. A room starts from seed
data or a local copy the project's own tooling made on this machine; any
other room is refused at the door, and so is one that points anywhere but its
own ports on this machine. Professionals never run setup themselves. The
Administration builds the room and hands it over ready.

**2. Every professional works alone.** One room each: its own copy of the
app, its own ports, its own browser with nothing saved. Nobody reads another's
notes until the contract is closed.

**3. One contract, one target.** "The sign-up flow," not "the app." Narrow
contracts come back in minutes. Sprawling ones come back late and vague.

**4. Every contract is paid in coins.** A coin is a minute of wall time.
Each professional has a purse (10 by default). Professionals write each
finding down the moment they find it. When the coins run out they're shown
out mid-sentence, and everything they wrote is kept.

**5. No claim without evidence.** A finding names the URL and steps, or the
file and line. At close, each one is checked against the code before it's
reported as real.

**6. Say what you could not reach.** A professional who couldn't sign in,
hit a stub or ran out of coins says so. Silence is not a pass.

---

## How a Contract Runs

1. **Placed.** You name the target and its real scenario ("the forms,
   which customers use on their phones"). The **Director** reads the code in
   scope and the recent commits, and casts specialists for this contract:
   two to five professionals, each narrow, each with a checklist pointing at
   files, lines, screens and widths, plus anyone fitting from the roster.
   You see the cast in a few lines and add or strike names.
2. **Taken.** Each professional starts at once, in a session of its own.
   Browser professionals each get a room first. The Administration runs
   every stay itself, to its coins, and keeps its log.
3. **Worked.** You keep working. Claude waits in the background and checks
   in when they're back. There's no live feed.
4. **Closed.** The reports are merged into one summary, and each finding is
   checked against the code. You hear what broke, how to see it and how sure
   we are. The rooms are cleared.
5. **Open rooms.** If the contract needed someone nobody is, the summary
   says so and Claude offers the Director.

**On changes.** "Redteam these changes" is the everyday contract, and
most of it is the sweep around the change, not the change itself. With
`--changes` the Director reads the commits in the range and every hunk,
then maps the blast radius: everything else that uses what changed (change
what's for sale, and the register, the receipts and the reports need
sweeping). It casts sweepers for the affected areas ("does this still work
exactly as before"), users for each kind of person whose screens changed,
on their own devices, a consistency critic that holds the change to the
project's own conventions and components, and a connections critic when
anything outside or scheduled is involved. Critics get the diff, minus
`.env` files and keys.

**Later, or on a repeat.** The Administration keeps a roster; it doesn't
keep a clock. To run a contract at 7am or every Monday, have whatever
schedules things on your machine ([the
Crontinental](https://github.com/Techneosis/the-crontinental), cron, a
launchd job) run `administration place ... --cast a,b`, then `wait` and
`close`: each works from a script, with nobody at the screen. The hooks
(below) tell you when it's back.

## The Roster

**The framework hires nobody.** Every project trains its own roster, so a
new project starts empty, with a list of open rooms to fill first.

Two kinds of professional:

| kind | works | sees |
|---|---|---|
| **user** | the running app, in a browser, or the project's equipment (a command-line tool, a window, a phone) | only the room's own pages and the equipment's tools |
| **critic** | the code | the project, read-only (`Read`, `Grep`, `Glob`); with `uses`, it also runs the commands of the project's equipment, to settle what it reads |

A browser professional reads nothing of yours, except a **handout** the project names in `roster.toml` (`[handout] files = ["house-rules.md"]`): copies of its own rule documents, put in each user's desk, the only place it can read. Personas that work from a project's rules need it; secrets never go in one.

A user can also work through the project's **equipment** instead of (or as well as) the web app: a command-line tool run in the Administration's own sandbox (`server = "sandboxed"`), a desktop app's window, a phone emulator, a VM's desktop. Anything a person looks at is seen through the Administration's screen: a picture, a tap or click where something is, typing and keys, and what changed since the last look; never the device's own text of what's on it. The project hooks up what it already has (the Administration ships no VM or emulator) and declares each piece in `roster.toml`, with the hooks that start a fresh one for every room; the professional gets only its tools. See [docs/equipment.md](docs/equipment.md).

A professional is an ordinary agent file with a `administration:` block:

```yaml
---
name: newcomer
description: Someone on a phone who has never seen the app. Takes contracts on sign-up and first use.
model: haiku               # optional
administration:
  kind: user               # user | critic
  coins: 5m                # wall time, 1m to 2h
  knows: [onboarding, mobile, forms, copy]
  constraints: [phone-sized screen only]
  signs_in_as: member      # a role from the room's logins; "any" for all; leave out for none
---
Who they are and how they work...
```

`knows` is what the Director casts from. `constraints` is how it tells two
similar professionals apart.

The roster is read from:

1. **the project's hires**, in `.administration/professionals/`;
2. **agents the project already has** in `.claude/agents/`, joining as
   critics with no edits to their files. They're named in `.administration/roster.toml` with their tags:

   ```toml
   [critics.code-reviewer]
   knows = ["code-review", "typescript"]
   ```

3. **the open rooms**: suggestions the framework ships, plus your own
   (`[[open]]` entries in `roster.toml`). A room closes once a professional
   of that name is hired.

## The Director

The Director makes and keeps the professionals. It's walled like one of
them (it reads the project, writes only into its own run folder in the
books) and nothing it writes reaches the project until it passes the same
checks as a hire, and you apply it. Its jobs:

| job | does |
|---|---|
| `director cast TARGET` | specialists for one contract, from reading the code in scope, casting the roster first; `place --with RUN` puts them to work, and each one that finds something joins the roster at close |
| `director train WHO` | a new professional for the roster |
| `director onboard` | turns a project's existing agents into a roster: critics join by name, browser and command-line professionals are converted, the rest are said why not |
| `director review` | condenses what each professional learned into its dossier, proposes merges, splits, retags, retirements and new coins with the numbers behind each, and tunes the persona of whoever passed over a miss |

`director show RUN` lists what a run wrote and prints a cast's plan; `director apply RUN` takes it.

When a professional saw something and called it fine, tell the Administration:
`administration missed "no word that the list saved" --contract ID`. The
miss is kept in `.administration/missed/`, and the next `director review`
rewrites the persona of whoever should have caught it, teaching the kind of
thing rather than the one case. Its frontmatter stays as it was: a tuning
changes how it judges, never what it is or what it gets.
A specialist from a cast that found confirmed bugs is worth hiring:
`administration hire` with its file.

An ad-hoc *"pretend you're a nurse on a slow phone"* in conversation that
finds something real is a good brief for `train`. Claude will offer to make
that persona a regular.

---

## A House of Your Own

```
.administration/
  setup            builds a room (needed for browser professionals)
  teardown         takes it down (optional)
  roster.toml      existing agents that join as critics; your own open rooms
  professionals/   your hires
  candidates/      the Director's trainees, waiting for /hire
```

**`setup MAIN APP`** runs as you, outside every wall, with a short
environment: `PATH`, `HOME`, your locale, mise's settings, and only the extra
variables the project names in `roster.toml` (`[setup] pass_env =
["DOCKER_HOST"]`). Your tokens don't reach it unless you pass them. It must:

- build a working copy of MAIN at APP (`$ADMINISTRATION_APP`; the room it's in
  is `$ADMINISTRATION_ROOM`) without touching MAIN;
- start the app on the ports it's given, never on the defaults:
  `ADMINISTRATION_PORT_WEB`, `_API`, `_DB` and `_AUX` (all four are also in
  `ADMINISTRATION_PORTS`). Anything it leaves running stays in its process
  group, so it's cleared at the end even if `teardown` misses it. Start a
  server with a plain `&`, not `nohup`: the hook has no terminal, and
  macOS's `nohup` then fails and the server never starts;
- load seed data, and nothing else, and enough of it to be real: what
  professionals can't see they can't test (photos the pages show, a busy
  day's worth of records, the flows people use switched on, with a key
  made for the room when a feature needs one);
- turn off whatever calls out: scheduled jobs, webhooks, vendor URLs
  pointed at stubs;
- run the app the way production does: no debug pages, consoles or error
  pages that show the environment, and never serve the source folder (a
  professional could read `/.env` through it);
- write the room's manifest to `$ADMINISTRATION_MANIFEST`:

  ```json
  {"url": "http://127.0.0.1:41234/", "origins": ["http://127.0.0.1:41235"],
   "data": "seed",
   "roles": {"member": {"email": "member@example.com", "password": "local-only"}}}
  ```

  `origins` lists any other ports the page talks to, such as an API. The
  Administration refuses a room whose `data` isn't `seed` or `local`, or
  whose addresses aren't this machine's and this room's ports.

  **Data sets.** However the project already gets data for local testing
  (a seed script, a dump its own tooling pulled), the hook is where it
  plugs in. Declare each set in `.administration/roster.toml`:

  ```toml
  [data.orders]
  about = "seed plus a busy week of orders, two refunds and an unpaid invoice"
  ```

  `place --data orders` (or a Director plan's `data:` line) hands the name
  to the hook as `$ADMINISTRATION_DATA`, on the release check's before side
  too; `room-check --data orders` checks it. A set that seeds what the
  change itself adds (a new table, records only the new code can write)
  must skip that part where the code isn't there yet: the before side is
  the app without the change (`$ADMINISTRATION_SIDE` is `before`), so test
  for the code, not the side, and the set keeps working once the change is
  merged. What the name loads is the
  hook's business; write `"data": "local"` in the manifest for a copy of
  real data, so it's on record. Each room gets its own copy, thrown away
  with the room.

`examples/demo/` is a complete house: a small app, its setup hook and two
professionals. Try it with `administration place "sign up" --cast
newcomer,reader --project examples/demo`.

## The Wall

A developer's machine holds their keys, their logins and every other
project. Each professional is a separate headless Claude Code session:

- **`--restricted`**: no shell or code-running tools at all, the person's own
  settings and hooks ignored, file tools confined to the working directory;
- **users get no file tools**, only the browser. Critics get `Read`, `Grep`
  and `Glob` in the project, with `.env` files and keys denied;
- **no MCP servers but the room's browser**, nothing persisted after the
  session, and permission prompts answered "no" automatically;
- **the browser goes through a gate.** Every connection Chromium makes,
  loopback included, goes through a small proxy the Administration runs for
  that room, which lets through only the room's own ports on this machine.
  Other local servers, the internet, WebSockets, redirects out of the room
  and `file:` URLs all stop there, and each refusal is logged (`gate.log`).
  Playwright's own origin list sits in front as a second layer. WebRTC over
  UDP, DNS prefetch and service workers are off, Chromium's sandbox is on,
  and the profile is isolated and thrown away. File upload, file drop and
  the run-code tool are off.
- **the professional can't touch the app it's testing.** A room is three
  folders: `app` (the copy setup builds, which runs as you), `desk` (the
  professional's working directory, where screenshots go) and `tmp` (the
  browser's profile). The professional's tools only reach the desk.
- **the browser server is ours**: `install.sh` puts the pinned Playwright
  MCP in `~/.local/share/administration/tools`, so nothing in a project can
  swap it.
- **rooms live outside home**, at `/Users/Shared/administration` on macOS and
  `/var/tmp/administration-<uid>` on Linux. The rooms folder must be a real
  folder that's yours, not a link.
- **cleanup only touches what's provably the room's**: processes in the
  room's process groups that work in or name the room's folders. A port
  still answering after teardown is reported, not hunted down.
- **a professional inherits almost nothing** from the session that placed
  the contract: no session ids, no tokens beyond what Claude Code needs to
  sign in.

Reports are text. Screenshots stay in the room and go with it at close
(`close --keep` keeps them).

---

## The Books

| command | does |
|---|---|
| `administration roster [--match tags] [--json] [--project DIR]` | who's hired, what they know, the open rooms; `--match` drafts a cast |
| `administration place TARGET --cast a,b [--coins 10m] [--changes [--base REF]] [--project DIR] [--dry-run] [--show-browsers]` | places a contract and prints its id; `--show-browsers` opens the users' browsers as windows on your screen, to watch them work |
| `administration wait ID [--timeout 3h]` | waits until everyone is back or out of coins |
| `administration status [ID] [--json]` | who's working and who's back |
| `administration close ID [--keep]` | stops anyone still working, merges the reports, puts the Director's specialists who found something on the roster and clears the rooms; prints the summary's path |
| `administration report ID [NAME]` | prints the summary once closed, or one professional's report |
| `administration room-check [--project DIR] [--data NAME] [--changes [--base REF]]` | builds one room with the setup hook, checks it comes up on seed data, shows its address and logins (not the passwords), and clears it away: the test for a hook you just wrote. `--changes` also builds the app before them, as a release check does |
| `administration watch ID [NAME] [--panes]` | follows each professional live: every action, every finding as it's written; `--panes` gives each one a pane in Herdr or tmux |
| `administration hire NAME\|FILE [--replace]` | puts a candidate on the roster |
| `administration hire --from CONTRACT NAME` | keeps one of the Director's specialists from a contract by hand (one that found something joins the roster at close by itself), with the record it earned there |
| `administration check FILE...` | checks a professional's file |

Exit codes: **0** done, **1** error, **2** a room or a professional failed
(to start, or a user whose browser never loaded a page), **3** refused by
the Rules.

Each contract is a directory in `~/.local/share/administration/contracts/`,
with every professional's brief, settings, stream, report and status, and
the merged `summary.md`. Up to four browser rooms run at once
(`ADMINISTRATION_MAX_ROOMS`), because each one runs a copy of the app.

**Hooks.** The Administration tells nobody anything itself; it runs your
hooks. An executable named after an event, `placed`, `closed`, `hired` or
`director`, in the project's `.administration/hooks/` or the machine's
`~/.config/administration/hooks/`, hears it as JSON on stdin and as
`ADMINISTRATION_*` variables (the contract, the summary's path). Post the
summary to a chat, send it to your phone, open issues from confirmed
findings: that's the hook's business. A hook that fails or hangs changes
nothing.

## Not Yet

- prebuilt binaries, so nobody needs Go;
- trial contracts for new hires, and tuning a professional from past
  contracts;
- sharing professionals between projects (a roster source other projects
  can point at);
- a container room, for projects that want a harder wall;
- vendor test keys from a team secret store, for contracts that need a real
  test environment instead of a stub.
