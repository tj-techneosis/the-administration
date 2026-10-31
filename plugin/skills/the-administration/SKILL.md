---
name: the-administration
description: Test the running app or a branch's changes the way real people use them. Persona agents (a newcomer on a phone, busy staff, keyboard-only) each work alone on seed data and report findings with evidence. On a branch's changes it sweeps around them too: does everything that uses what changed still work and look the same. Use for "redteam these changes", "put a contract on these changes", "sweep my branch", "redteam the checkout", "try this on a phone", "test it as staff", "send the professionals", hiring or training a persona, showing the roster, setting a project up for the Administration (bringing in existing agents, writing or checking its .administration/setup hook or equipment). Not for a plain code review of a PR or diff, preflight checks, running tests, or questions about a file.
allowed-tools: Bash(administration *)
user-invocable: false
---

# The Administration

Your instructions for this come from the installed tool, so they always
match the version that runs. **First, run `administration guide`** with
Bash, on its own (no `;`, `&&`, pipes or `cd` around it), and follow what
it prints for the rest of this task. Run it again in each new task; don't
work from an earlier copy.

- If it says `administration` isn't installed, give the person the two
  steps, which they run once in a terminal:

  ```
  git clone https://github.com/tj-techneosis/the-administration ~/.local/share/the-administration
  ~/.local/share/the-administration/install.sh
  ```

- If it says `guide` is an unknown command, the installed tool is older
  than this plugin: they upgrade it by running `install.sh` again from
  their copy, after updating that copy.
