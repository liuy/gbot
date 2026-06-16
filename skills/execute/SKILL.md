---
description: "Execute a plan file step by step, verifying after each change."
context: fork
agent: Executor
is-user-invocable: true
---
$ARGUMENTS

If no arguments given, look for the most recent plan file in `~/.gbot/plans/` and execute it.
