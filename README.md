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
browser. Releases carry a binary for macOS and Linux (Apple silicon, ARM,
Intel and AMD):

```
curl -fsSL https://raw.githubusercontent.com/tj-techneosis/the-administration/main/install.sh | sh
```

That fetches the latest release for your machine, checks it against the
release's SHA-256 sums, puts `administration` in `~/.local/bin` and
installs Playwright's Chromium (`| sh -s -- --no-browser` skips it). To
build from source instead, clone the repo anywhere and run its
`install.sh`: a checkout builds, with Go 1.26 or newer (or mise, which
reads `mise.toml`; without either it installs the latest release instead),
and `administration upgrade` pulls and rebuilds it (set `ADMINISTRATION_SRC` if it isn't in
`~/.local/share/the-administration`). Then add the plugin in Claude Code. In VS Code, type the same
commands in the chat, or use the `/plugins` panel, then quit and reopen VS
Code:

```
/plugin marketplace add tj-techneosis/the-administration
/plugin install the-administration@tj-techneosis
```

The plugin puts a small launcher on Claude's PATH, so it finds the binary
even when the extension's shell doesn't read your profile.

**On Ubuntu 23.10 or later** (24.04 on most servers), AppArmor keeps
Playwright's Chromium from building its sandbox, and every browser user
fails at once with "Chromium sandboxing failed!" (`place` warns when it
sees this). Give Playwright's browsers a profile like the stock Chrome
one, as root, with your own home folder in the path:

```
cat > /etc/apparmor.d/playwright-chromium <<'END'
abi <abi/4.0>,
include <tunables/global>

profile playwright-chromium /home/YOU/.cache/ms-playwright/chromium{,_headless_shell}-*/**/{chrome,headless_shell,chrome-headless-shell} flags=(unconfined) {
  userns,
  include if exists <local/playwright-chromium>
}
END
apparmor_parser -r /etc/apparmor.d/playwright-chromium
```

**Upgrading** is `administration upgrade`: it installs the latest release,
or, if you built from a checkout, pulls it and builds again. The plugin's skill reads its instructions from the binary
(`administration guide`), so open sessions use the new version from their
next task, with no restart. Only a change to the plugin itself (rare) needs
`/reload-plugins`, and `upgrade` says when. A release upgrades with the
installer it came with, not whatever is on `main` today.
`administration upgrade --check` only asks: it exits 5 when a newer
release is out (or, for a checkout, newer commits), so a weekly job on
your scheduler can tell you.

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
   The contract is on a commit, and nobody works the wrong build: a room
   whose app is another commit's is turned away at the door, and critics
   read the project as it was placed. Browser professionals each get a
   room first, and the house paces itself to the building: rooms are
   made up side by side as far as there's memory for them, and a contract
   placed while every room is taken waits in the lobby, then goes up by
   itself, in turn. Nobody's purse is touched while they wait: the coins
   start when the room is ready. The Administration runs
   every stay itself, to its coins, and keeps its log.
3. **Worked.** You keep working. Claude waits in the background and checks
   in when they're back. In Herdr or tmux the watch panes show them at
   work; anywhere else, `administration watch`.
