# Design

The plain-language version of the README.

## What it is

A CLI (`administration`) and a Claude Code plugin. The CLI does everything
that has to be exact: who is on a roster, building rooms, starting the
agents with their limits, timing them, collecting what they write, cleaning
up. The plugin is the conversation: it turns "put a contract on these
changes" into CLI calls, and checks findings against the code afterwards.

## Pieces

- **Roster**: agent files with a `administration:` block, read from
  the house's `professionals/` (the project's `.administration/`, or the
  one kept on the machine for its origin), plus the project's existing
  agents named in its `roster.toml`. Each has `knows` tags. `roster --match` ranks
  them against a contract's tags and lists the tags nobody covers, which is
  how gaps (and so hires) are found. A broken file is left off and reported,
  so one bad file doesn't stop contracts.
- **Contract**: a directory in the books. `place` writes it, copies each
  professional's persona into it (later edits don't change a placed
  contract) and starts one worker per professional in its own session, so
  the workers outlive the tool call that placed them.
- **Worker** (`administration _work ID NAME`): for a browser professional,
  builds the room by running the project's setup hook with free ports, reads
  the manifest and refuses it unless it is seed or local data (a data set
  the project declares, `--data`, handed to the hook) on this room's own
  local ports, waits for the URL to answer, then runs the professional.
  Afterwards it runs teardown and kills what's left in the setup's process
  group. It records everything in `status.json`.
- **Room**: `rooms/<contract>/r<N>/` (a short slot, not the name: the
  browser's socket lives under it, and socket paths can't be long) with `app/` (setup builds the app
  here; it runs as the person), `desk/` (the professional's working
  directory and Playwright's only writable place) and `tmp/` (TMPDIR, so the
  browser's throwaway profile lives in the room and its processes are
  recognisable at cleanup).
- **Gate** (`proxy.go`): an HTTP proxy per browser professional, opened by
  the worker. Chromium is launched with it as its proxy and with
  `<-loopback>` bypass removed, so even 127.0.0.1 goes through it. It dials
  only 127.0.0.1 and only the room's ports, whatever host name a request
  carries, and logs every refusal. Playwright's `allowedOrigins` stays as a
  first filter, but it isn't a boundary: it misses WebSockets and redirects.
- **Professional**: `claude -p` with `--restricted`, `--strict-mcp-config`,
  `--no-session-persistence`, `--permission-prompts none`, stream-json
  output, the persona as an appended system prompt, and a generated settings
  file. Users: no built-in tools, Playwright only (an allowlist of its
  tools), working directory = the room. Critics: `Read,Grep,Glob`, working
  directory = the project, the diff handed in with `--add-dir`.
- **Cleanup** (`procs.go`): signals only processes in the room's recorded
  process groups that work in or name the room, plus anything whose command
  line names the room or the professional's books. Marks must be paths
  strictly inside the books or rooms folders; anything else is dropped, so a
  bad mark can only mean killing less. (An empty room once made the mark
  "/", which matched every process and ended a desktop session.) PIDs can
  be reused, so a recorded group is never signalled on its number alone.
- **Sandboxed equipment** (a user's command-line tool): `--tools Bash` (plus
  `Read` with a handout) with a `Bash(cmd)` and `Bash(cmd *)` allow rule per
  listed command (a relative path resolved inside the room's
  `equipment/<name>/`, built by its setup and made read-only), and Claude
  Code's sandbox: `autoAllowBashIfSandboxed`
  off (otherwise any sandboxed command runs without a rule),
  `allowUnsandboxedCommands` off, `failIfUnavailable` on (otherwise it runs
  bare if the sandbox can't start), no allowed domains, `denyRead` on home.
  Commands can write the desk, nothing outside the room, and not the tool's
  own folder.
  Shells, interpreters, network tools and anything that runs other commands
  are refused in `commands`; that list is a first wall, not the wall (a tool
  with plug-ins or delegates, like ImageMagick, can still run things, inside
  the sandbox). The sandbox also denies reading `/run/user` (the session's
  sockets), container sockets, `/tmp`, other rooms, `/mnt` and `/media`, and
  writing the room's build and inbox; the build is made read-only after
  setup, and refused if it links out of the room. `git` and `cd` are denied
  outright. Claude Code's own read-only built-ins (`ls`, `cat`, `id`...)
  still run, inside the same sandbox.
- **The release check** (a contract on `--changes` with users): each
  user's room also gets the app as it was before the change, at the merge
  base, built by the same setup hook from a clone in the room that shares
  the project's objects (`git clone --shared`; the project's repo is only
  read). The hook gets `ADMINISTRATION_SIDE=before`, `ADMINISTRATION_MAIN`
  pointing at the clone and `ADMINISTRATION_PROJECT` at the project, and
  the same data set: a set that seeds the change's own new records must
  skip them where the code doesn't exist yet. The
  user runs each flow on both and marks findings `NEW:` (the change caused
  it) or `OLD:`; the gate lets both apps through. `--no-before` skips it.
  Critics have a third mark, `CHANGE:`, for the change's own text being
  wrong (a comment, document or test) with nothing behaving differently;
  NEW needs the before shown. The summary leads with NEW, then CHANGE,
  then OLD.
