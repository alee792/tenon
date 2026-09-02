# ADR 0027: Consolidate the read surface into one gate, and name the pin set for what it does

- Status: accepted, and implemented. The consolidation shipped as four
  slices on this branch: check absorbing validate and `fingerprint show`
  with `--emit`; the manifest→pins rename; clean; and `--format`,
  `TENON_HARNESS`, and the outcome contract. This record documents the
  reasoning, not an appetite.
- Amends: [ADR 0025](0025-make-the-fingerprint-the-unit-of-revision-identity.md)
  (proposed) — its gate-proven property is unchanged and its reference
  rendering moves: the command that reports a fingerprint and its per-file
  contributions is `tenon check --emit files`, not `tenon fingerprint show`
- Bears on: [ADR 0003](0003-separate-agent-source-from-workspace.md) — clean
  is the first command that acts on a workspace without an agent source, and
  the separation that record established is what makes that coherent
- Reuses: [ADR 0013](0013-bound-authored-projects-with-aggregate-budgets.md),
  whose bounds every load behind the gate still enforces
- Context: [docs/workbench/improve-notes.md](../workbench/improve-notes.md)
  (G14–G22 — the surface review, the cold onboarding test, and the two
  independent design passes that converged here)

## Plain-English summary

There is one command that reads an agent project and reports on it:
`tenon check`. It absorbed `validate` and `fingerprint show`, because all
three were the same load and the same gate wearing different names, and it
gained `--emit catalog`, a description of the very thing the gate just
proved. The file that pins the runtime closure is now called the *pin set*
and is supplied as `--pins FILE`, because it never was a manifest. The gate
writes it (`--write-pins`), so there is no ordering to get wrong between
proving a source and pinning it. `tenon clean` undoes an apply.
`--diagnostics` became `--format` because it governs all output, not only
diagnostics. `TENON_HARNESS` supplies a default harness. Every jsonl stream
now closes with an object carrying an `outcome`.

## Decision

**One load, one gate, projections of one result.** `tenon check AGENT` is
the single read-only entry point over an agent project. Without `--harness`
it is the portable gate — load, bound, prove tool contracts, prepare tools
exactly as apply prepares them against a throwaway cache. With `--harness`
it additionally verifies a supplied pin set before any generation and
performs a generation dry-run against the same target apply builds, so
check and apply fail identically on the same source, which was validate's
binding contract and remains binding here. That parity is structural rather
than test-enforced: check, drift, and apply call one `runGate`, so there is
no second copy of the sequence left to diverge.

`validate` and `fingerprint show` were never two commands. Both loaded the
project, both bounded it, both prepared its tools, and both refused to
report anything for a source that failed — ADR 0025 recorded that
`fingerprint show` "runs the same gate as validate and apply" as a
*property*, which is another way of saying the second command existed only
to select a different projection of the first one's result. A reader had to
learn two names, two help texts, and the invariant that they agreed;
merging them makes the agreement structural instead of documented. What was
`fingerprint show` is now `check --emit files`.

**The gate may describe what it proved, and only that.** `--emit catalog`
reports the resolved capability inventory: skills as merged under plugin
precedence with their descriptions, tools with their language, MCP servers,
subagents, and schedules. The resolution is already paid for — the gate
computed all of it to reach a fingerprint — so emitting it costs
serialization, not work, and the flag exists to decide who parses, not what
runs: a loop gating thousands of candidates reads two fields and should not
have to skip an inventory it discards.

`--emit` fires only when the gate passes. An inventory of a source that
does not load is a description of something that cannot run, and a consumer
holding one has been told what an agent *would* have, in a voice that
sounds like what it *does* have. Framed positively: prove the source, then
describe the proven thing, and let the description be warranted by the
proof.

**Tenon never accepts a catalog as input.** The catalog is derived, one
direction only. Accepting an authored capability inventory would create
exactly the second inventory the north star and product principle 9
forbid — a file that can disagree with the directory, that has to be
regenerated after every edit, and whose staleness is a new failure mode the
folder-is-the-API convention exists to abolish. The invariant is
load-bearing, not incidental: it is the reason this record can add a
capability listing at all without contradicting the thing tenon is.

**Manifest becomes pins.** The word *manifest* universally means a list of
contents — a ship's manifest, a package manifest, a staging artifact
manifest — and this file categorically refuses to be one. It records the
agent name, the expected source fingerprint, the tenon version, and per
harness the harness executable version, integration package identities,
tool runtime versions, and an advisory model. Every one of those is an
environment pin; none is a component. The old name told the author the
opposite of the truth, and the first thing the specification then had to say
about it was that it "identifies and pins; it never lists". A name whose
first sentence is a denial is the wrong name. `--manifest PATH` becomes
`--pins FILE` on every command that took it.