4. **Closed.** The reports are merged into one summary, each finding once
   with everyone who found it, under the build that was tested. Verifiers,
   walled like everyone else, check each against the code as it was when
   the contract was placed (the books keep a copy, so a branch that moved
   on can't clear a real finding) and record the verdict in the books,
   beside the finding; Claude reviews theirs and records where it disagrees. You hear what
   broke, how to see it and how sure we are. The rooms are cleared.
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

A browser professional reads nothing of yours, except a **handout** the project names in `roster.toml` (`[handout] files = ["house-rules.md"]`): copies of its own rule documents, put in each user's desk, the only place it can read. Personas that work from a project's rules need it; secrets never go in one, nor does the house's `env/`, and no two of its files may share a name.

**Files to upload** go on the same desk, for a user to pick in the page's file picker or drop onto it, as a person does from their own computer:

```toml
[desk]
files = ["fixtures/receipt.pdf"]   # copied from the project, or the house

[[desk.make]]                      # made to a size: real images, PDFs, text
name = "batch-{n}.jpg"
size = "5MB"
count = 20
```

Each room gets its own copies, and a user reaches them only by name (`browser_choose_file`, `browser_drop_file`): the Administration hands the page the desk's copy, so nothing else on the machine can go into it. `place` checks the desk before anyone starts, and the contract keeps it as it was then: its rooms, and a `resume` of it, get that desk, not whatever the roster and the files say later. Its rules: a name is letters, digits, dots, dashes, underscores and spaces, starting and ending with a letter or digit, and no two the same (ignoring case); a file is up to 200 MB, a desk up to 1 GB, and one `[[desk.make]]` makes up to 200 (with `{n}` in the name for more than one); a made file is at least as big as its format's smallest. A file from the project can't be a secret (by its name or its path, in any case), be in the house's `env/`, or reach outside it through a link. Every user with the browser has the desk (a screen-reader user aims a drop at an element from the page's snapshot, with the browser's own drop, as it aims everything), and so does one who sees a window equipment shows.

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
   of that name is hired. The framework's are chosen by the kind of
   project: one with a setup hook gets the web rooms (`for = "web"`), one
   with a screen to work through the device rooms (`for = "device"`), one
   with only command-line equipment neither; a project with nothing set up
   yet sees them all.

## The Director

The Director makes and keeps the professionals. It's walled like one of
them (it reads the project, writes only into its own run folder in the
books) and nothing it writes reaches the project until it passes the same
checks as a hire, and you apply it. Its jobs:

| job | does |
|---|---|
| `director cast TARGET` | specialists for one contract, from reading the code in scope, casting the roster first; `place --with RUN` puts them to work, and each one that finds something joins the roster at close. `--amend RUN` starts from an earlier cast and changes only what's new |
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
  build            builds what rooms install, once per commit (optional)
  env/             the untracked config a clean checkout needs to build (optional)
  roster.toml      existing agents that join as critics; your own open rooms
  professionals/   your hires
  candidates/      the Director's trainees, waiting for /hire
```

**A house kept off the premises.** Some projects can't carry
`.administration/` in their repo. Keep the same folder on the machine
instead, under `~/.config/administration/projects/` (or the
`projects/` of `$ADMINISTRATION_CONFIG` or `$XDG_CONFIG_HOME/administration`,
when you set them) at the path of the checkout's `origin` remote: `git@github.com:acme/shop.git` (or
`https://github.com/acme/shop`) lives at
`~/.config/administration/projects/github.com/acme/shop/`. The host is
always lowercase, and so are the owner and repo on hosts that don't tell
`Acme/Shop` from `acme/shop` (GitHub, GitLab, Bitbucket), so name the
folders that way.
`$ADMINISTRATION_PROJECTS` moves the whole shelf. Every clone and
worktree of that remote finds the same house (a project in a subfolder of
its repo, the same subfolder of it: `.../acme/shop/apps/web/`); one with its own
`.administration/` (or a worktree whose main checkout has one) always uses
that. `administration roster` says which house a checkout lives in, and
room-check and place, finding none, name the places they looked; place
also says when it's using a kept house, and for which origin, since the
origin is the checkout's own config and its setup is what runs. A kept
house is the person's to back up or share: nothing commits it.

Paths in `roster.toml` (handout files, equipment programs, a critic's
`file`) follow one rule wherever the house is: one that starts with
`.administration/` is in the house; any other is the project's, or the
house's when the project has nothing there. So a roster written for the
repo works unchanged when the house moves, and `equipment/phone/driver`
works in both.

**`setup MAIN APP`** runs as you, outside every wall, with a short
environment: `PATH`, `HOME`, your locale, mise's settings, and only the extra
variables the project names in `roster.toml` (`[setup] pass_env =
["DOCKER_HOST"]`). Your tokens don't reach it unless you pass them. MAIN is
the project the contract is on, which in a linked git worktree is the
worktree, while the hook itself is the main checkout's (a worktree shares
it): find the project's files through MAIN, not through the hook's own
folder. It must:

- build a working copy of MAIN at APP (`$ADMINISTRATION_APP`; the room it's in
  is `$ADMINISTRATION_ROOM`) without touching MAIN. The house itself is
  `$ADMINISTRATION_HOUSE`, for setup, teardown and equipment alike: reach
  the files beside the hook (a seed script, a helper) through it, never
  through `$ADMINISTRATION_MAIN/.administration`, so the hook works
  wherever the house is kept;
