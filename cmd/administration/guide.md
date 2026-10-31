# The Administration

The Administration hands out **contracts**. A contract is a test of the
running app, or of a branch's changes, placed by a person. The
**professionals** on the project's **roster** take it. Each one is an agent
trained for one kind of work, working alone in a room of its own, paid in
**coins** (wall time). The **Director** trains new professionals. Production
is consecrated ground: no business is ever done there.

Everything runs through the `administration` command. Run it with Bash,
one command per call: no `;`, `&&`, pipes or `cd` around it, because the
person's permissions allow `administration ...` and a compound command
asks for approval nobody may be there to give. Don't try to stand in for
it. If it says it isn't installed, give the
person the one line, which they run once in a terminal:

```
curl -fsSL https://raw.githubusercontent.com/tj-techneosis/the-administration/main/install.sh | sh
```

When the person asks to upgrade the Administration, run `administration
upgrade` and pass on what it says (it tells them when the plugin itself
needs `/reload-plugins`; usually nothing does).

Professionals run the standalone Claude Code CLI (`claude`), which the VS
Code extension doesn't provide on its own. If `place` says `claude` isn't
found, they install the CLI (code.claude.com/docs/en/setup), then quit and
reopen VS Code.

The commands are namespaced by the plugin: `/the-administration:contract`,
`:redteam`, `:hire` and `:roster`. Write them that way when you offer them.

## The Rules (they don't bend)

1. No business on consecrated ground. Never point a contract at production,
   a production database, a thaw or a replica. Rooms start from seed data
   or a local copy the project's own tooling made (a data set, `--data`);
   the command refuses anything else; don't look for a way around a refusal (exit code 3). Tell
   the person what was refused and why.
2. Every professional works alone.
3. One contract, one target. "The sign-up flow", not "the app". If a request
   is sprawling, propose splitting it.
4. Every contract is paid in coins.
5. No claim without evidence. Every finding is checked against the code
   before you report it as real.
6. Say what could not be reached.

## Placing a contract

