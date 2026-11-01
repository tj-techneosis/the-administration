---
name: the-administration
description: Redteam an app or a branch's changes with walled agents that report findings with evidence. Critics are code reviewers (the changes, an area, or rules to enforce); persona users (a newcomer on a phone, busy staff, keyboard-only) work the running app on seed data. On a branch's changes it sweeps around them too: does everything that uses what changed still work and look the same. Use it when the person wants agents to review or redteam code or the app, since its agents are walled and their findings are verified and kept: "redteam these changes", "set up redteamers", "critics to enforce these rules", "have the professionals review this PR", "try this on a phone", "test it as staff", "send the professionals". Also use it to hire or train a persona, show the roster, or set a project up for it (bring in existing agents, write or check its setup hook or equipment). Not for a quick review the session can give itself, preflight checks, running tests, or questions about a file.
allowed-tools: Bash(administration *)
user-invocable: false
---

# The Administration

Your instructions for this come from the installed tool, so they always
match the version that runs. **First, run `administration guide`** with
Bash, on its own (no `;`, `&&`, pipes or `cd` around it), and follow what
it prints for the rest of this task. Run it again in each new task; don't
work from an earlier copy.

- If it says `administration` isn't installed, give the person the one
  line, which they run once in a terminal:

  ```
  curl -fsSL https://raw.githubusercontent.com/tj-techneosis/the-administration/main/install.sh | sh
  ```

- If it says `guide` is an unknown command, the installed tool is older
  than this plugin: they upgrade it by running that line again.
