# ADR 0005: Use a release archive for first installation

- Status: accepted
- Re-records: prototype ADR 0007 (alee792/hctl)

## Decision

The first supported platform is `darwin-arm64`, the one platform exercised by
the prototype's clean-install proof. The exact `vX.Y.Z` Git tag is the
authoritative release version. It produces
`tenon_X.Y.Z_darwin_arm64.tar.gz`, containing one `tenon` executable at the
archive root, and `tenon_X.Y.Z_SHA256SUMS`, containing that archive's SHA-256
checksum. A user verifies the exact release, extracts the executable to a
stable location on `PATH`, and applies portable agent source to a workspace.

Generated MCP configuration records the resolved absolute path to that
executable. Moving the binary requires `tenon apply` again. Replacing the
binary at the same path leaves the reference valid, but the supported upgrade
journey reruns `apply` to refresh any runtime cache. There is no `tenon
package` command.

## Context

The clean-install journey builds an isolated binary, copies an agent project
and workspace outside the checkout, and proves that the generated MCP server
starts from the installed executable. The runtime reads agent source directly
and keeps generated host files, native dependency environments, executable
receipts, and compiled Go hosts in a fingerprinted workspace-local cache
directory. They are disposable and must be rebuilt by `apply` on another
machine.

`go install` would require users to supply a Go toolchain and resolve source or
a module version, rather than consuming the checked released artifact. It does
not improve the first cross-platform journey. A relocatable package would need
to define how it bundles source, lockfiles, native runtimes, and caches, but
the source-plus-apply contract has no demonstrated need for that extra
surface. [ADR 0012](0012-stage-agent-filesystems-for-downstream-oci-builds.md)
defines a bounded staged filesystem artifact for downstream OCI builds without
changing this local installation journey or making arbitrary workspace caches
portable.

## Consequence

Release tooling must accept only an exact `vX.Y.Z` Git tag as its version
source, produce the named `darwin-arm64` archive and checksum manifest without
publishing them, document the installation commands, and make the
credential-free proof extract and use the archive. It must not introduce an
agent-image or deployment system, copy workspace caches between machines, add
a `tenon package` command, or claim another platform. A future relocatable
package or platform matrix needs a separate, evidence-backed decision;
ADR 0012 supplies that separate decision for canonical staged filesystems and
does not add a general package command or supersede this release archive.
