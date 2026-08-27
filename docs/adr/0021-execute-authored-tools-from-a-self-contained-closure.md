# ADR 0021: Execute authored tools from a self-contained runtime closure

- Status: accepted
- Completes: [ADR 0012](0012-stage-agent-filesystems-for-downstream-oci-builds.md)'s
  execution-closure commitment for authored tools
- Re-decides: prototype ADR 0004's tool-host execution mechanics
  (alee792/hctl), the record its rebuild notes deliberately left for
  re-decision on new evidence; ports staging intent from prototype ADR 0027

## Decision

Every authored-tool language host is prepared as one self-contained closure
directory, and launching that host means exec'ing one file inside it. A
running host may reference exactly three roots — its own closure, the
immutable agent source, and the workspace — and nothing else: no PATH
lookup, no build tool, no interpreter outside the closure, no absolute path
into the machine that prepared it.

This is one contract, not a staging mode. `tenon apply` prepares the same
closure the staged tree carries, and `tenon mcp serve` launches it the same
way in a workspace and in a container, so every credential-free test of the
local journey exercises the identical artifact an image ships. A second
container-only launch path is exactly the kind of surface that drifts
unseen, and is rejected here by construction.

### The line: runtimes in, toolchains out

The closure includes the language *runtime* and excludes *acquisition and
build* tools. Concretely:

- **Go** already satisfies the contract: the prepared closure is a
  statically linked host binary plus its generation inputs, and launch is
  exec'ing that binary. The Go toolchain that built it stays outside.
- **Python** drops the virtual environment entirely. Preparation installs a
  pinned, checksum-verified standalone CPython into the closure and lays the
  locked dependencies flat beside it (`uv export --locked` followed by a
  hash-required `--target` install); launch execs that interpreter directly
  on the generated host with the dependency directory added as a site
  directory. `uv` remains a preparation-time tool and never runs at serve
  time; the venv machinery — `pyvenv.cfg`, activation scripts, the
  interpreter symlink — never exists, so nothing points outside the closure.
- **TypeScript** follows the same contract; its rendering is a bounded spike
  (per tenet 4) with `deno compile` as the candidate — the direct analog of
  the Go host, with tool modules passed via `--include` — and the
  prototype-proven fallback of copying the single self-contained `deno`
  executable into the closure beside a pruned, cached-only `DENO_DIR`
  (hctl served a real tool call this way from a clean base image with no
  network; its prune list, including Deno's `node_compat_bin` link back to
  the build-time executable, is the starting point). Both satisfy the
  contract; the spike picks between them. Until it lands, staging *refuses*
  a TypeScript-bearing agent with a named diagnostic rather than emitting a
  tree that cannot run.

Deno-as-runtime and CPython are runtime; `uv`, the Go toolchain, and
deno-as-compiler are build tools and stay in the build image, per ADR 0012's
"build-only compilers, caches, and inspection output are discarded".

### Acquired runtimes are pinned, verified, and recorded

ADR 0012's prohibition on substituting an unverified runtime download
governs the native harness, whose redistribution terms tenon does not own.
It does not prohibit this: a language runtime enters the closure only
through an acquisition whose exact artifact is pinned by checksum ahead of
time (for CPython, the per-interpreter sha256 baked into the pinned uv
release), and every byte it contributes lands in the artifact manifest with
its own hash, mode, and ownership. An acquisition that cannot be
pre-verified against a recorded digest fails preparation; nothing is fetched
at serve time or in the runtime image.

The staged manifest's runtime record grows the interpreter identity and ABI
(for example `cpython-3.11.13-linux-x86_64-gnu`), so ADR 0012's
"one exact compatible final-base contract" names the libc a payload
requires, and a mismatched base is refused by fact rather than discovered by
crash.

### Staging normalizes, then proves, relocatability

Preparation may write machine-local paths; publication may not carry them.
Before the single publishing rename, staging normalizes the closure for its
final canonical paths — the same substitution the generated integration
already performs — deleting the runtime's internal convenience symlinks and
rewriting the enumerated files that embed an absolute preparation path (the
CPython `_sysconfigdata` module; the generated Go host's `go.mod` replace
directive). After normalization, staging fails closed if any non-regular
entry or any build-machine path survives anywhere in the tree — the
prototype's `rejectBuildPaths` scan, widened from generated configuration to
the whole tree. `copyTree`'s
symlink prohibition and the manifest's regular-files-only model are
unchanged: the closure is made symlink-free rather than the guarantee made
symlink-tolerant.

