# Security policy

## Reporting a vulnerability

Report suspected vulnerabilities privately through GitHub's private
vulnerability reporting on this repository (the repository's Security tab →
"Report a vulnerability"). Do not open a public issue for a suspected
vulnerability.

## Scope

In scope: tenon's own code — validation, compilation to native harness
configuration, the managed MCP server, dispatch, scheduling, staging, and
the integration package store. Out of scope: the native harnesses
themselves (Claude Code, Codex) and any MCP server or authored tool they
run, which are governed by their own security processes.

## Tenon's stance

Tenon's own diagnostics never expose credentials, private prompts, or raw
process output; native harness and external-server diagnostics remain
outside that claim, per
[the product specification's failure and safety behavior](docs/product-spec.md#failure-and-safety-behavior).
Tenon also never claims to sandbox authored code: an authored tool under
`tools/` is the author's own code, reviewed and adopted like any other
dependency, and tenon does not claim to enforce instructions, inspect
native effects, or make model behavior safe from outside the harness.
