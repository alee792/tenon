# Native GitHub MCP

An agent requests GitHub through the generic `connections/github.md` installed
target. The file selects the operator-installed official
`github/github-mcp-server` package and its `github` capability; tenon maps that
selection into native Claude Code or Codex project configuration. There is no
GitHub SDK, provider adapter, or manual MCP JSON editing in this journey.

> **Unmanaged credential and effect boundary:** Claude Code or Codex, the
> model-accessible shell and execution tools, plugins, the official server, and
> other processes inheriting the launch environment may read or transmit
> `GITHUB_PERSONAL_ACCESS_TOKEN`. Tenon does not filter, confirm, broker,
> authorize, observe, or audit native GitHub calls. Use a fine-grained PAT
> limited to the required repositories and permissions, give it a short
> expiration, isolate the runtime identity, and do not expose valuable
> credentials to untrusted input. A read-only workspace does not make GitHub
> effects read-only.

## Local journey

The curated package supports Darwin/arm64 and Linux/amd64. Prepare a reviewed
local materialization of the pinned official package, then install and
explicitly trust it once:

```sh
tenon integration install /trusted/path/to/github-mcp-server-package --trust operator
tenon integration inspect github-mcp-server
tenon integration verify github-mcp-server
```

`inspect` reports the installed identity, enablement, provenance, capability,
and required environment *name* with `value=not-read`. `verify` is the offline
status check for every cached byte. Neither command starts the package or reads
the PAT.