1. **Name the target, with its real scenario.** Say what and for whom:
   which screens or flows, which kind of person uses them, and on what
   device or screen size if it matters ("the owner's flyer editor, which
   she uses on her phone", not "the flyer editor"). Professionals test what
   the brief says; a scenario left out is a scenario not tested. If it's
   about changes ("these changes", "my branch"), the contract is on the
   changes: add `--changes` (and `--base REF` if they name a base). With
   nothing changed it exits 1 with "nothing has changed since ..."; say so.
2. **Have the Director cast it** (the default for any contract worth
   doing): run `administration director cast "<target>" [--changes]` with
   `run_in_background`. The Director reads the code in scope, the recent
   commits and the roster, and writes 2 to 5 specialists for this contract,
   each with a checklist pointing at files, lines, screens and widths. It
   prints a run id; `administration director show <run>` prints its plan:
   what each specialist covers, which roster professionals it also wants
   ("also cast"), what it swept and what nobody covers.
   - For a quick check, or when the person names professionals, skip this
     and use the roster: `administration roster --match tag,tag --json`
     ranks who knows what.
   - If the run lists problems, it already had one repair round; say what's
     left and cast from the roster instead.
   - If the plan ends with an **equipment** section, the contract needs
     something nothing here reaches yet (a desktop app's window, a phone, a
     tool on another OS, better material in the room). That's yours to
     build with the person, never the Director's: say what it needs in a
     line, and offer to set it up. If they say yes, check what it rests on
     is installed (ask them to install anything that needs their password;
     `! <command>` runs it here), write the hooks and the
     `[equipment.<name>]` block into the project's `.administration/`, show
     them the hooks (they run as the person, outside every wall), run the
     plan's "how to tell it works" check, then cast again. Or place the
     cast now and add the equipment for the next contract; say which.
3. **Propose the cast** in a few lines, and wait for a yes unless the person
   already said to go ahead:
   > Contract on "the forms on a phone". The Director wrote 3 specialists:
   > form-second-try (critic: double submits, limits, bots), phone-forms-320
   > (user: the forms and header at 320px and large text), admin-home-truth
   > (critic: are the dashboard's numbers true). Also cast: the owner. Not
   > covered: the waiver form. Place it?

   **Mix the kinds**: critics catch logic they can read, browser
   professionals are the only ones who see layout. They may add or strike
   names, or change the coins.
4. **Placing**: `--with <run>` brings the Director's specialists (each one
   that finds something joins the roster when the contract closes);
   `--cast a,b` adds roster names. **To re-check a fix, skip the Director**:
   cast the professionals who found the bug (`--cast`), so the contract
   starts in seconds.
   **Give each roster professional its own task** with `--brief
   <name>="..."`: you know what was just built, so write the steps a person
   in their shoes would take through it, at the widths that matter, ending
   on the thing most likely broken ("an order paid half by gift card, half
   cash; check the card is charged only its half; refund one item and
   check which payment it goes back to"). One task each; the Director's
   specialists already have theirs. `--only <name>="..."` makes the task
   the whole of their work, with the persona's own checks off: for a quick
   smoke test ("open the app and take one screenshot") that shouldn't turn
   into a full run.
   If the Director's plan names a `data:` set, it's used; otherwise pick
   one with `--data <name>` when the default seed lacks what the tasks
   start from.
5. **Place it**: `administration place "<target>" --with <run> [--cast a,b]
   [--changes] [--brief <name>="..."]... [--data <name>] [--coins 10m]`. It
   prints the contract id and returns at once.
6. **Wait in the background**: run `administration wait <id>` with
   `run_in_background` (a foreground wait dies at the Bash time limit). It
   prints a line as each professional's state changes, and returns early
   if one fails or is refused while the others work: tell the person who
   and why (its line says), then run the wait again for the rest. Tell
   the person it's placed, about how long it takes (the largest purse in
   minutes, plus a few minutes to build each room), and that in VS Code
   `/tasks` shows it running. Then carry on with whatever else they want. If
   they ask how it's going, `administration status <id>`.
   **Watching**: in Herdr or tmux, place opens a watch tab with a pane per
   professional, without taking focus, in the Herdr space you're working
   in; each pane closes once its professional is back. A `watch` hook (the
   person's or the project's) opens them instead, wherever it likes. Place
   says which ("Watching in Herdr", "Watching through the watch hook");
   tell the person they're there. If it says "Couldn't open the watch
   panes", pass on why, and the watch line it printed. Anywhere else (VS
   Code), give them the line place printed, `administration watch <id>`,
   to run in a terminal of their own: a live line per action and every
   finding as it's written. Watching changes
   nothing about the work. If they don't want the panes, `--no-watch`.
   If they want to see the browsers themselves, place it with
   `--show-browsers`: each user's browser opens as a window on their
   screen. Only when they ask.
7. **Later, or on a repeat** ("every morning", "at 7am"): the
   Administration has no clock. The person's own scheduler runs it: a
   script that does `administration place ... --cast a,b`, then `wait`
   and `close`, booked with whatever schedules things on their machine
   (the Crontinental's skill, if it's installed). A contract that ran with
   nobody there closed with nobody checking its findings, so when the
   person comes back to it, read its summary (`administration report <id>`)
   and check every finding before reporting it.

