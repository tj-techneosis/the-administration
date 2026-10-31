---
description: Show who's on the Administration's roster, what they know, and the open rooms
---
Load the `the-administration:the-administration` skill, then run `administration roster` and show the result as is. If there are
problems listed (files left off the roster), say how to fix each one. If
the roster is empty, say that the project trains its own professionals and
offer `/the-administration:hire`.
If the project has agents in `.claude/agents/` that aren't on the roster,
offer to add them as critics with `[critics.<name>]` entries (and their
`knows` tags) in `.administration/roster.toml`.