The rename reaches the diagnostic identifiers: `manifest.drift.agent`
becomes `pins.drift.agent`, and so on for the whole `manifest.*` family.
Those identifiers are documented as stable across releases, so this is a
break — taken once, before the first release, where it costs nothing, and
explicitly not available again afterwards. Integration *package* manifests
keep the word in both prose and identifiers (`manifest.*` there is
untouched), because that document genuinely is a list of contents:
identity, provenance, platform artifacts, and capability declarations. The
internal Go package keeps the name `manifest` as well; this record binds the
surface, and renaming a package would churn code to no author-visible end.

**Pins are written by the gate.** `check --harness H --write-pins FILE`
resolves the closure and writes the pin set after the gate passes, bound to
the fingerprint just proven; `--model VALUE` records the operator's
advisory choice into it and is meaningless without `--write-pins`. This is
why `manifest write` is gone rather than renamed. A standalone writer must
either re-run the gate itself — in which case pins can be written from a
gate result the operator never saw, with an edit possibly interleaved — or
accept a fingerprint by hand, which *is* the ordering burden. Making the
writer a flag on the gate dissolves the question: there is one load, and the
pins that come out of it describe it.

The instructions-free-root journey survives the move. With `--write-pins`
and no `--pins`, check loads for write and accepts a root without
`instructions.md`, so a loop-generated candidate can still mint the very
pin set that later proves it; with both flags, the supplied pins are
verified before anything is written.