- start the app on the ports it's given, never on the defaults:
  `ADMINISTRATION_PORT_WEB`, `_API`, `_DB` and `_AUX` (all four are also in
  `ADMINISTRATION_PORTS`). A stack that needs more names them in
  `roster.toml`, `[setup] ports = ["idp", "s3"]`, and gets
  `ADMINISTRATION_PORT_IDP` and `_S3` the same way, claimed with the rest
  so rooms side by side never race for one. Anything it leaves running stays in its process
  group, so it's cleared at the end even if `teardown` misses it. Start a
  server with a plain `&`, not `nohup`: the hook has no terminal, and
  macOS's `nohup` then fails and the server never starts. What it starts
  outside its process group (containers, a daemon) isn't cleared that
  way: the moment it starts them, before anything can fail, it appends
  the command that stops them to `$ADMINISTRATION_CLEANUP`, one per line,
  and every teardown runs them (the last first), even for a setup cut off
  by its time limit before it wrote its manifest;
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
  whose addresses aren't this machine's and this room's ports (never the
  db port: the database is the app's, not the browser's).

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

  **Which build.** The contract is on a commit, and the manifest can say
  which one the app was built from (`"build": "<sha>"`); without it, the
  room is held to the checkout's HEAD while the setup ran. A checkout that
  moved on since the contract was placed is refused at the door.

**The build hook.** A room is only as true as what it installs. A house
whose rooms install a build (a phone app, a desktop bundle) can let the
Administration make it: **`build COMMIT OUT`** runs as you, once per
commit, in a clean checkout of that commit with the house's `env/` copied
in at the same paths (the `.env` or `local.properties` nobody commits, so a
build never ships without its map key again). It puts its build in OUT and
prints its path last. Every room of the contract, and the next contract on
the same commit, gets that one build as `$ADMINISTRATION_BUILD`, with
`$ADMINISTRATION_BUILD_COMMIT`; its setup installs it. A release check's
before app is built the same way, once for its merge base, and handed to
each room's before setup (`$ADMINISTRATION_SIDE` is `before`), so a room
only installs and starts its two apps. Uncommitted work is
built too, kept apart under its own hash. A changed hook or `env/` builds
again. The checkout has no `.git`, so a build that stamps its version uses
`$ADMINISTRATION_BUILD_COMMIT`, not `git describe`. `administration build`
tries the hook; builds stay in the books until `prune` finds them unused.
A house without one has every room build the app for itself; when that
is heavy, `status` and `room-check` say so, with the time and memory they
measured, and suggest the hook.

**The reset hook.** A room takes minutes to set up and seconds to put
back. A house's **`reset SRC APP`** runs where the setup ran, with the
room's environment, and puts the room's data back the way the setup left
it (a database loaded from a dump the setup kept, a mail catcher emptied),
leaving the servers running. With it, users a contract plans in turns
(below) hand one room on instead of each setting up their own: the
Administration resets it, clears the desk and the browser's profile
itself, checks the app still answers, and the next user starts on it at
once. It runs in the project's folder (the before checkout's, for a
release check's before app, with `ADMINISTRATION_SIDE=before`), with the
setup's environment and the name of the user handing the room on as
`ADMINISTRATION_PROFESSIONAL`, under the setup's time limit (15 minutes,
`ADMINISTRATION_SETUP_TIMEOUT`); its output goes to that user's
`reset.log` in the books. A reset that exits non-zero, runs out of time,
or leaves the app not answering within a minute is torn down as before.

**More logins.** The manifest can give a role more logins than its own:
`"logins": {"member": [{"email": "member2@example.com", "password": "..."}]}`.
Users sharing a room get one each, in turn, the role's own first; when
they run out, they share one.

`examples/demo/` is a complete house: a small app, its setup hook and two
professionals. Try it with `administration place "sign up" --cast
newcomer,reader --project examples/demo`.

## The Wall

A developer's machine holds their keys, their logins and every other
project. Each professional is a separate headless Claude Code session:

- **`--restricted`**: no shell or code-running tools at all, the person's own
  settings and hooks ignored, file tools confined to the working directory;
