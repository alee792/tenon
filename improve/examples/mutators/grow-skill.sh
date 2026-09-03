#!/bin/sh
# A structural mutator: adds a locus the genome did not have.
#
# combine can only ever shuffle genes the parents already hold, so without a
# mutator like this the genome's dimensionality is frozen at the seed. This is
# how an agent acquires a capability rather than rewording an existing one.
set -eu

report=${EVOLVE_PARENT_REPORT:-/dev/null}
existing=${EVOLVE_GENES:-}

name=$(claude -p "Read this evaluation report of an agent's runs: $(cat "$report" | head -c 12000)

The agent already has these components: $existing

Name ONE new skill that would have helped on the runs that scored worst.
Answer with a single lowercase hyphenated word, nothing else." | tr -dc 'a-z-' | cut -c1-32)

[ -n "$name" ] || exit 1
[ ! -d "skills/$name" ] || exit 1

mkdir -p "skills/$name"
claude -p "Write skills/$name/SKILL.md for an agent, based on this report of its
weakest runs: $(cat "$report" | head -c 12000)

Required format — YAML frontmatter with exactly 'name: $name' and a one-line
'description:', then a short Markdown body of concrete instructions. Output the
file contents only, no fences." > "skills/$name/SKILL.md"