The workspace cache and the staged closure stop being strangers: the staged
apply record names the closure root it was published with, and serving
honors that record — the same owner-checked file serving already trusts —
instead of assuming the workspace cache layout. A staged tree whose closure
is unreachable from its own apply record is a staging bug by definition,
and the acceptance tests for this decision serve tools *from* a staged tree
rather than asserting files exist inside one — ultimately in the prototype's
proof shape: a per-language probe agent staged onto the clean documented
base, its tool called over MCP with networking disabled.

## Context

Staging today delivers no working authored tool in any language, three
defects compounding: the Python venv's interpreter symlink points at the
preparation machine and is (correctly) refused; the Python and TypeScript
hosts launch through `uv run` and `deno run` at absolute preparation-machine
paths that the runtime image does not contain; and the closure is staged at
a path the server never consults, so even the self-contained Go host stages,
verifies clean, and cannot be served. The quiet failures are worse than the
loud one — a tree that verifies and cannot run breaks staging's own promise.

The defects share one root: the execution model differed per language and
between preparation and serving, so the only language that worked was the
one whose closure was accidentally self-contained. ADR 0012 already
committed staging to carry "the union of discovered language-runtime
requirements"; this decision makes that commitment executable by defining
what a language-runtime closure *is*.

The prototype already proved the hard parts and recorded the diagnosis.
hctl's ADR 0027 names raw caches as insufficient in exactly these terms —
receipts binding absolute executables, Python environments encoding their
installation location, platform-specific hosts — and its acceptance script
served real MCP tool calls in all three languages from a clean base image
with no build toolchain and no network, using a checksum-pinned standalone
CPython enforced at a canonical path, materialized symlinks, a rewritten
`pyvenv.cfg`, and a staged-tree scan for surviving build paths. Two things
it did not solve, and this decision corrects rather than ports: its closure
was split between `/opt` runtimes and the workspace cache, joined by a
receipt of absolute paths — the ancestor of the unreachable-closure defect —
where this decision requires one self-contained directory; and it staged
`uv` into every Python image and asserted its presence in acceptance while
the runtime path never executed it, an acquisition tool riding in the
closure against its own stated rule.

Alternatives evaluated and rejected on evidence: `uv venv --relocatable`
rewrites activation scripts and entry-point shebangs but leaves the
interpreter symlink and `pyvenv.cfg home=` absolute — it relocates a venv on
a machine that still has the base interpreter, which the runtime image is
not. Zipapp-style single files cannot load compiled extension modules from
inside the archive, failing on `pydantic` in tenon's own example. PEX-style
self-extracting scies embed the same standalone CPython this decision
stages, wrapped in a runtime extraction step and an opaque blob the
per-file manifest cannot verify. A container-only launch branch in the
server was rejected above. Requiring the interpreter on the base image at a
documented absolute path pushes an ABI-and-path contract onto every
operator to save megabytes in the closure — the wrong side of the
cost-is-what-you-must-know ledger.

## Consequence

`examples/mixed-tools` becomes stageable and its staged image serveable,
which acceptance must prove end to end. Local preparation gains the same
determinism the manifest already claims: today the venv links whatever
interpreter the machine offers, so two machines preparing identical pinned
source could disagree beneath an identical manifest; a pinned interpreter
closes that hole. Closures grow by the runtime they now carry (tens of
megabytes for CPython; comparable for a compiled TypeScript host) —
ADR 0012 already records that minimization is deferred, and that note now
covers a larger, honest number. Preparation on a network-restricted machine
needs the pinned interpreter artifact available through whatever channel
supplies its other pinned inputs; the build-image journey already assumes
this. The agent manifest's tool-runtime pins, which the
distilled specification inherited as Deno, uv, and Go versions, gain the
interpreter identity itself once the interpreter is the pinned artifact;
`uv` remains pinned as a preparation input, no longer implied at serve
time. Until each language's rendering lands, staging refuses that language
with a named diagnostic — a smaller true claim over a broader broken one.