**Clean is apply's inverse.** `tenon clean --workspace DIR [--harness H]
[--force]` removes the files an apply record owns, prunes the directories
that emptying them leaves behind, and drops the record. Neither apply nor
drift covered removal, so switching harnesses left the previous harness's
generated files in the workspace forever, and uninstalling meant deleting
files by hand against a record the operator had to read themselves. Clean
takes no AGENT: it acts on the workspace's own records, which is what lets
it work when the source is gone. Ownership discipline is apply's, run
backwards — a recorded file modified since that apply refuses the entire
clean rather than half-uninstalling a workspace, `--force` overrides exactly
that refusal, and a file tenon never recorded is never touched with or
without the flag.

**`--format`, because the flag governs all output.** `--diagnostics` was
named for what it rendered when diagnostics were the only thing a run
emitted. Once the same flag decides how a fingerprint stream, a catalog, a
pin-write confirmation, and a result object are rendered, the old name
described a fraction of its own scope. No deprecated alias: pre-release is
when a flag rename is free.

**`TENON_HARNESS`, and why clean ignores it.** An unset `--harness` falls
back to `TENON_HARNESS`; an explicit flag always wins, and an invalid value
is reported as coming from the environment rather than from a flag the
operator did not type. Clean is the deliberate exception. There, an omitted
`--harness` means *every* harness recorded in the workspace — the full
reset — so honoring the environment default would silently narrow a
destructive operation to less than the operator asked for. An environment
variable may supply a missing argument; it may not shrink the scope of a
removal.

**The outcome field is the authoritative machine signal.** Every jsonl
stream ends with one distinct object carrying `outcome`, from the full
vocabulary `ok / gate_failed / drift / blocked / error`: `ok` from check,
apply, drift, clean, stage, and run; `gate_failed` when the source itself is
invalid; `drift` when the source is fine but the workspace no longer
matches; `blocked` when clean refuses; `error` when the run could not
complete at all. Previously a failing run simply stopped, and a
consumer had to infer failure from a summary that never came — an absence
that is indistinguishable from a truncated pipe. Exit codes remain, and
remain useful, but they are a coarse projection of the outcome: one integer
cannot carry both what happened and what was produced, and a loop that must
distinguish "discard this mutation" from "the environment moved" should read
a field, not a number.

**`error` is that second thing, and it is not a score.** The rule is:
`error` means the run could not complete for a reason that is not the
source's fault — an unreadable pin set, an unwritable cache or pin path, a
closure that would not resolve, an os error partway through a clean, a
harness or dispatch that would not start. The loop retries or escalates it;
it never scores it. The other four outcomes are findings about a source or a
workspace, and a loop that treated an unwritable temp directory as a failing
candidate would be scoring its own filesystem. This is also what closes the
gap the paragraph above opened: the promise was that *every* jsonl stream
ends with an outcome object, but environment failures used to print prose to
stderr and end the stream with nothing at all — precisely the silence the
decision exists to abolish, arriving on exactly the paths a machine consumer
is least able to interpret. The prose stays on stderr, unchanged; the object
carries the same text, bounded, so a consumer reading only the stream still
learns what went wrong. Usage errors keep emitting nothing (exit 2): a
malformed invocation never ran, and an outcome object would report on a run
that did not happen. `run` is included, its stdout being the wire event
stream itself — and there the terminator is an event rather than a bare
object. A bare `{"outcome":"ok"}` on a stream where every other line carries
`schema_version`, `sequence`, `type`, `harness`, `conversation`, and
`fingerprint` forces a consumer to special-case its own stream's last line
to decode it at all. So run ends with a `run.completed` event: the next
sequence number, the full envelope, and the `outcome` field no other event
carries, which is what distinguishes it. It also carries `turns` — the
counts of the turns the dispatch ran by terminal status — because run's
`ok` means only that **the dispatcher completed every turn it was given**,
whatever those turns' own statuses. A run whose every turn failed is a run
that finished; it ends `ok`, and a loop scores it from `turns`, not from the
outcome. The failure paths are `run.completed` events too, `gate_failed`
with the source digest and no fingerprint (the gate minted none, and an
empty field says so honestly) or `error` with the bounded prose.

Two commands are exempt, for opposite reasons. `schedule` has no
`--format` and no machine-readable stream to terminate — its output is a
prose lifecycle stream — so it emits no outcome object at all. `mcp serve`
does have a stream, and that is the problem: its stdout is the MCP protocol,
so an outcome object written there would corrupt what its consumer is
parsing. It therefore refuses `--format jsonl` as a usage error rather than
accept the flag and silently ignore it, which would promise a terminator
that never comes.

**A failed gate emits a `source_digest`.** Rejected candidates are data: a
loop that discards a mutation still wants to name it, and until now the only
name a failed candidate had was the identifier set that rejected it. The
`gate_failed` object now carries `source_digest`, a content hash over the
authored files, so a rejected candidate is attributable without a loop
hashing the tree itself.

The rule that keeps it honest is the naming: **a digest names bytes, a
fingerprint names a proven configuration.** A consumer joins failures by
digest and successes by fingerprint, and never confuses the two. The
fingerprint's whole value is that ADR 0025 mints it only from a passing
gate; a hash of a source that does not load carries none of that proof, and
letting the two share a name — or a value — would dilute exactly the
property the fingerprint exists for. So the two are separated by
construction, not by convention: the digest is hashed under its own domain
prefix, so a source's digest always differs from that tree's fingerprint.
The separation is in the values, not in their appearance — both render as
`sha256:` and 64 hex characters, and a bare string cannot be classified by
looking at it; the field a value arrives in is what carries the meaning.
The fields never appear together either — a passing run carries a
fingerprint and no digest, a failing one the reverse.

It is computed from whatever the loader inventoried, and otherwise by
walking the agent root for exactly the names the loader itself reads:
`instructions.md`, the component directories, and the native tool
dependency files the fingerprint inventories at the root. That allowlist is
the point. A walk that hashed everything it was not told to skip would fold
in the output a fresh apply generates (the default workspace is the agent
directory itself), and worse, `.git/` — which mutates on every fetch and
checkout, so the digest of an unchanged source would change under it.
Each entry contributes its path, its content hash, and its executable bit,
the same authored intent the fingerprint covers. Both paths are
deterministic for a given tree. The field is omitted only when the root
itself cannot be read: there are no bytes to name, and a placeholder would
be a name for nothing.

**A named `--harness` clean asserts that harness was applied.** A bare
`clean` over a workspace with no records is the idempotent no-op it always
was. `clean --harness H` over a workspace holding no `apply-H.json` record
is not: the operator asserted something about the workspace that is false,
and exiting 0 would report "that harness is now clean" about files tenon
never wrote. It exits 1 with the `error` outcome — an argument that does not
match the environment, not a refusal to remove something, which is what
`blocked` is for.

## Consequences

- The read surface is one command with flags instead of three commands with
  a documented invariant between them, and the invariant cannot rot.
- ADR 0025's atoms are unaffected in substance: the fingerprint is still
  minted only by a passing gate, still commit-free, still deterministic,
  still joinable. Only the command that prints it changed name.
- A capability listing exists for the first time, and the one-way rule is
  what keeps it from becoming the registry the folder already is. Any future
  feature that wants to *read* a catalog should be treated as a proposal to
  re-decide that invariant, and refused on those terms unless it argues them.
- Diagnostic identifier stability now starts from `pins.*`. The pre-release
  window that made this rename cheap is closed once v0.1.0 ships.
- Clean gives tenon a removal path, and with it the obligation not to delete
  what it did not write. The all-or-nothing refusal is deliberately stricter
  than per-file skipping: a partially uninstalled workspace is a state
  nobody asked for and nothing else in tenon can describe.
- Four flag and command renames land at once. They are correct and they are
  cheap exactly now, and every one of them would be expensive later.

## Settled: `pins`, not `lock`

The question was whether the file should be called a lockfile, since
everyone recognizes the word and it carries the fail-closed connotation this
file genuinely has. It is settled as **`pins`, final**, before the
identifier stability window closes, on two grounds.

A lockfile connotes a *resolved dependency graph the tool computed*. This
file records versions of things tenon does not resolve and does not install:
the harness executable somebody else put on the PATH, integration package
identities, tool runtime versions. Calling it a lock would promise a
resolution step that does not exist, which is the same failure `manifest`
had — a name whose first sentence has to be a denial.

And one of its fields could not honor the promise even if the rest could.
The `model` field is advisory: operator-supplied, never resolved
automatically, and never verified, because the harness owns model selection
and tenon does not claim to know which model served a turn. `lock` says
"verify against this and fail closed"; over that field there is nothing to
verify, so the connotation would be dishonest for part of the file's own
contents.

`pins` says exactly what the file holds and no more. That is the whole
argument: it claims less, and it delivers all of what it claims.