- **users get no file tools**, only the browser. Critics get `Read`, `Grep`
  and `Glob` in the project, with `.env` files and keys denied (in any
  case, wherever they are, and the house's `env/`), and `.git` too;
- **no MCP servers but the house's own**: the room's browser (behind the
  screen), the project's equipment it was given, and the record's server,
  which only says "Recorded." (or why a call can't be); nothing persisted
  after the session, and permission prompts answered "no" automatically;
- **the browser goes through a gate.** Every connection Chromium makes,
  loopback included, goes through a small proxy the Administration runs for
  that room, which lets through only the room's own ports on this machine.
  Other local servers, the internet, WebSockets and redirects out of the
  room all stop there, and each refusal is logged (`gate.log`). A page
  that isn't on the web (`file:`, `view-source:`, `chrome:`, `data:`) never
  reaches a proxy, so the screen in front of the browser refuses to
  navigate to one (any tool's `url`: an http or https page, or a host and
  port with no scheme, which it writes out as `http://`), and Playwright's
  own origin list sits in front as a second layer. WebRTC over
  UDP, DNS prefetch and service workers are off, Chromium's sandbox is on,
  and the profile is isolated and thrown away. Playwright's file upload,
  file drop and run-code tools are off; a user uploads or drops only the
  desk's files, by name, through the screen's own tools, which hand the
  page the room's copy.
- **the professional can't touch the app it's testing.** A room is three
  folders: `app` (the copy setup builds, which runs as you), `desk` (the
  professional's working directory, where screenshots go) and `tmp` (the
  browser's profile). The professional's tools only reach the desk: a
  file a browser tool writes (a screenshot, a snapshot) is named, never
  pathed, and the screen refuses a `filename` with a folder in it, as it
  does any argument it doesn't know that holds a folder or a path.
- **the browser server is ours**: `install.sh` puts the pinned Playwright
  MCP in `~/.local/share/administration/tools`, so nothing in a project can
  swap it.
- **rooms live outside home**, at `/Users/Shared/administration-<uid>` on
  macOS and `/var/tmp/administration-<uid>` on Linux: one per account, so two
  accounts on one machine never share. The rooms folder must be a real
  folder that's yours, not a link.
- **cleanup only touches what's provably the room's**: processes in the
  room's process groups that work in or name the room's folders. A port
  still answering after teardown is reported, not hunted down.
- **a professional inherits almost nothing** from the session that placed
  the contract: no session ids, no tokens beyond what Claude Code needs to
  sign in, and the tool servers it starts don't get even that (the house's
  take it out before starting anything; the others are given it empty).

Reports are text. Screenshots stay in the room and go with it at close
(`close --keep` keeps them). A professional files each finding with the
house's `record_finding` tool (or a `FINDING:` line), and each one goes
in its record, `findings.jsonl`, written as they come in: numbered, marked,
and the source the summary and the checks are built from. One that opens
like a check that held ("Held:", "No issue -") is kept under "Not counted"
instead: never dropped, never counted.

---

## The Books

| command | does |
|---|---|
| `administration roster [--match tags] [--json] [--project DIR]` | who's hired, what they know, the open rooms; `--match` drafts a cast |
| `administration place TARGET --cast a,b \| --with RUN [--coins 10m] [--model M] [--changes [--base REF]] [--brief NAME=TASK] [--only NAME=TASK] [--data NAME] [--project DIR] [--dry-run] [--show-browsers] [--no-watch] [--no-before] [--build SHA] [--no-queue] [--read-only] [--house-from REF]` | places a contract on the project's HEAD (and any uncommitted work) and prints its id; a room whose build is another commit's is refused, unless `--build` names that commit on purpose; `--changes` warns when the branch is missing commits the local default branch has; `--brief` gives one professional their own task on top of their persona's checks, `--only` instead of them; `--show-browsers` opens the users' browsers as windows on your screen, to watch them work. When the rooms are taken it's queued ("Queued, N ahead", exit 0) and its users start by themselves, in turn; `--no-queue` exits 4 instead; `--read-only` is for code the machine shouldn't run (see below) |
| `administration build [REF]` | the house's build hook's build of REF (HEAD by default), from the books or made now; prints its path |
| `administration wait ID [--timeout 3h]` | waits until everyone is back or out of coins, with a line as each one's state changes; returns early (2, or 3 if refused) when one fails while others still work |
| `administration status [ID] [--json]` | who's working and who's back, who's waiting for the machine (a room, the setup turn, memory) and how long each waited, and who placed it; with no ID, every open contract and a `Rooms:` line saying how many are free and how many users wait for one |
| `administration status --all [--since 7d] [--json]` | every contract placed in the window, open and closed, newest first: each professional's line as the ledger keeps it (times, room, findings and their checks, tokens) and what checking cost in dollars and tokens. For a board of the books |
| `administration close ID [--keep]` | stops anyone still working, merges the reports, puts the Director's specialists who found something on the roster and clears the rooms; prints the summary's path |
| `administration report ID [NAME]` | prints the summary once closed, or one professional's report |
| `administration sweep [--dry-run]` | stops what rooms left behind still run (their room folder and working folder both gone, no open contract owning them); `place` does it too. Linux |
| `administration room-check [--project DIR] [--data NAME] [--changes [--base REF]]` | builds one room with the setup hook, checks it comes up on seed data, shows its address and logins (not the passwords), and clears it away: the test for a hook you just wrote. `--changes` also builds the app before them, as a release check does |
| `administration resume ID NAME [--coins 10m]` | a new contract for one professional on only what contract ID left undone: what they couldn't reach, and their plan's steps never answered, or their whole part when they wrote nothing down (out of coins, failed, equipment lost). A part is resumed once; what that leaves is resumed from the new contract. On changes, it's refused when the contract's merge base is no longer under the tip it would measure from (history rewritten, another branch checked out); a branch rebased onto a newer base still resumes, and takes in what the base gained |
| `administration equipment-check NAME [--shot FILE]` | starts one piece of equipment in a fresh room, says what its setup printed, takes one picture through a screen and clears it away: room-check for equipment |
| `administration watch ID [NAME] [--panes]` | follows each professional live: every action, every finding as it's written; `--panes` gives each one a pane in Herdr or tmux (or wherever your `watch` hook puts them), which closes when they're back |
| `administration hire NAME\|FILE [--replace]` | puts a candidate on the roster |
| `administration hire --from CONTRACT NAME [--project DIR]` | keeps one of the Director's specialists from a contract by hand (one that found something joins the roster at close by itself), with the record it earned there; from a read-only contract, only into a checkout other than the one it reviewed |
| `administration verify ID [--batch 12] [--coins 10m] [--model M]` | checks a closed contract's unchecked findings in the house: walled verifiers read the project's code (nothing else, no shell) and record confirmed, likely or false for each, with the line that decided it, a corrected mark when a finding's is wrong (an OLD marked NEW), and also or not_also for each link the matcher proposed; yours replace theirs. Most are checked already: each professional's findings are checked in the background as soon as it's back, while the others work, so this waits for that and checks what's left. Every verifier is given what's checked so far, and folds the same bug worded otherwise into it instead of checking it again; first, one pass over the whole list folds the rest of those, and parts any fold it finds wrong. Nothing is folded into a finding found false. The early checks' output, a verifier that failed among it, is in the contract's `verify-early.log` (the close's verify checks again whatever it left); while one runs, `prune` says the books are busy. `ADMINISTRATION_VERIFY_EARLY=off` leaves every check to the close, and `ADMINISTRATION_MAX_VERIFIERS` (4) caps the verifiers running at once on the machine |
| `administration verdicts ID FILE\|-` | records the check of each finding, `{"name": [{"finding": 3, "verdict": "confirmed", "why": "...", "tag": "OLD", "also": ["ID NAME #N"], "not_also": [...]}]}`; `tag` refiles a finding whose mark is wrong; the summary shows each beside its finding, and `also` links it to the same bug in another contract, on both sides |
| `administration check FILE...` | checks a professional's file |

Exit codes: **0** done (a contract queued for rooms is placed, and exits
0), **1** error, **2** a room or a professional failed
(to start, or a user whose browser never loaded a page), **3** refused by
the Rules (from `place` or `resume`, 2 when someone fails in its first
seconds, 3 if any of them was refused: the contract is placed all the
same, its id printed), **4** busy: from `place --no-queue`, not enough
rooms free for this contract now (nothing was placed; the message names
the contracts holding them: place it again once their users are back,
closed or not; `administration status` says how many are free); from
`prune`, the books are in use: prune once it's done; from any command
that holds the books, a prune held them for over two minutes: run it
again. **5** from `upgrade --check` only: a newer release (or, for a
checkout, newer commits upstream) is out.

Each contract is a directory in `~/.local/share/administration/contracts/`,
with every professional's brief, settings, stream, report and status, and
the merged `summary.md`.

**Pacing.** Each browser room runs a copy of the app, and a machine has
only so much of it to give, so the house paces itself and nobody has to
pick a room count:

- **Setups in turn, side by side.** Rooms are made up in the order they
  were asked for, the app with the changes, the one before them and the
  build hook's builds alike, as many at once as the memory covers (and at
  most half the machine's processors, `ADMINISTRATION_MAX_SETUPS`). A
  professional whose room is ready starts at once. `status` says "setting
  up", or "waiting for setup (2 ahead)", or why it waits.
- **By memory, learned per house.** Each setup is measured (how far the
  machine's available memory falls while it runs, containers included),
  and kept with the house's records. A room sets up only when what's free,
  less a reserve for the machine itself (15% of it, at least 2 GB),
  covers what the house's rooms took before, counting what the setups
  under way took as taken. A house never measured the way it sets up now
  (new, or its setup or build hook changed since) sets up its next room
  alone, and is measured; the ones after it go together. One whose setup
  never comes up (or never comes up alone) is never measured, so its
  rooms keep setting up alone: `status` says why it waits. "waiting for
  memory" says how much it needs.
- **The room plan.** Which users share a room is decided for each run,
  when it's cast, because it depends on what each does to the data:
  `place --rooms "a+b, c>d>e"` puts a and b in one room at once
  (**together**: they don't collide), and c, d and e in one room one after
  another, its data put back between them by the reset hook (**in
  turns**: each uses up something the data holds once, a one-time gift, an
  invite). Anyone it doesn't name gets a room of their own (**apart**).
  `a+b>c` is a and b together, then c. Each user in a room with others is
  told they may be in the same data, so a change it didn't make isn't
  reported as the app's; each gets a login of its own while the manifest
  has any. In turns is a promise the users can wait for each other, not an
  order to: with room to spare the machine sets the next one up a room of
  its own, and when it's busy it hands the last one's on. The agent placing
  the contract proposes the plan with the cast, and says why; the
  Director writes one in its cast (`rooms:`), which `place --with` uses
  unless `--rooms` is given (`--rooms ""` places without it). `status` and
  the summary show it, and each seat's room: its own, shared, or handed on.
- **Who goes next.** Contracts go in the order they were placed, and a
  contract's users in its cast's order; a setup never passes one asked
  for before it, so a big room isn't starved by small ones. `room-check`
  and `equipment-check` go first: someone is sitting waiting for them,
  and they're over in minutes. A user that joins a room, or takes one
  handed on, doesn't need a setup turn at all.
- **A queue, not a refusal.** Up to four rooms are held at once
  (`ADMINISTRATION_MAX_ROOMS`, a hard cap). A contract placed while
  they're taken is placed all the same, waiting, and its users take rooms
  by themselves in the order contracts were placed. Only one needing more
  rooms than there are is refused (a room the plan shares counts once):
  plan more of its users together or in turns, or split it.
- **The coins start at the door.** A seat's purse starts when its room is
  ready, and a setup's 15 minutes when its turn comes, so waiting costs
  nothing; `status` and the summary say how long each seat waited.
The books grow: a phone user's stream can pass 10MB in ten minutes.
`administration prune` strips reviewed or old contracts to their summaries
(`--keep 10`, `--older 30d`), and past `--keep` (the last to close are
kept) to a line each in the project's ledger, so the stats outlive them.
Each line keeps the seat's times (started, room up, finished, in UTC), how
long it waited and how long its room took to set up, where the room came
from (its own, shared, handed on), the contract's data set and room plan,
the Administration it ran on, and what it was placed on: the commit, the
branch and, when the GitHub CLI knows one, its pull request. Enough to line
old runs up against the machine's history, and against any other record of
the same change. `status` and the
summary name the branch and pull request when it isn't the default branch. A
contract nobody has reviewed is kept whole until it's older than
`--older`. Prune is refused (exit 4) while a place, close, verify,
verdicts or resume is running, and leaves alone a build a room is
installing or running. One of those started during a prune says so and
waits for it; prune holds the books only while it looks and moves things
aside, and removes them after. `--dry-run` only looks, so it's never
refused. A contract's ledger lines are written before it's removed, and
never twice, so a prune cut off or failing loses no numbers; one that
can't remove something carries on, then names it (exit 1).

The books file a project under one name, so every clone and worktree of
it is one project, with one ledger, one build cache and one set of setup
measurements: the house's `[project] name` in `roster.toml` when it has
one, else its origin repository's name (`shop` for `github.com/acme/shop`;
`shop-apps-web` for a project in that repository's `apps/web`), else its
path. Books from before 0.7.6 filed a project with no name by its path;
its history still counts. Anything else that keeps records of the same
work can join on that name and the branch. `place --ticket SHOP-12
--requested-by alice --release v0.6.4` adds what the contract was for
(a tracker's id, who asked for it, the version it reviews) to its record,
its ledger lines, `status --all` and the summary.

Someone who keeps books on more than one machine can name each one an
**office**: `ADMINISTRATION_OFFICE`, or the first line of `office` in the
config folder (`$ADMINISTRATION_CONFIG`, else
`$XDG_CONFIG_HOME/administration`, else `~/.config/administration`). A
contract id is only unique within one machine's books, so records brought
together from several are joined on the office and the id. Each contract
records its office and the system its rooms ran on (`linux`, `darwin`)
when it's placed, on its ledger lines, in `status --all` and in the
summary. A resume is a new contract in this machine's books, so it takes
this machine's office, and names the contract it picks up from. What's
recorded is never filled in later: with no office set, none is made up,
and a contract placed before 0.7.7 has none. An office that's set but
blank, unreadable or not a short name (lowercase letters, digits and `-`,
up to 64) stops `place`, `resume`, `room-check` and `equipment-check`
before they change anything, and says where it came from.

**Read-only contracts.** A contract runs some of the project's own code as
you, outside the wall: its setup and build for a user's room, the programs
a critic `uses`, and the project's hooks (`placed`, `closed`, `verified`,
`hired`, `watch`, `director`). On a branch you haven't read, that's the
branch's code. `place --read-only` places only critics that read: it
refuses users and any critic that uses equipment, skips the project's own
hooks (the machine's, in the config folder, still run), and writes nothing
into the project, so close keeps no one-off on its roster. The steps
around it hold the same: `director cast --read-only` skips the project's
`director` hook, an amended cast of it is read-only too, `director apply`
refuses it, and a contract placed with that cast (by its id or a
candidate's own file) is read-only even without the flag; a resume of a
read-only contract is read-only, and picks a one-off up from the
contract's own file of it, never the roster; and `hire --from` a read-only
contract refuses to write into the checkout it reviewed or its house, by
any way round (keep them in one you trust, `--project DIR`) and runs no
project's `hired` hook. any Director job run in a checkout where a read-only
contract reviewed code (the whole working tree, whatever is checked out
there now) is read-only as well; `hire` of a read-only run's or
contract's own file by its path refuses that checkout too; and a read-only cast is told to cast only
critics that read. Place says that the project's hooks won't run, and each one
skipped is noted in the books' `hooks.log`. Without `--house-from`, a
read-only contract is filed under the project's origin, not under the
`[project] name` its branch's roster gives (its copy of the project too),
and neither `director review` nor `stats` takes it in: its critics came
from the branch's own house. The professionals' own files still come from the
project's house, and a branch can rewrite its own critics and hooks.
`place --house-from origin/main` takes the house (roster, professionals,
handouts, hooks) from that ref instead. Each way of naming it means one
kind of ref and nothing else, since whoever can push to the reviewed repo
can put tags and branches into your clone: `REMOTE/BRANCH` is the remote
branch (`refs/remotes/origin/main`), `REMOTE` alone is the branch its
`HEAD` points to, `refs/...` is that ref, and a full commit id is that
commit. A bare name, a short id or `HEAD` is refused with what to say
instead, and place says which ref and commit it took. The house is that
commit's own `.administration`,
unpacked once per commit into the books with the repository's filters off
and links refused, or the house kept on this machine when the ref has none
(`--house-from :kept` asks for that one); never the checkout's. A house
from a ref uses only files inside it: a critic that roster.toml gives no
file, or a file outside the house, isn't read from the checkout. Every
command that works on the contract uses that house; `hire --from` and
`missed --contract` write to the house of the project you run them in, as
ever (never a read-only contract's reviewed checkout). A resume takes the
same house, and close keeps no one-off into either. `prune` removes an
unpacked house once no contract in the books names it and it hasn't been
made or used in the last hour.

A room whose teardown never ran (its setup run by hand, then its folder
deleted with its cleanup in it) leaves its servers running, and nobody is
left to send them home. Each `place` first stops what such rooms still
run, and the summary says so: the room, how many processes, the memory
they held. Only this user's processes whose room folder and working folder
are both gone, and that no open contract owns, so a room being set up
again is never touched. `administration sweep` does the same for a timer
on a quiet machine (`--dry-run` only lists them). Linux only.

**Hooks.** The Administration tells nobody anything itself; it runs your
hooks. An executable named after an event, `placed`, `closed`, `verified`,
`hired` or `director`, in the project's `.administration/hooks/` or the machine's
`~/.config/administration/hooks/`, hears it as JSON on stdin and as
`ADMINISTRATION_*` variables (the contract, the summary's path). Post the
summary to a chat, send it to your phone, open issues from confirmed
findings: that's the hook's business. `closed` comes before the close's
own checks (the findings checked as each professional came back are in,
the last ones aren't); `verified` comes when a verify at the close is
done, with what it decided (`verdicts`) and how many verifiers didn't
finish (`verifiers_failed`), and once even when there was nothing left to
check, so a hook that reports on a run, verdicts and all, listens for
that. A verify run again later fires it again whenever it had findings to
check (`verifiers_failed` says if they didn't all finish). A hook that fails or hangs changes
nothing (except `watch`, below); its output and its exit go in the
books' `hooks.log`, and the command says in a line that it failed. A
project's hook runs in the project; your own, the machine's, runs in its
own folder, so nothing a project configures (a version manager's config
you never trusted, say) decides what its interpreter is.

A `watch` hook decides where the watch panes open, in place of the house's
own (a Herdr tab in the space you placed from, or a tmux window). It hears
JSON on stdin: `contract`, `project`, `books`, and `professionals`, each
with its `name` and the `command` that follows it and returns once it's
back. `exec` that command as a pane's last and the pane closes itself
(if watch fails, it waits for return first, so the error can be read).
Only one `watch` hook runs, the machine's before the project's (one that
isn't executable is skipped, and hooks.log says so): where the panes go is
your call. Exit 0 once they're open; on any other exit, or after 30
seconds, `place` says the panes didn't open and prints the watch line. To send every contract's panes
to a Herdr space of their own (here `swarm`, made when it's missing, since
Herdr closes a space with its last tab):

```sh
#!/bin/sh
# ~/.config/administration/hooks/watch
set -eu
space_name=swarm
ev=$(cat)
label="contract $ADMINISTRATION_CONTRACT"
space=$(herdr workspace list |
        jq -r --arg l "$space_name" '[.result.workspaces[] | select(.label == $l)][0].workspace_id // empty')
if [ -z "$space" ]; then
  # Herdr drops a space with its last tab, so make it when it's gone and
  # give its first tab to this contract.
  out=$(herdr workspace create --label "$space_name" --no-focus)
  herdr tab rename "$(printf '%s' "$out" | jq -r .result.tab.tab_id)" "$label" >/dev/null
else
  out=$(herdr tab create --workspace "$space" --label "$label" --no-focus)
fi
pane=$(printf '%s' "$out" | jq -r .result.root_pane.pane_id)
n=$(printf '%s' "$ev" | jq '.professionals | length')
# Every pane first, then each watch: one that ends at once would close its
# pane while the next is still being split from it.
panes=$pane i=1
while [ "$i" -lt "$n" ]; do
  # Split the newest pane, across then down, so no pane gets squeezed.
  dir=right; [ $((i % 2)) -eq 0 ] && dir=down
  pane=$(herdr pane split "$pane" --direction "$dir" --no-focus | jq -r .result.pane.pane_id)
  panes="$panes $pane" i=$((i + 1))
done
i=0
for pane in $panes; do
  herdr pane rename "$pane" "$(printf '%s' "$ev" | jq -r ".professionals[$i].name")" >/dev/null
  herdr pane run "$pane" "exec $(printf '%s' "$ev" | jq -r ".professionals[$i].command")" >/dev/null
  i=$((i + 1))
done
```

## Not Yet

- trial contracts for new hires, and tuning a professional from past
  contracts;
- sharing professionals between projects (a roster source other projects
  can point at);
- a container room, for projects that want a harder wall;
- one running before app shared by a contract's users, for a house whose
  users each sign in as their own seeded account and can't change what
  another sees (today each room has its own);
- vendor test keys from a team secret store, for contracts that need a real
  test environment instead of a stub.
