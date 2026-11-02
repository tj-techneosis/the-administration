# Equipment

First build, 2026-10-25. What a professional uses to reach the thing it tests,
when that thing isn't a web app or a command-line tool: an Android
emulator, a desktop app, a VM. How a project declares it, how the
Director casts with it, and where the wall sits.

## The line

The Administration manages professionals: contracts, the Director, rooms,
the wall around each professional, the books, watching. It doesn't own
the equipment. An emulator, a VM, a container, a desktop session: those
are whatever the person already uses, and the project declares them in
one place. The Administration starts the equipment for each room, hands
the professional the equipment's controls and nothing else, and clears it
up after.

That keeps the framework small and keeps it from guessing at anyone's
setup. The two built-in kinds stay: the browser (Playwright, through the
gate) and the command line (named programs in an OS sandbox). Equipment
is the third way in, and it's the project's.

## Declaring it

In the house's `roster.toml` (`.administration/roster.toml`, or the one kept
on the machine: README, A House of Your Own), one table per piece of
equipment. Program paths starting `.administration/` are the house's,
wherever it is:

```toml
[equipment.desktop-app]
# What it reaches and how to use it, in a sentence or two. The Director
# reads this to cast; the professional reads it in its brief.
about = "The app's own window (Electron), driven like a browser: snapshot, click, type, screenshot."
# Per room: start it, and stop it. The setup prints KEY=VALUE lines, and
# {KEY} in args is replaced with them; {room} and {port} are always there
# (a fresh port the equipment may listen on).
setup = ".administration/equipment/desktop-app/start"
teardown = ".administration/equipment/desktop-app/stop"
server = "playwright"     # the pinned browser server, "sandboxed", "commands", "screen", or a program in the project
args = ["--cdp-endpoint", "{CDP}"]
tools = ["browser_snapshot", "browser_click", "browser_type", "browser_take_screenshot"]
show_args = []            # added when the contract is placed with --show-browsers

[equipment.cli]
about = "The tool, built for this room; run it on the files in your desk."
setup = ".administration/equipment/cli/build"   # builds it into $ADMINISTRATION_EQUIPMENT_DIR
server = "sandboxed"      # a shell allowed only these, in the OS sandbox
commands = ["bin/the-tool", "wc", "file"]

[equipment.phone]
about = "An Android phone, fresh for you, with the app open. Tap, swipe, type, Back and Home."
setup = ".administration/equipment/phone/start"   # boots it, installs the build, prints SERIAL= and BUILD_COMMIT=...
teardown = ".administration/equipment/phone/stop"
restart = ".administration/equipment/phone/restart"  # once, before the device is called lost: boot it again, reinstall
server = "screen"         # the house's screen, in front of the project's driver
driver = ".administration/equipment/phone/driver"
args = ["{SERIAL}"]
keys = ["Back", "Home", "Enter", "Tab", "Backspace"]

[equipment.linux-vm]
about = "A throwaway Linux VM with the tool installed; run it there with run_run."
server = "commands"       # the house's command server
commands = [".administration/equipment/linux-vm/run"]

[equipment.db]
about = "Read this room's own database: one SELECT per call."
setup = ".administration/equipment/db/start"   # prints DB=<the room's database>
server = "commands"
commands = [".administration/equipment/db/query"]
needs_app = true          # its setup reads the app's room: a browser user's only, never a critic's
```

- **`server`** is a tool server (MCP) the professional talks to, with
  only the listed `tools` allowed. `playwright` is the one install.sh
  pinned, and only the tools a browser professional may have are allowed
  on it. A program in the project is the project's own server (a phone
  driver, say).
- **`server = "sandboxed"`** is the Administration's own wall: the
  professional gets a shell allowed only the listed `commands`, each run in
  Claude Code's OS sandbox, with no network, nothing of the person's
  readable, and nothing run unsandboxed (if the sandbox can't start, nothing
  runs). A relative command is one inside `$ADMINISTRATION_EQUIPMENT_DIR`
  (`<room>/equipment/<name>/`), where the setup builds the tool; the folder
  is made read-only before the professional starts, and a link out of it
  refuses the room. Never a shell, an interpreter or anything that runs
  another command. It's the default for a command-line tool: the project
  writes no wrapper. Only users take it.
