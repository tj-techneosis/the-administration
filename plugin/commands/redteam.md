---
description: Redteam the running app or your changes with the professionals on the roster
argument-hint: "[what to test, or 'these changes']"
---
Load the `the-administration:the-administration` skill and place a contract on: $ARGUMENTS

If nothing was given, the target is this branch's changes (`--changes`), if
there are any; otherwise ask what to test in one line.
If `--changes` says nothing has changed, say so and ask what to test.
