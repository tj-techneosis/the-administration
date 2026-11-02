---
description: Place a contract (or check on one, or close one) with the Administration
argument-hint: "[target] | status [id] | close <id>"
---
Load the `the-administration:the-administration` skill and follow it.

The request: $ARGUMENTS

- If it is empty, ask in one line whether they want to place a contract
  (and on what) or close one; list open contracts with `administration
  status` first so they can pick.
- `status [id]` and `close <id>` map to those commands; a close goes on to
  check every finding as the skill says.
- Anything else is the target of a new contract: follow "Placing a contract".
  A contract placed while the rooms are taken is queued and starts by
  itself ("Queued, N ahead", exit 0): wait for it, don't place it again.