- **`server = "commands"`** is the house's command server: one tool per
  program in `commands` (`run_<name>`, from the file's name, so two
  programs in one piece need two names), which runs that program with the
  arguments given as a list, never through a shell, in the room's desk,
  for up to 10 minutes a call (then it and everything it started in its
  process group are stopped), and without Claude Code's own sign-in in
  its environment.
  It runs outside the professional's sandbox, which is the point: a
  wrapper that boots a VM or talks to Docker can't work inside a sandbox
  with no network and no sockets. So the program must be its own wall
  (the VM, or a sandbox it sets up), and a sandboxed tool is refused the
  wrappers it allows here, because the person declared them in their own
  project. The Director can
  use equipment; it can't make any.
- **`server = "screen"`** is the house's screen, for anything a person
  looks at: a phone, a VM's desktop, a native window. The project writes a
  small `driver` for its device; the professional gets the screen's tools
  (`screen_take_screenshot`, `screen_look`, `screen_click`, `screen_drag`,
  `screen_scroll`, `screen_type_text`, `screen_press_key`, `screen_wait`)
  and nothing of the device's own: no list of what's on it, no text of it,
  only the picture. `screen_look` says where the picture changed since the
  last look, so "did anything happen?" has an answer a person would have.
  The screen scales big screens down for the professional and puts its
  taps back on the device's pixels. The driver is called with the
  equipment's `args` and one action:

  | action | does |
  |---|---|
  | `screenshot` | a PNG of the whole screen on stdout |
  | `click X Y [HOLD_MS]` | a tap or click, held that long when given |
  | `drag X1 Y1 X2 Y2` | press, move, let go (a swipe) |
  | `scroll X Y DX DY` | scroll at that point; DY > 0 is down |
  | `type TEXT` | type into what has focus |
  | `key NAME` | one key, from the equipment's `keys` |

  Coordinates are the device's own pixels; a non-zero exit is a failure
  and its stderr goes back to the professional. The driver runs as the
  person, outside the wall (without Claude Code's sign-in), with the
  project's `pass_env` and every piece's setup lines as `EQ_<EQUIPMENT>_<KEY>`. For Android it's adb
  (`screencap`, `input tap`, `input swipe`, `input text`,
  `input keyevent`); for a VM or a virtual display, a VNC or X client.
  `examples/phone-demo` is a whole one.
- **A window through `playwright`** is seen the same way by a user who sees
  the screen (the default): the browser's screen goes in front of it,
  with the screenshots, coordinate clicks and keyboard, and no text dump
  of the page. A `sees: screen-reader` user gets the declared tools,
  behind the same screen as a screen-reader user's browser: files named,
  never pathed, and only web pages.
- **`setup` and `teardown`** run once per room, as the person, like the
  house's setup hook: a fresh emulator, VM or app per professional, so two
  can't step on each other. Their environment has `ADMINISTRATION_ROOM`,
  `ADMINISTRATION_PORT`, `ADMINISTRATION_EQUIPMENT`, `ADMINISTRATION_EQUIPMENT_DIR`, `ADMINISTRATION_PROFESSIONAL`,
  `ADMINISTRATION_HOUSE` (the house's folder, for reaching the files beside
  them wherever it's kept; a screen's driver gets it too),
  and `ADMINISTRATION_SHOW=1` (with the screen's variables) when the contract
  was placed with `--show-browsers`. What they leave running is cleared
  with the room.
- **No business on consecrated ground**, for equipment too. An app on a
  device talks to the room's own app (`needs_app = true`; an Android
  emulator reaches this machine at 10.0.2.2) or a local stand-in its setup
  starts, and the accounts it uses are made there. The setup prints the
  addresses it points the app at, and `DATA=seed` or `DATA=local` if it
  loads data; a contract whose equipment prints an address off this
  machine, or other data, is refused, as a room is. The house can't see
  inside a built app, so the hook is where this is kept.
- **A shared test server, declared.** When a local stand-in isn't
  practical and the project has a test server everyone shares (a staging
  API), the equipment lists it: `shared = ["staging.example.com"]` (that
  host and those under it). Its setup may then point the app there and say
  `DATA=shared`; any other address off this machine is still refused. A
  host whose name looks like production (a part that is `prod`,
  `production` or `live`) is refused until the team confirms it isn't, by
  listing it under `not_production` as well: a name is a hint, so
  `prod-replica.example.com` can be used, once someone has said so. Never list production under another name: it's the one
  line the house can't check. When the Director plans one, its plan has a
  **Shared server** part that says so, and the Claude session setting it
  up asks the person, naming the host, before writing the line, with a
  comment above it recording that it was confirmed. Each professional on it is told the app
  talks to a shared server, and the summary marks what was seen only
  there: "it could be the server, not the app". The same finding from
  every device is one line, so a server's own fault shows as one. Accounts
  made there are other people's to see: make them test ones.
- Every path is inside the project, one program, no arguments.

## Casting with it

A professional names what it uses:

```yaml
administration:
  kind: user
  uses: [desktop-app]
  browser: false
```

A `user` with `uses` gets that equipment's tools; with `browser: false`
it gets nothing else (no web app, and the project needs no
`.administration/setup`). Without it, it has the web browser too. The
Director's job lists the project's equipment with each `about` line, so it
can cast a "first run on Linux" user for the desktop app the way it casts a
phone user for the web app, and a cast that names equipment the project
doesn't declare is sent back.

## When there's none yet

When a contract needs something nothing reaches, the Director doesn't
cast a user who can't get in. It writes `out/equipment.md`: for each piece,
what it reaches and which risks need it, what it rests on (for the session
to check is installed), the `[equipment]` block, what the hooks must do,
the material the room should hold, and one check that it works.
`director show` prints it; `apply` never installs it. The Claude session
that placed the contract finds or builds it with the person, shows them
the hooks, runs the check, and casts again. The Director proposes; the
person's own session builds, where they can see it.

## Checking it

`administration equipment-check <name>` starts one piece the way a
contract would, in a fresh room (with the app's room first when it
`needs_app`), says what its setup printed, takes one picture through a
screen (saved to `--shot FILE`, or the books), and clears it all away. A
device that won't boot, a driver whose pictures come out truncated, or an
address off this machine shows up there in a minute, not as a contract's
lost purse. Then a small first contract, one user with `--only` a short
task, proves the rest.

It also says which build the setup installed (below, Which build), and
with a picture, what the app's badge should read.

During a contract, a screen whose pictures fail three times in a row is
lost: the professional is told it's the device, not the app, to stop, and
to write what's left as unreached; the summary says "equipment lost at
HH:MM", so the coverage reads as partial. `administration resume <id>
<name>` places a new contract for them with only what they couldn't reach.

Before that, a screen whose equipment declares `restart` is restarted
once: the hook runs as the person, in the folder the setup hook runs in,
with the setup's environment
(`ADMINISTRATION_ROOM`, `ADMINISTRATION_PORT`, `ADMINISTRATION_BUILD`,
`ADMINISTRATION_DEVICE_PORTS`, and `ADMINISTRATION_SHOW` with
`--show-browsers`...),
and has to bring the same device back (the same serial: kill the
emulator on the room's console port and boot it again there) with the
build installed and the app open. If the screen answers after it, the
professional is told the device was restarted and where they were is
gone, and carries on; the status says "equipment restarted at HH:MM". If
the restart fails, or the device dies again, it's lost. A failed hook's
stderr goes in the record ("equipment lost") and the server's log, never
to the professional, who is told only that restarting failed. Write the boot
once and have both hooks use it, so a restart is the setup's boot.

## Which build

A contract is on a commit: the project's HEAD when it was placed, and
the uncommitted work on top if there was some ("Tested build" in
`status` and the summary). What a room installs has to be that build,
and the house can only know it if the setup says so. A setup that
installs an app prints what it installed:

```sh
echo "BUILD_COMMIT=$(git -C "$ADMINISTRATION_MAIN" rev-parse HEAD)"  # better: the commit stamped in the build itself
echo "BUILD_APP=org.example.notes"
echo "BUILD_VERSION=1.4.0"
```

A build from another commit is refused before anyone enters, with the
commit it is: rebuild, or place the contract with `--build <sha>` to test
that build on purpose. Read the commit from the build (a field your build
stamps into the app, the artifact's own metadata), not from the checkout,
or a stale artifact at a shared path passes as new. A screen whose setup
says nothing is let in, shown as "build not reported". Other equipment
(`commands`, a sandboxed tool) that says nothing gets no build line: it's
taken to install no app. One that installs the app under test should say
so like a screen does, and is held to it the same way. `BUILD_COMMIT` is
a commit's hash (whole or its first seven letters or more, `-dirty` on
the end for a build from a changed tree, which is let in and said), or a
tag this checkout has, by its name alone (`v1~1` isn't a tag). A version, a tag from elsewhere or a branch name
is refused: nothing holds the first two to a commit, and a branch names
wherever the checkout is now, not what was built (a hash is looked up as
a commit only, even when a branch has the same name).

Better still, let the house build it: a `build` hook in the house builds
a commit into a folder, once, in a clean checkout with the house's `env/`
copied in (README, The build hook; docs/design.md). The setup then
installs `$ADMINISTRATION_BUILD`, and the room's build is known: a setup
that prints nothing is taken to have installed it, and one that prints
`BUILD_COMMIT=$ADMINISTRATION_BUILD_COMMIT` is held to it, uncommitted
work and all (`$ADMINISTRATION_BUILD_WORK` is that work's hash).

**Give test builds a badge.** A small banner in a corner, or a line in
the app's settings, reading the commit and the server it talks to
("UAT · a1b2c3d · api 10.0.2.2:8080"), so every screenshot says which
build it is. A professional's picture, a person's screenshot from a dev
device, and a verifier's reading can then be told apart at a glance;
without one, a dev build of the default branch and a room's build of the
change look the same. `equipment-check` names the commit the badge
should read.

## Room devices apart from yours

Rooms and the person's own development devices on one machine must not be
mistaken for each other, by people or by agents:

- **Their own names.** Rooms boot their own AVD (or simulator) made for
  rooms, never the one used for development: an agent driving "the
  emulator" by AVD name once tapped into a room's phone. Name it so it
  reads as a room's (`<app>-room`).
- **Their own ports.** Rooms take emulator console ports from a range
  kept for them, 5620 to 5680 (even ones; each emulator takes the odd port
  above for adb). Equipment hooks get the range as
  `ADMINISTRATION_DEVICE_PORTS` (`5620-5680`; set it in the environment
  you place from to move it: first even, and no higher than the last, or
  place refuses it). A room's setup claims a port there and
  passes over any port something else holds (its console or adb port
  answers, or adb already lists `emulator-<port>`), never sharing one:
  `examples/phone-demo` does. Keep development emulators at 5554 to 5618,
  and have the project's own dev tooling (anything that boots emulators
  for people or agents) refuse the rooms' range. A development emulator
  once booted on a port in the rooms' range during a contract, and the
  room's emulator on that port crashed twice.
- **Only the build under test.** A room's setup uninstalls every app it
  didn't install (on Android, the third-party packages, `pm list packages
  -3`, other than the one under test) before installing the build: an
  AVD's snapshot can carry an old app, and a person or professional who
  opens it judges the wrong one. A read-only emulator forgets the
  uninstall when it's killed, so it costs nothing.

## Codes and links that arrive elsewhere

Sign-up that needs a code from an email or a text can't be finished on the
screen alone. Give it a way that stays in the room:

- the setup makes accounts already confirmed, so the flow under test starts
  past that step; or
- the room's app sends mail to a local catcher its setup starts (Mailpit,
  MailHog), and a `commands` program reads the newest message for one
  address: the professional's own mailbox, and nothing else.

Never read codes from a shared server. A program under `commands` runs as
the person with whatever it holds, so give it the narrowest reach there is:
one fixed query, its argument checked (an address that matches the room's
pattern), no credentials it doesn't need.

## The wall

The professional is walled as before: only the declared tools or
commands, no shell, no files of the person's. The equipment itself is not
walled by the Administration. It's the person's own code, run with their
permissions, like the setup hook. What the professional can do is exactly
what the equipment's controls let it do, so a project should declare
controls that drive the thing under test and nothing more (no
`adb shell`, no VM console).

## What it makes possible

- **A desktop app.** Setup starts the built app with a
  debug port (Electron's `--remote-debugging-port`), the browser server
  connects to it, and a user clicks through the real window. On Linux,
  hidden means a virtual display (Xvfb) or Chromium's headless platform;
  `--show` puts the window on the real desktop, which is how a window
  manager's own behaviour gets tested.
- **Android** (`examples/phone-demo`). Setup boots an emulator of the
  person's own AVD for the room (`-read-only`, so several rooms share one
  AVD, on a console port it claims in the rooms' range), uninstalls any
  app it didn't put there, waits for the phone's network, opens
  the app, and prints `SERIAL=`; the screen drives it through adb. Shown
  (`--show-browsers`) drops `-no-window`. The Administration ships no SDK
  or image: `pass_env` hands the hooks `ANDROID_HOME`, `ANDROID_AVD_HOME`
  and the AVD's name.
- **A tool on another OS.** A command wrapper that runs the tool inside a
  VM or container the person already has (Docker, UTM, Lima), copying
  files in and results out. The professional only ever runs the wrapper.
- **A device on another machine.** An iOS simulator on a Mac, driven from
  Linux: setup, teardown and the driver each run over SSH (`simctl` to boot
  and screenshot, `idb` to tap and type). Copy a helper script to the far
  side rather than nest quotes (`ssh ... bash -lc` around `python3 -c`
  breaks); share one SSH connection (`ControlMaster`), or every tap is a
  new login; write `simctl io ... screenshot` to a temp file and send that,
  since some Xcode versions write nothing to stdout. The app on it talks
  to a stand-in its setup starts on that machine, or a shared test server
  the equipment declares.

## Not in the first build

- Equipment shared between rooms (one emulator for everyone): per room
  only, for now.
- Installing equipment: the project's setup does it, or the person.
- Windows or macOS VMs as examples: a screen driver over VNC, or the
  wrapper shape, covers them, but nothing here has been tried on one.
- Enforcing the rooms' port range on the person's side: the house can
  give rooms their range and a setup can pass over a taken port, but only
  the person's own tooling can keep out of it.
- An emulator killed hard (not by its teardown) can leave its AVD locked,
  and the next boot waits on it; the teardown's `emu kill` avoids that.

## What the first module taught

The first equipment was for a desktop photo converter (a lab copy): its
command line on Linux through the command server, and the desktop window
through Xvfb, Electron's debug port and the pinned browser server, with a
drop tool for real files. Six live runs, and what each changed:

- **Hire one narrow person on a small device first.** On a phone app, the
  most useful professional was one specific person on the smallest phone
  the app supports, going through one real flow: they found the layout,
  keyboard and sheet problems no critic could. Once the equipment works, a
  persona written by hand is fine (who they are, their device, what they
  do and what trips them up): `administration check` it, hire it, and
  `director review` tunes it from what it finds and misses.
- **Tell the Director users can work through equipment.** With no web
  setup it read "no users" and cast only critics.
- **Equipment is only as good as its room's material.** Six good photos
  gave three users 0, 1 and 3 findings; a desk of awkward inputs
  (truncated, empty, mislabelled, hidden, huge, transparent, odd names,
  a symlink, 120 broken files) and tools to pipe, list and inspect gave
  one user six real bugs. Plan the material with the hooks.
- **Hold back the tools that reach past the room.** In an app whose own
  code reads the person's files, `browser_evaluate` and `browser_navigate`
  are a way out; the window has neither. Give the ones a user needs to
  get unstuck (`browser_handle_dialog`: one alert stopped a user cold).
- **A custom action is a command, and it needs the room's other
  equipment:** the drop tool reads the window's debug port from `EQ_`
  variables, and must aim at the main window, not the last one opened.
- **The Director can plan equipment.** Asked for the desktop app with
  nothing declared, it wrote a plan (virtual display, debug port, profile
  in the room, a drop tool, material, a check) that was right in shape;
  the tool names were wrong until it was given the list.
- **Findings only count as FINDING: lines.** A window user wrote six real
  problems into its report and none as FINDING: lines; the brief now asks
  for every finding again at the end. The same cast then gave 4, 11 and
  12 where it had given 0, 0 and 1.
