#!/bin/sh
# Rewrite the agent's instructions, one small change at a time.
#
# The model doing the mutating is deliberately not the model being evaluated:
# this is a cheap edit, while the agent under test is whatever the search's
# `model` pin says. Keeping them separate keeps the comparison about the
# instructions rather than about which model wrote them.
set -eu

report=${EVOLVE_PARENT_REPORT:-/dev/null}

claude -p --model claude-haiku-4-5-20251001 \
  "Here is an agent's instructions.md:

$(cat instructions.md)

Here is how it performed on its last runs:

$(head -c 8000 "$report")

Rewrite instructions.md to make the agent better at that task. Change ONE
thing and keep it small — sharpen a rule, add a single concrete constraint, or
cut a line that is not earning its place. Keep the YAML frontmatter exactly as
it is, including the description field. Output the complete file and nothing
else: no code fences, no commentary." > instructions.next

# A truncated or fenced reply is not worth a harness run; let the gate reject
# it as a malformed candidate rather than silently shipping a broken genome.
mv instructions.next instructions.md