- **Handout**: `[handout] files` in `roster.toml` are copied into each
  user's desk (inside the project, regular files, no secret names, 256 KB
  in all), and the user gets `Read`, allowed for its desk only; anything
  outside its working directory asks, and with no one to answer it's
  refused (live probe 2026-10-25: the app's `.env.local`, its source,
  `/etc`, the books and other rooms were all refused).
- **Coins**: a wall-clock limit. At the limit the professional's process
  group gets SIGTERM, then SIGKILL after 10 s. Findings are parsed from the
  stream as they arrive (`FINDING:` and `UNREACHED:` lines), so a kill loses
  only the closing prose.
- **The brief** names everyone else on the contract, with their own task if
  they have one, and asks each to keep to their part: the target is shared,
  the work isn't. `--brief` adds a task to the persona's own checks;
  `--only` replaces them, for a smoke test that shouldn't grow into a run.
  `resume` is `place` with `--only` the professional's unreached items and
  unanswered plan steps, and the persona exactly as it was placed (not as
  its file says today).
- **Lost equipment**: the screen's server counts screenshots that fail or
  aren't a picture (a bad tap or an unknown key is the professional's, not
  the device's). At three in a row it answers every call with "lost: stop,
  write what's left as unreached", and writes the time to the room, where
  the worker records it in the status before teardown.