A project with no `.administration/setup` can still place contracts with
critics (code readers). Browser professionals need the setup hook; if it's
missing, say so and offer to help write one (see the README's "A House of
Your Own").

## Closing a contract

When the wait finishes:

1. `administration close <id>`, then `administration report <id>` prints
   the summary (`report <id> <name>` one professional's report). Read them
   that way: the books are outside the project, and reading them directly
   asks for a permission the command doesn't need.
2. **Have the verifiers check every finding**: run `administration verify
   <id>` with `run_in_background` (a few minutes). Walled verifiers read
   the project's code, nothing else, and record **confirmed**, **likely**
   (the code agrees, but reading can't settle it) or **false** for each,
   with the line that decided it; the summary shows them as "by a
   verifier". If one didn't finish, it says so; run verify again for what
   it left. Then **review them**, yourself, before reporting: open the line
   behind every NEW finding and every one marked false, and a few of the
   confirmed. Never pass a finding on unchecked, by them or by you. The
   summary lists each finding once, with who found it and its number in
   their report (`name #3`); several who found the same thing on their own
   come first. **Record where you disagree** in the books (yours replace
   the verifiers'): write them to a file, `{"<name>": [{"finding": 3, "verdict":
   "confirmed", "why": "..."}]}` (for a folded finding, under each name
   that found it), and run `administration verdicts <id> <file>`; a later
   run adds to earlier ones, and a finding checked again takes the new
   verdict. When two who found the same thing were checked differently,
   the summary shows both. The summary then shows each verdict beside its
   finding, for whoever reads it next, and the stats learn who earns their
   coins.
   **Links between contracts**: when another contract on the same work
   has the same bug (a critic predicted it, a user saw it on a phone), the
   summary says "maybe the same as contract X name #n (proposed: ...)".
   Check each like a finding: same defect, same cause? Add `"also":
   ["X name #n"]` to that finding's verdict when it is, `"not_also":
   [...]` when it isn't; also add an `also` for any you notice yourself.
   A confirmed link shows on both summaries, first, as the strongest
   evidence there is, and each decision teaches the matcher this
   project's words.
3. **Report briefly**: what broke, how to see it, how sure you are, ranked by
   how much it would hurt a person using the app. On a contract on changes
   (a release check, "redteam these changes") lead with the verdict: the
   summary opens with "What these changes broke" (findings marked NEW).
   Check those first; say plainly "these changes broke nothing we could
   find" when none survive the check, or list what they broke. Then what's
   wrong in the change's own comments, documents or tests (CHANGE): worth
   fixing, but nothing a person sees behaves differently, so it doesn't
   hold a release by itself. Then, short and apart, what was already so
   before (OLD) or unmarked; those aren't the release's problem. Then what nobody could
   reach. Keep it to text: findings may contain test data; never paste
   screenshots or personal data into a commit, issue or chat.
4. **New on the roster**: the summary lists the Director's specialists who
   found something; they joined the roster at close
   (`.administration/professionals/<name>.md`), so the next contract casts
   them by name. Say who joined in one line. If one only found what turned
   out false, offer to let it go (delete its file); the Director keeps
   managing the rest through `director review`.
5. **Open rooms**: if the contract needed tags nobody knows, or the summary
   lists open rooms that fit, end with one line offering the Director:
   "Nobody tested this with a keyboard only. Train one? (/the-administration:hire)".
6. **A miss**: when the person (or your own check) says a professional
   saw something and let it pass ("it was on the screen and it called it
   fine", "there's no saved message and nobody said so"), record it:
   `administration missed "<what it saw, and why it's a problem>" --contract <id> [--by <name>]`.
   It goes in `.administration/missed/` for the team, and the next
   `administration director review` rewrites the persona of whoever
   should have caught it, so that kind of thing is a finding next time.
   Show the person what changed (`director show`, and the file against the
   old one) before `director apply`. Not for something it never reached:
   that's coverage (a split, or the room), not judgment.
7. If the project has its own way to capture lessons (a capture or notes
   command), list the confirmed findings worth keeping as candidates for it.
   Don't run it yourself.

**Equipment lost**: a professional whose device died mid-contract (three
failed screenshots in a row) is told to stop and write what's left as
unreached, and marked "equipment lost at HH:MM"; their part is partial. Say so, and offer `administration resume
<id> <name>`: a new contract for them with only what they couldn't reach.
It also picks up after a professional ran out of coins.

Exit codes: 0 done, 1 error, 2 a room or professional failed, 3 refused by
the Rules. On a 2, the `wait` line and `administration status <id>` give
each failed professional's reason; for more, read
`~/.local/share/administration/contracts/<id>/<name>/setup.log` (the room)
or `claude.log` (the professional) and say what broke. A user whose
browser never worked did none of their part: say so, and don't pass on
their findings as tested.

## Setting a project up

1. **Bring in their agents** ("set it up", "bring in my agents"): don't
   search for agents yourself. Run `administration director onboard` in the
   background; it reads the project's `.claude/agents/` and those of the
   folders above it (a shared `../.claude/agents/` included) and proposes
   the roster: critics joining by name, browser and command-line
   professionals converted, the rest said why not. Show the plan from
   `director show <run>` in a few lines; on a yes, `director apply <run>`,
   then `administration hire <name>` for each converted professional it
   lists.
2. **The setup hook**: browser professionals need `.administration/setup`,
   which starts the app on seed data in each room (the README's "A House of
   Your Own" has its contract). If there isn't one, offer to write it: read
   how the project runs its app, seeds a demo and makes logins, and keep
   everything that calls out switched off.
3. **Check it with `administration room-check`**, in the background (a real
   build takes minutes). Never run the hook yourself: it needs permissions
   a session usually doesn't have, and room-check runs it the way a
   contract does, in a fresh room, then clears it. It makes the hook
   executable if it isn't, says whether the room came up on seed data and
   which logins it made, and shows the end of the setup's output if not.
   Fix and run it again until it passes, before any contract.
4. **Equipment**, when what they test isn't a web app (a desktop window, a
   phone, a tool on another OS): see `docs/equipment.md`. When onboarding
   leaves agents out for want of equipment, or a cast needs some, the
   Director's plan is in `director show <run>`: check what it rests on is
   installed, build it with the person, add its block to
   `.administration/roster.toml`, then onboard
   (or cast) again so those agents come in through it. The programs it
   declares needn't be executable; the Administration makes them so.
   `room-check` checks the setup hook only; **check each piece with
   `administration equipment-check <name>`** (in the background): it
   starts it in a fresh room, says what its setup printed, takes one
   picture through a screen and clears it away. Fix and run it again until
   it passes. Equipment that points an app off this machine is refused
   unless it declares that server shared (`shared = ["<host>"]`, a test
   server, never production). When the plan has a **Shared server** part,
   ask the person straight out, naming the host: "Is <host> production?
   The professionals will make accounts and change data there." Add the
   line only on a clear no, with a comment above it that says so: `#
   <host>: a shared test server, not production (confirmed with the
   person, <date>)`. If they're unsure, plan a local stand-in instead.
   A host whose name looks like production (`prod-replica`, `live-demo`)
   is refused until the person has said it isn't: ask about that host by
   name, and on a clear no add it to `not_production = ["<host>"]` in the
   same block, with the same comment.
   Then a small
   first contract (one user through it, a few coins, `--only` a short
   task) proves the rest.
5. **Data sets**, when the default seed lacks what contracts need (the
   records a flow starts from) or the person has their own way of pulling
   data for local testing: the hook loads it on `$ADMINISTRATION_DATA`,
   each set declared as `[data.<name>]` in `roster.toml` (README, "Data
   sets"). Check each with `room-check --data <name>`; place with
   `--data <name>`, or the one the Director's plan names. **Before a
   release check** on a set (or a hook) changed for this work, run
   `room-check --changes --data <name>`: it also builds the app without
   the changes, as the contract's users get, and a set that needs the new
   code fails there in a minute instead of in the contract.
6. Remind them to commit `.administration/`.

## Training and hiring (the Director)

- **After a contract**, the Director's specialists who found something are
  already on the roster. One that found nothing can still be kept by hand
  (`administration hire --from <id> <name>`).
- **When someone is needed** ("hire someone who ..."), run
  `administration director train "<who, and why>" [--contract <id>]` in the
  background. It writes a candidate to the run's `out/candidates/`;
  `administration director apply <run>` puts it in
  `.administration/candidates/`, and `administration hire <name>` on the
  roster, each step checked again. Show the person the candidate in a few
  lines (who, knows, coins) before hiring.
- **Setting a project up**: see "Setting a project up" above.
- **Every so often** (or when the person asks to tidy the roster):
  `administration director review` condenses what each professional
  learned into its dossier and proposes merges, splits, retags, retirements
  and new coins, with the numbers, and tunes professionals for any open
  misses; `director apply` takes the dossiers and the tunings, and each
  roster change is the person's call.
- A name already on the roster is refused; `--replace` retrains them, only
  if the person asked. Remind them to commit the files so the team gets the
  hires too.
- After an ad-hoc "pretend you're a nurse on a slow phone" in conversation
  that found something real, offer once to make that persona a regular.

## Showing the roster

`administration roster` lists who's hired, where from (the project's own
hires, or existing agents joining as critics), what they know, and the open
rooms. Existing agents in the project's `.claude/agents/` join by name in
`.administration/roster.toml`, with their tags there, and their own files stay
untouched:

```toml
[critics.code-reviewer]
knows = ["code-review", "typescript"]
```

If the project has agents in `.claude/agents/` that aren't on the roster,
offer to add them this way.
