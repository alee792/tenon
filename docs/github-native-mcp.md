# Native GitHub MCP

An agent asks for GitHub by adding one file. `mcp/github.md` names GitHub's
hosted MCP endpoint; tenon renders it into native Claude Code or Codex project
configuration; the harness dials it, discovers that it needs authorization, and
runs the OAuth flow in the operator's browser. There is no GitHub SDK, no
provider adapter, no token in agent source, and no manual MCP JSON editing.

Tenon's whole contribution here is the file, the composition around it, and the
rendering. It never contacts GitHub — not while authoring, not during
`check`, not during `apply`, not during `stage`.

> **Unmanaged effect boundary:** the authorization the harness obtains is
> whatever the operator consented to in the browser, and it lives in
> harness-owned storage tenon neither writes nor reads. Claude Code or Codex,
> the model-accessible shell and execution tools, plugins, and other processes
> with access to that storage may use it. Tenon does not filter, confirm,
> broker, authorize, observe, or audit native GitHub calls. Consent to the
> narrowest scope the work needs, review what the agent can reach, and do not
> expose a valuable account to untrusted input. A read-only workspace does not
> make GitHub effects read-only.

## The journey

Write the connection into the exact agent root. `tenon mcp add` writes it for
you, offline:

```sh
tenon mcp add ./my-agent github --url https://api.githubcopilot.com/mcp/ \
  --context 'Use the discovered GitHub tools for repository, issue, and pull-request work.'
```

The result is ordinary versioned source — four lines of frontmatter and prose,
which you may just as well type by hand:

```text
my-agent/
  instructions.md
  mcp/
    github.md
```

```md
---
type: streamable-http
url: https://api.githubcopilot.com/mcp/
---

Use the discovered GitHub tools for repository, issue, and pull-request work.
```

