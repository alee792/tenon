#!/bin/sh
# An LLM-driven mutation operator. Runs with the candidate genome directory as
# its working directory and edits exactly one gene in place.
#
# Keep the edit to ONE gene. A mutation that touches three files at once makes
# the resulting score unattributable, which defeats the point of the search.
set -eu

gene=$(ls instructions.md skills/*/SKILL.md 2>/dev/null | sort -R | head -1)
[ -n "$gene" ] || exit 1

claude -p "Rewrite $gene to make this agent better at the task it describes.

Change ONE thing and keep it small: sharpen an instruction, add a single
concrete rule, or cut a line that is not earning its place. Preserve the YAML
frontmatter exactly. Write the whole file back to $gene and change no other
file." >/dev/null

# A malformed edit is fine to emit — tenon validate rejects it before any
# model is opened for evaluation, and the diagnostic id lands in the lineage.
