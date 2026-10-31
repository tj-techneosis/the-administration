---
description: Hire a professional onto the roster, or have the Director train one
argument-hint: "[candidate name, or who you need]"
---
Load the `the-administration:the-administration` skill and follow "Training and hiring".

The request: $ARGUMENTS

- If it names a file in `.administration/candidates/`, run `administration
  hire <name>` and report what it says.
- Otherwise it describes who is needed: run `administration director train "<who is needed>"` in the
  background, show the candidate it writes in a few lines, and ask whether to
  hire them (`administration director apply <run>`, then `administration hire <name>`).
- If it's empty, list the candidates waiting in `.administration/candidates/`
  and the open rooms from `administration roster`, and ask which.
