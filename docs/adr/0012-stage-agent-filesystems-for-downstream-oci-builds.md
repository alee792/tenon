# ADR 0012: Stage agent filesystems for downstream OCI builds

- Status: accepted
- Re-records: prototype ADR 0027 (alee792/hctl)

## Decision

Add an OCI-neutral staging boundary for deployable agents: tenon prepares one
complete runnable filesystem tree that an existing build system copies onto a
documented compatible base image. Tenon does not construct OCI manifests or
layers, contact a registry, publish, sign, deploy, or operate the resulting
image.

Publish one tenon harness image for Codex and one for Claude, each carrying
the matching tenon release, one pinned native harness, and all supported
authored-tool build and execution inputs. Two journeys are supported: apply
an agent directly in that image and ship the resulting larger image, or use
the image as a disposable build stage whose staging step carries only the
selected agent's required execution closure into a clean compatible image:

```dockerfile
FROM <tenon harness image for codex> AS build
COPY . /agent
RUN tenon stage /agent --harness codex --output /out/agent

FROM DOCUMENTED_COMPATIBLE_BASE
COPY --from=build /out/agent/opt/ /opt/
COPY --from=build --chown=65532:65532 /out/agent/workspace/ /workspace/
COPY --from=build --chown=65532:65532 /out/agent/home/tenon/ /home/tenon/
USER 65532:65532
ENTRYPOINT ["/opt/tenon/bin/agent-entrypoint"]
```

The two-stage form is recommended when image size, build-tool exposure, or
offline startup matters; it is not required for correctness. Publishing
either harness image remains separate, explicitly authorized work gated on
its acceptance contract and the harness vendor's redistribution terms.

### Staged layout (exact published contract)

Downstream builds copy the tree by these canonical paths, so they are
contract, not rendering:

```text
/opt/tenon/artifact.json
/opt/tenon/bin/tenon
/opt/tenon/bin/agent-entrypoint
/opt/tenon/harness/...
/opt/tenon/runtimes/...
/opt/tenon/agents/AGENT_NAME/...
/workspace/...
/home/tenon/...
```

`AGENT_NAME` is the normalized authored project name, preserving the
directory-derived identity. Immutable agent source and the generated
workspace stay separate per
[ADR 0003](0003-separate-agent-source-from-workspace.md); everything the
staged runtime references uses these final paths, never build-stage paths. A
deployment must preserve the staged workspace files and make its
runtime-owned portions writable; mounting an empty volume over the whole
workspace hides the required integration and is not a supported composition.
The staged entrypoint runs the staged tenon, agent source, workspace, and
harness selection without a downstream shell wrapper, requires the documented
non-root runtime identity (UID/GID 65532, home `/home/tenon`), and verifies
runtime identity, generated integration, and source fingerprint before a
turn.

### Responsibilities and outcomes

- **Complete and minimal.** The staged tree carries tenon, the selected
  harness, immutable agent source, the generated integration and apply
  record, an entrypoint, an artifact manifest, and only the execution closure
  the agent's authored tools actually need — the union of discovered
  language-runtime requirements, nothing more. A tool-free agent carries no
  language runtime; build-only compilers, caches, and inspection output are
  discarded.
- **Verifiable.** The artifact manifest is generated bookkeeping, not author
  configuration: a schema-versioned, content-complete record of identity
  (agent, harness and version, tenon version, source fingerprint, target
  platform and ABI, compatible base) and of every staged file's hash, mode,
  and intended runtime ownership, sufficient to verify the tree offline.
- **Deterministic and atomic.** Identical agent source and pinned image
  inputs produce identical staged contents and metadata; preparation never
  mutates authored source, and the tree is published with one rename only
  after the manifest is complete. Downstream image metadata, timestamps,
  compression, and digests belong to the downstream builder.
- **Platform-honest.** Staging targets the source image's own OS,
  architecture, and ABI; cross-compilation is not promised, and each
  published harness image declares one exact compatible final-base contract
  (shared libraries, certificate and shell facilities, runtime identity,
  writable home). A payload is never copied onto an incompatible base merely
  because the OS matches.
- **Credential-free.** The staged tree excludes all runtime-created or
  operator-owned state: model-provider credentials and login state,
  user-level harness configuration and trust decisions, tenon user
  configuration, dispatch and session state, and registry or deployment
  material. Staging never reads credentials from its environment or inputs
  and never contacts a model, validates live credentials, or creates a
  native trust decision. Authored source remains prohibited from containing
  credentials, and tenon does not claim staging can reliably detect a hidden
  secret.

The selected native harness still owns model calls, native tools, approvals,
and sandbox behavior; packaging the processes together does not expand
tenon's policy boundary. This decision grants no permission to redistribute
a native harness: publishing an image requires a current review of the
vendor's terms, and tenon must not substitute an unverified runtime download
to preserve the journey.

## Context

[ADR 0005](0005-first-install-release-archive.md) chose a single release
archive for local installation and rejected speculative copying of workspace
caches; that remains the local journey. A concrete distribution need exists:
authors want normal OCI build systems to consume a tenon-prepared agent
without carrying every compiler and unused runtime into the final image. Raw
workspace caches are not a sufficient artifact — prepared state binds
absolute paths and platforms — so staging re-prepares and verifies inside the
target image and publishes a manifest for the complete closure.

The alternatives were shipping the large all-runtimes image everywhere,
publishing a combinatorial matrix of harness-and-language images, or making
tenon an OCI builder that duplicates mature build and registry systems.
Selective staging keeps tenon responsible for the compilation facts it
already knows while leaving image construction to existing tools.

## Consequence

ADR 0003's separation defines the two canonical staged locations. ADR 0005
remains authoritative for local installation; its deferral of relocatable
packaging is superseded only for this bounded staging contract — there is
still no `tenon package` command and no license to copy arbitrary caches
between machines. Downstream users own their final base contents, image
metadata, signing, publication, runtime credentials, mounts, network policy,
and deployment platform; tenon cannot claim reproducibility or safety for
changes made after the staged tree leaves its boundary.
