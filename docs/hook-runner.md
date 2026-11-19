# Running a room's hooks as their own account

## The threat

A room is built by the house's hooks: `setup`, `teardown`, `reset`, the
`build` hook, and the cleanup lines a setup declares in
`$ADMINISTRATION_CLEANUP`. They install and build the branch under review
(`npm ci`, a compose build, a bundler), so they run the branch's code and
the code of every dependency it pulls, install scripts included.

Without a hook runner they run as the account that runs the Administration.
On a machine shared by a team that account holds what matters most: its
Claude sign-ins, its keys and tokens, its git credentials, and the books
(every contract, report and verdict). One bad package in a branch's
lockfile could read those keys, or rewrite the books and the reports that
come after it.

A hook runner closes that: every one of those hooks runs as a separate
account that has none of it. What the hook can still reach is what that
account is given: the room, the room's copy of the project, the Docker
access you give it, and the network.

## What changes with one configured

- **Every room hook runs through the runner**: setup and teardown (the
  room's app and, for a release check, the before app's), reset, the
  build hook, and each cleanup line. None of them ever runs as the
  Administration's account while a runner is configured.
- **The hook works on a copy.** The room gets `src/`, a copy of the project
  at the contract's commit (and its uncommitted work, as it was placed),
  with the house copied into it as `.administration/`. `$1`,
  `ADMINISTRATION_MAIN`, `ADMINISTRATION_PROJECT` and
  `ADMINISTRATION_HOUSE` point there, never at your checkout. The copy has
  no `.git` and nothing ignored (no `node_modules`), as a build hook's
  checkout doesn't: a setup that leaned on the checkout's installed
  dependencies installs its own.
- **Its own home.** `HOME` is the room's `home/`; `USER`, `LOGNAME`,
  `SHELL`, `TMPDIR`, `MISE_*` and `XDG_*` are dropped, `PATH` loses every
  entry under your home, and any other variable whose value names your
  home is dropped. The variables of the hook contract are passed as they
  always were.
- **Papers in the room.** `ADMINISTRATION_MANIFEST` and
  `ADMINISTRATION_CLEANUP` are files in the room's `papers/`; the
  Administration reads them back into the books when the hook is done.
  The build hook's build is copied into the room's `build/` for the setup
  (`ADMINISTRATION_BUILD`); a build is made in a folder in the rooms and
  copied into the books after.
- **What it leaves running** is stopped as the hook account: a copy of
  the `administration` program in the rooms folder's `.bin/` is run
  through the runner to stop that account's processes carrying the room's
  tag, at teardown and when a hook runs out of time.
- **No fallback.** Before any hook of a room runs, the runner is tried with
  `id -u`: it must work without asking for anything, and run as an account
  that is neither yours nor root. Then the hook account must be able to
  write the room's folders and run the hook. Any of that failing fails the
  room with what failed, and no hook runs; a broken config is never read
  as "no runner".
- **Equipment** (devices, desktop apps, a critic's commands) doesn't run
  through the runner yet, so with a runner configured a professional that
  uses equipment is refused a room instead of having it run as you.

Unset, nothing changes.

## Setting it up, once per machine

Below, `reviews` is the account that runs the Administration and
`reviews-hooks` the hook account; use your own names.

1. **Make the hook account**, with no login and nothing of yours:

   ```sh
   sudo useradd --system --create-home --shell /usr/sbin/nologin reviews-hooks
   ```

   On macOS, make a standard user with `sysadminctl -addUser` and hide it,
   or use `dscl`.

2. **Share a group.** The rooms are given to a group both accounts are in,
   so the hook writes them and you can read and clear them. The hook
   account's own group does:

   ```sh
   sudo usermod -aG reviews-hooks reviews
   ```

   Log in again (or restart the session that runs the Administration) so
   the new membership counts. Never put the hook account in your group.

3. **Let your account run commands as it, and only as it**, without a
   password. With `sudo visudo -f /etc/sudoers.d/administration-hooks`:

   ```
   reviews ALL=(reviews-hooks) NOPASSWD: ALL
   Defaults:reviews !use_pty
   ```

   The first line lets `reviews` run anything as `reviews-hooks` and nothing
   as root or anyone else. The second keeps sudo from putting the hook on
   a pseudo-terminal, which would take it out of the process group the
   Administration watches.

4. **Docker, carefully.** Membership of the `docker` group is root on the
   machine (a container can mount `/` and read your home), which would
   undo all of this. Give the hook account a rootless Docker of its own
   (`dockerd-rootless-setuptool.sh install` as that account) and pass
   `DOCKER_HOST` with `[setup] pass_env`, or a socket proxy that refuses
   bind mounts and privileged containers. A project that needs no Docker
   needs none of this.

5. **The tools the hooks use** (node, a package manager, python) must be
   where the hook account can run them: installed system-wide, or under
   its own home. Nothing under your home is on its `PATH`.

6. **Configure the runner**, in the machine's config
   (`~/.config/administration/config.toml`, or `$ADMINISTRATION_CONFIG`):

   ```toml
   [hooks]
   runner = ["sudo", "-n", "-u", "reviews-hooks", "--"]
   group = "reviews-hooks"
   ```

   It's the machine's setting, never a project's: a project's
   `roster.toml` is in the branch under review, which could take the
   runner out or name one that runs as you.

7. **Check it**: `administration room-check` builds one room through the
   runner and says what failed if anything did.

## What it doesn't cover yet

- Equipment (see above), and the project's event hooks
  (`.administration/hooks/`), which run as you as before; a read-only
  contract doesn't run the project's event hooks at all.
- `administration sweep` looks for processes left behind by your account
  only; the hook account's are stopped at teardown.
- Rooms running at once share the one hook account, so one branch's hook
  can reach another room's app while both run.
- The hook's environment is on its command line (sudo would clear it
  otherwise), where any account on the machine can read it with `ps`.
  The hook contract passes nothing secret; keep `pass_env` that way.