- **Wait** prints a line when someone's state changes, and returns early
  when one fails or is refused while others work, so a room that broke in
  a second is heard of then, even one that happened before it started. A
  failure it has returned for once (kept in the contract's `.told`) doesn't
  end a later wait again.
- **Watch**: reads the same streams from the books and prints each action,
  the first line of what was said, and every marked line, until everyone
  is back. It only reads, so watching can't change the work. `--panes`
  opens a Herdr tab or tmux window with one `watch` per professional, and
  `place` does that by itself, unfocused, when it runs inside one
  (`--no-watch` or `ADMINISTRATION_WATCH=off` doesn't); elsewhere it prints
  the watch line. The Herdr tab goes in the placer's space, as Herdr
  says it is now (`herdr pane current`; `HERDR_WORKSPACE_ID` is only where
  the pane started), not whichever one has focus, and each pane runs
  watch as its shell's last command, so it closes once its professional is
  back (a watch that fails waits for return, so the error is seen). Where
  panes go is a matter of taste, so a `watch` hook replaces all of this: it
  hears the contract and each professional's watch command, and opens them
  however it likes. Only one runs, the machine's before the project's, since
  two would open everything twice and the placement is the person's choice.
  `place --show-browsers` opens the users' browsers as windows instead of
  headless: the browser gets the screen's variables (`DISPLAY`,
  `WAYLAND_DISPLAY`, `XAUTHORITY`, `XDG_RUNTIME_DIR`), so it can draw there.
  The professional still has only the browser tools, so it can't see
  anything else on the screen.
- **Close**: SIGTERM to any worker still running (it records `closed` and
  tears down), a merged `summary.md` with the open rooms at the end, rooms
  removed, and the open rooms as they are then kept with the contract, so a
  summary written again later lists those. The summary lists each finding
  once: the same claim from several
  professionals (or one, twice) is folded into one line naming each, when
  most of the words match, or more than half do and they cite the same lines
  of a file. The reports follow without their lists, except for findings
  folded into another's, kept in their own words so a wrong fold loses
  nothing; their closing prose loses what it restates (the plan and its
  answers, findings marked or under a heading like "What broke"), which
  leaves what held: the evidence of what was looked at. Each professional's
  own report keeps every word. `verdicts` writes it again with each check beside its finding,
  confirmed first and false last.
- **Checks** (`room-check`, `equipment-check`) build a room under a
  contract of their own and clear it away. Killed outright, they can't, so
  each writes its pid; the next `status` or `place` clears a check whose
  process is gone, room and all.
- **Verify** (`verify.go`): after close, the summary's unchecked findings
  go in batches (12 by default) to verifiers, four at once. A verifier is
  `claude -p` walled as the Director is: `Read`, `Grep` and `Glob` only,
  the project and the contract's patch added, a deny list for the shell,
  the web, writing and secrets. Its prompt (`house/verifier.prompt`) has it
  trace each finding in the code and answer confirmed, likely or false with
  the line that decided it. The verdicts go into each professional's
  `verdicts.json` marked `by: verifier`, under every name a folded finding
  carries; a verdict the session recorded is never replaced, and one it
  records later replaces the verifier's. A verifier that fails keeps its
  folder (`.verify/N`) and its findings stay unchecked for the next run.
  One verify runs at a time per contract (a lock in its folder). `stats`
  says how many of its checks are a verifier's that nobody has reviewed
  since, so the numbers don't pass for a person's.
- **Links between contracts** (`links.go`): at close, contracts on the same
  work (the same project and merge base, or the same target, within a
  week) are found, and one small model call (Haiku, read-only) proposes
  pairs of findings that are the same bug. A critic's `cart.ts:88` and a
  user's "the total was $5 high" share no words, so it can't be done by
  matching text. Proposals are shown as proposed; the session confirms or
  rejects each with `verdicts` (`also`, `not_also`), which writes the link
  into both contracts and the decision, with both findings' words, into the
  project's log in the books. The matcher's next call gets the latest of
  those as examples, so it learns the project's vocabulary, and `stats`
  shows how many of its proposals held. `ADMINISTRATION_MATCH=off` skips
  it.
- **Findings that aren't**: a `FINDING:` line that opens with "Held:",
  "Plan 3:", "No issue -" and the like is kept under "Not counted" in the
  report, not counted and never dropped; the word needs a colon or dash
  after it, so "the OK button" and "the plan page" still count. Halfway
  through the purse (if nothing's written yet) and near its end, the
  professional is asked to write down what it has, without being told why:
  the limit throws away findings kept for the end.
- **Workers**: started directly by `place`, each in a session of its own,
  so they outlive the shell or tool call that placed the contract. The
  worker resets PATH to the placer's.
- **No clock**: the Administration schedules nothing. A contract at a time
  or on a repeat is the person's scheduler running `place`, `wait` and
  `close`, which all work from a script with nobody at the screen; the
  event hooks say when it's back. A roster and a scheduler are two tools,
  and skills are built on each.

## Why these choices

- **Separate `claude -p`, not subagents.** Subagents share the parent
  session's permissions and sandbox, and a developer's session may run in a
  permissive mode. A separate process gets its own limits, set by us.
- **`--restricted` over a deny list.** Prefix deny rules are beaten by
  `bash -c`, `node -e` or a script. Taking the shell away entirely is the
  only rule that can't be talked around. Users don't need it, because the
  browser is their only interface. Critics only read.
- **Rooms outside home.** File tools are confined to the working directory,
  and the browser to its origins, but a room inside home would sit next to
  everything worth protecting. Outside, a mistake in one rule still leaves
  nothing of the person's in reach.
- **Setup gets a short environment.** The app it starts can be made to show
  its environment (debug pages, error pages), so a token in it would be one
  page away from a professional. Projects name what else it needs.
- **The setup hook runs as the person.** Building a stack needs Docker, the
  network and the project's tools, which professionals must never have. So
  the Administration does it, and holds the result to the Rules before
  anyone enters.
- **Seed data only.** The manifest declares the data, and anything but
  `seed` is refused. It's a declaration, so the hook is trusted to tell the
  truth; the README asks for it in plain words. Equipment is held to the
  same: its setup's printed addresses must be on this machine (10.0.2.2 is
  this machine to an Android emulator) and any `DATA=` seed or local, or
  the shared test servers the equipment declares (`shared`, never a host
  that names production) with `DATA=shared`. Professionals on one are told,
  and the summary marks what only they saw. The house can't see inside a
  built app, so the hook is trusted here too, and the `shared` line is the
  team's word, in a file they review, that the server isn't production.
- **Findings as marked lines.** A professional killed at the coin limit
  can't write a closing report, so the report is built from the stream.
- **A screen, not a text of the screen.** Users are there to stand in for
  people, and a page's element list says what a control is even when it
  doesn't look like one. So a user who sees the screen gets pictures,
  coordinates and a keyboard, in the browser, a desktop window or a device,
  and the Administration owns that screen; a project only writes the small
  driver for its own device.
- **Misses tune the persona, by hand.** A professional that saw a problem
  and called it fine needs its judgment changed, not more time. The person
  records the miss; the Director rewrites the persona to catch the kind of
  thing, keeping everything else about it; and the person applies it. A
  tuning can't change what a professional is or what it gets.
- **A specialist who found something stays.** Writing one costs the
  Director minutes of reading; the next contract on the same code needs the
  same people. So each specialist the Director wrote that found something
  joins the roster when its contract closes, and the Director casts from the
  roster before writing anyone. Letting one go is deleting its file; the
  Director's review proposes who to retire.
- **Empty roster.** Personas are specific to a product and its people, so
  the framework ships open rooms, not professionals, and the Director trains
  them in the project.
- **A house can be kept on the machine.** The house is normally the
  project's `.administration/`, committed so the team shares it. A project
  that can't carry it keeps it under `projects/<host>/<owner>/<repo>/` in
  the config folder, found by the checkout's `origin` remote, so a fresh
  clone or a new worktree finds it where a path-keyed one would be lost.
  The project's own folder always wins, so nothing changes for a project
  that has one. Every reader and writer goes through one lookup
  (`houseOf`), hooks get the result as `ADMINISTRATION_HOUSE`, and
  `roster.toml` paths keep one rule (`.administration/...` is the house;
  anything else the project's, then the house's), so a roster moves without
  edits. The wall doesn't move: the house is read and run by the
  Administration, outside every room, as before; only a handout's copies
  reach a desk.

## Usage data

Every professional's `status.json`, every ledger line and every Director
run's `run.json` keep what claude reports at the end of a session: `turns`,
`tokens_in`, `tokens_out` and `cost_usd` (Claude Code's estimate at list
prices; absent when a professional was shown out before it could report).
Nothing shows them: the budget a person sees is coins, which are time. Anyone
who wants to track usage can read them from `status --json`, `stats --json`
or the ledgers in `ledgers/`.

## Environment

| variable | default | for |
|---|---|---|
| `ADMINISTRATION_HOME` | `~/.local/share/administration` | the books |
| `ADMINISTRATION_ROOMS` | `/Users/Shared/administration`, `/var/tmp/administration-<uid>` | rooms; must be outside home |
| `ADMINISTRATION_CLAUDE` | `claude` | the claude binary |
| `ADMINISTRATION_MAX_ROOMS` | 4 | browser rooms at once |
| `ADMINISTRATION_SETUP_TIMEOUT` | 15m | the setup hook's limit |
| `ADMINISTRATION_TOOLS` | `~/.local/share/administration/tools` | the pinned Playwright MCP, from `install.sh` |
| `ADMINISTRATION_CONFIG` | `~/.config/administration` | the machine's own professionals and hooks |
| `ADMINISTRATION_PROJECTS` | `$ADMINISTRATION_CONFIG/projects` | houses kept on the machine, at `<host>/<owner>/<repo>` of each project's origin |
| `ADMINISTRATION_POLL`, `_CLOSE_GRACE`, `_COIN` | 2s, 30s, 1m | test knobs |
| `ADMINISTRATION_TEST_ISOLATED`, `_TEST_UNSAFE` | | the suite runs only inside `tests/isolated.sh` (or in a throwaway account) |