The frontmatter is the [Agent Plugins 1.0](https://agent-plugins.org/specification)
`mcp.json` server-entry vocabulary, unchanged: a server entry copied from a
vendor's README works here as written. The filename supplies the native server
name. The body is bounded model-facing guidance rendered once into generated
instructions, alongside one statement that the native harness owns MCP startup,
trust, approval, authentication, discovery, calls, and effects.

Check the composed surface offline:

```sh
tenon mcp status ./my-agent github
```

```text
github: target=remote transport=streamable-http url=https://api.githubcopilot.com/mcp/ headers=0 context present configured runtime=unchecked (mcp/github.md)
```

`runtime=unchecked` is the honest word: status proves the declaration, not the
server. Apply to the intended workspace explicitly:

```sh
mkdir -p ./my-workspace
tenon apply ./my-agent --workspace ./my-workspace --harness claude
# or
tenon apply ./my-agent --workspace ./my-workspace --harness codex
```

Launch the harness normally from the applied workspace:

```sh
cd ./my-workspace
claude
# or
codex
```

The first time the agent reaches for a GitHub tool, the harness dials the
endpoint, receives `401` with the authorization server it advertises, and runs
OAuth 2.1 with PKCE — a browser consent screen you complete once. That is the
whole credential story: no PAT to mint, export, rotate, or unset.

## Who owns what

| Concern | Owner |
| --- | --- |
| The URL and the guidance prose | The author, in `mcp/github.md` |
| Rendering them into `.mcp.json` / `.codex/config.toml` | Tenon, at apply |
| Discovering that authorization is required | The harness, at connect time |
| The OAuth flow, the consent grant, and the token | The harness and the operator's browser |
| Token storage, refresh, and revocation | The harness (and GitHub) |
| Server approval, tool approval, and project trust | The harness's own prompts |
| Which tools exist and what they do | GitHub's hosted server |

Tenon holds no token, writes no credential store, and refreshes nothing — per
[ADR 0006](adr/0006-use-a-local-secretless-operation-broker.md) it stays
secretless. Inspect the generated project before accepting the harness's trust
and approval prompts; tenon grants nothing because `mcp/github.md` exists.

Native Git and `gh` authentication remain separate, operator-owned setup. The
MCP surface does not authenticate either, and does not promise publication of an
exact local branch and history.

## What is not pinned

A pinned executable freezes a tool catalog; a hosted endpoint does not. The
project fingerprint covers *declared source* — the URL, any headers, the
guidance body, and the staged bytes of anything local — and has never covered a
remote server's behavior. GitHub may add, remove, or reshape tools under an
unchanged fingerprint, and tenon will not notice. This is a knowing trade
recorded in
[ADR 0026](adr/0026-author-remote-first-spec-aligned-mcp.md); probing remote
catalogs for drift is a possible future `tenon mcp status --probe` and is
deliberately out of scope today.

## If the endpoint needs a header

Some remote servers want a header rather than OAuth. A header value is either a
literal containing no `$`, or an optional literal prefix followed by exactly one
`${VAR}` reference:

```sh
tenon mcp add ./my-agent example --url https://mcp.example.com/mcp/ \
  --header 'Authorization: Bearer ${EXAMPLE_TOKEN}'
```

Tenon never reads, resolves, copies, or retains the *value*; only the variable
*name* is written into generated configuration, since emitting the reference is
the entire point, and the harness's own process environment is what resolves it.
Literal header values are package-visible configuration and must not contain
secrets — tenon claims no heuristic for recognizing one. Codex's generated
configuration has no header support at all, so headers on a Codex rendering are
warned and omitted (`mcp.header.not-honored`) rather than silently mangled.

GitHub's hosted endpoint needs none of this.

## Troubleshooting

| Symptom | Owner and action |
| --- | --- |
| The consent screen never appears | Complete the harness's own MCP server approval first; a server the harness has not been allowed to start never dials the endpoint. Inspect the harness's native MCP diagnostics. |
| Codex refuses the project or the tool | Establish Codex project trust, then native server and per-tool approval. Missing project trust fails launch; missing optional server approval simply leaves GitHub unavailable. |
| Authorization is rejected or expired | Re-run the harness's own authorization for the server and, if needed, revoke and re-grant the consent in GitHub. This is a harness and GitHub failure; tenon does not intercept or reclassify it. |
| A tool the agent's prose names does not exist | Tenon does not freeze tool names or schemas; the hosted catalog is GitHub's. Update the guidance body and reapply. |
| Generated name collision | An authored `mcp/github.md` beats a plugin-provided `github` server with a warning naming both sources; two plugins declaring it resolve first-wins. To suppress a plugin's server with no replacement, author a masking file. `managed` is reserved and unmaskable. |
| Native Git or `gh` cannot push | Configure their authentication separately. GitHub MCP does not promise exact local branch publication. |
| The workspace looks stale after editing source | A plain `claude` or `codex` launch does not call tenon. Reapply the workspace, then restart the harness. |

## Deferred: the curated PAT package journey

Everything below describes ADR 0015's original journey: a curated
`github/github-mcp-server` package installed into tenon's operator-owned
integration store, launched over stdio, authenticated by a personal access
token in the harness's environment. [ADR 0026](adr/0026-author-remote-first-spec-aligned-mcp.md)
deferred it as the *reference* journey; it is not withdrawn. Every code path
below still exists, is still tested, and remains the answer for an air-gapped
machine, an organization whose policy forbids the hosted endpoint or the OAuth
grant, or an operator who wants the tool catalog pinned to an exact executable.
It costs a platform matrix, a package materialization, a trust ceremony, and a
credential the harness environment carries — which is exactly why it is no
longer what a new author is asked to do first.

> **Unmanaged credential and effect boundary:** Claude Code or Codex, the
> model-accessible shell and execution tools, plugins, the official server, and
> other processes inheriting the launch environment may read or transmit
> `GITHUB_PERSONAL_ACCESS_TOKEN`. Tenon does not filter, confirm, broker,
> authorize, observe, or audit native GitHub calls. Use a fine-grained PAT
> limited to the required repositories and permissions, give it a short
> expiration, isolate the runtime identity, and do not expose valuable
> credentials to untrusted input. A read-only workspace does not make GitHub
> effects read-only.

### Local journey

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

Add the connection to the exact agent root. `tenon mcp add` does not author
installed targets (see the specification's Known limitations), so write the file
directly and check it offline:

```sh
tenon mcp status ./my-agent github
```

```md
---
type: installed
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
because `mcp/github.md` exists. After approval, ask the harness to use the live
discovered GitHub tools; tenon does not freeze their names or schemas.

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

### Service or container journey

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
injection with protection from the harness or model: every warning above still
applies.

An already-running tenon service or container keeps the environment it received
at its own start. Every concurrent harness child and replacement after
hibernation inherits that unchanged parent environment, not a later change in
the caller's shell or secret manager. After rotating runtime injection, restart
the owning tenon service or container; allowing it to open another child is not
a credential refresh. Tenon does not snapshot the PAT during apply or propagate
an in-process rotation.

### Package lifecycle and offline reuse

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
offline. `tenon mcp status ./my-agent github` separately reports the
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

1. run `tenon mcp remove AGENT github` for every consuming agent source;
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
documented behavior. An agent without an installed `mcp/github.md` neither
resolves nor stages this package. `tenon drift` and `tenon mcp serve`
re-resolve current package state before they run. Plain direct Claude or
Codex launches do not; their generated
configuration remains unchanged until reapply.

### Deferred-journey troubleshooting

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
| Update or removal appears stale | Plain Claude or Codex does not call tenon before launch. For update, verify the new identity, reapply local consumers, rebuild agent images, then restart or redeploy. For removal, restore/enable the package if necessary and follow the connection-removal, local reapply/image rebuild, package-removal, restart order above. Tenon re-verifies package state in `drift` and `mcp serve`, never in a launch it is not part of. |
| A rotated PAT appears stale | Restart a directly launched local harness from the updated shell. For headless, concurrent, or hibernated sessions, restart the owning tenon service/container so later child processes inherit the new injected environment. |

Every claim on this page must be proven by credential-free tests before the
journey it describes is offered; live GitHub acceptance — OAuth or PAT — is
optional and requires explicit authorization.