Add the connection to the exact agent root. `tenon connection add` does not
author installed targets yet (see the specification's Known limitations), so
write the file directly and check it offline:

```sh
tenon connection status ./my-agent github
```

The connection is ordinary versioned source:

```text
my-agent/
  instructions.md
  connections/
    github.md
```

For example:

```md
---
type: mcp
package: github-mcp-server
capability: github
---

Use the official native GitHub tools discovered by the harness for repository
issues and pull requests.
```

Connection discovery and exact package resolution are offline. Apply to the
intended workspace explicitly; the installed package, not `PATH`, supplies the
exact executable:

```sh
mkdir -p ./my-workspace
tenon apply ./my-agent --workspace ./my-workspace --harness claude
# or
tenon apply ./my-agent --workspace ./my-workspace --harness codex
```

Create a fine-grained, repository-scoped PAT in GitHub with only the permissions
needed for this session and a short expiration. Export it only in the trusted
runtime shell. Avoid putting the value in shell history; this example reads it
without echoing:

```sh
IFS= read -r -s GITHUB_PERSONAL_ACCESS_TOKEN
export GITHUB_PERSONAL_ACCESS_TOKEN
```

Launch the harness normally from the applied workspace:

```sh
cd ./my-workspace
claude
# or
codex
```

Claude owns its one-time project MCP server approval. Codex first owns project
trust, then native server and per-tool approval. Inspect the generated project
and installed package before accepting those prompts. Tenon does not grant trust
because `connections/github.md` exists. After approval, ask the harness to use
the live discovered GitHub tools; tenon does not freeze their names or schemas.

Native Git and `gh` authentication are separate, operator-owned setup. The MCP
PAT does not authenticate either, and the official MCP surface does not promise
publication of an exact local branch and history.

Unset the variable when the session ends and revoke the temporary PAT when it
is no longer needed:

```sh
unset GITHUB_PERSONAL_ACCESS_TOKEN
```

Changing or removing the variable does not alter a process that already
inherited it. For a directly launched local Claude or Codex process, stop it
and launch a new process from the updated shell environment.

## Service or container journey

Installation, apply, image build, and staging remain credential-free. Prepare
and trust the Linux/amd64 package in the build environment before `apply` or
`stage`. Never use a Dockerfile `ARG` or `ENV`, build-secret copy, source file, package
manifest, or generated harness file to carry the PAT.

Inject the value only when the already-built image starts. For example, after
placing it in the caller's trusted environment, launch the native Codex
interface from an already-applied agent image:

```sh
docker run --rm -it \
  --env GITHUB_PERSONAL_ACCESS_TOKEN \
  --entrypoint /opt/tenon/harness/bin/codex \
  AGENT_IMAGE@sha256:DIGEST
```

For a headless image whose entrypoint is the generated tenon agent entrypoint,
inject the same variable while selecting its bounded JSONL input:

```sh
printf '%s\n' '{"input_id":"service-1","text":"Inspect the allowlisted repository"}' \
  | docker run --rm -i --env GITHUB_PERSONAL_ACCESS_TOKEN \
      AGENT_IMAGE@sha256:DIGEST --input jsonl
```

A service manager, container orchestrator, or external secret manager should
likewise resolve the secret into the harness process environment at runtime.
The deployment must deliberately establish the harness's supported project,
server, and tool trust before unattended launch. Do not confuse runtime secret
injection with protection from the harness or model: every warning at the top
of this page still applies.

An already-running tenon service or container keeps the environment it received
at its own start. Every concurrent harness child and replacement after
hibernation inherits that unchanged parent environment, not a later change in
the caller's shell or secret manager. After rotating runtime injection, restart
the owning tenon service or container; allowing it to open another child is not
a credential refresh. Tenon does not snapshot the PAT during apply or propagate
an in-process rotation.

## Package lifecycle and offline reuse

These are lifecycle reference commands for the operator-owned machine store,
not a sequence to run top to bottom:

```sh
tenon integration list
tenon integration inspect github-mcp-server
tenon integration verify github-mcp-server
tenon integration disable github-mcp-server
tenon integration enable github-mcp-server
tenon integration remove github-mcp-server
```

There is no separate integration-package `status` command: `inspect` reports
selected package state and `verify` proves the complete installed closure
offline. `tenon connection status ./my-agent github` separately reports the
authored selection and its offline resolution health. Disablement removes the
package from future resolution without deleting metadata. Removal retires the
selected record and retains immutable shared cache bytes. Reinstalling the same
exact package can reuse that cache.

For a reviewed future package source, update the selected immutable identity
explicitly rather than rerunning install against a changed identity:

```sh
tenon integration update github-mcp-server /trusted/path/to/package \
  --trust operator
tenon integration verify github-mcp-server
tenon apply ./my-agent --workspace ./my-workspace --harness codex
```

Reapply every local workspace that consumes GitHub. Rebuild any direct or
staged agent image from the updated trusted package rather than changing an
already-built image. Then restart each direct native harness or deploy a new
owning tenon service/container. A plain `claude` or `codex` launch uses the exact
path already embedded in generated project configuration; it does not ask tenon
to resolve the current package again.

Remove the package in this order so a workspace never retains an apparently
current stale entry:

1. run `tenon connection remove AGENT github` for every consuming agent source;
2. reapply every local workspace so its generated native GitHub entry is
   removed, and rebuild staged outputs/images without the connection;
3. run `tenon integration remove github-mcp-server`; and
4. restart direct harnesses and owning tenon services or containers.

Removing or disabling the package first makes a still-declared connection fail
reapply and does not erase previously generated native configuration. If that
happens, restore or enable the exact package, follow the order above, and then
remove or disable it.

Connection discovery and package resolution during apply and stage are offline
and verify current package state; other apply responsibilities retain their
documented behavior. An agent without `connections/github.md` neither resolves
nor stages this package. Tenon-owned scheduled, channel, and continuation
process opens re-resolve current package state before opening a native child.
Plain direct Claude or Codex launches do not; their generated configuration
remains unchanged until reapply.

## Troubleshooting

| Symptom | Owner and action |
| --- | --- |
| Package is missing | Run the explicit curated installer, or install a reviewed local materialization with `tenon integration install SOURCE --trust operator`. Apply never downloads it. |
| Package is disabled or untrusted | Inspect the package. Use `enable` only after operator review; portable agent source cannot grant trust. |
| Unsupported platform | The curated package supports only Darwin/arm64 and Linux/amd64. Use a supported host; do not copy a foreign closure. |
| Verification reports corruption | Do not launch it. This delivery has no cache-repair or garbage-collection command, and `remove` deliberately retains cache bytes. Update to a reviewed different immutable package identity, or re-provision the operator-owned integration store through an approved recovery procedure; never bypass verification or edit the cached executable. |
| `authentication required` | Inject a non-empty `GITHUB_PERSONAL_ACCESS_TOKEN` into the harness launch environment and restart the process. Apply does not need it. |
| GitHub reports invalid, expired, or insufficient authorization | Replace or correct the fine-grained PAT and restart. This remains an official-server, GitHub, and harness failure; tenon does not intercept or reclassify it. |
| Claude cannot start the server | Complete the native project MCP approval and inspect Claude's native MCP diagnostics. GitHub is optional, so unrelated managed tools remain available. |
| Codex refuses the project or tool | Establish Codex project trust, then native server and tool approval. Missing project trust fails launch; missing optional server/tool approval leaves GitHub unavailable. |
| Generated name collision | Remove the authored/plugin `github` server or the connection. Tenon rejects rather than renames or overrides it. Harness-owned higher-precedence configuration remains a native diagnostic. |
| Update or removal appears stale | Plain Claude or Codex does not call tenon before launch. For update, verify the new identity, reapply local consumers, rebuild agent images, then restart or redeploy. For removal, restore/enable the package if necessary and follow the connection-removal, local reapply/image rebuild, package-removal, restart order above. Tenon re-verifies only its own scheduled, channel, and continuation opens. |
| A rotated PAT appears stale | Restart a directly launched local harness from the updated shell. For headless, concurrent, or hibernated sessions, restart the owning tenon service/container so later child processes inherit the new injected environment. |
| Native Git or `gh` cannot push | Configure their authentication separately. GitHub MCP does not promise exact local branch publication. |

Every claim above must be proven by credential-free tests before this journey
is offered; live GitHub acceptance is optional and requires explicit
authorization with a temporary least-privilege PAT.
