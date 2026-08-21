# ADR 0008: Dispatch schedules as fresh tasks

- Status: accepted
- Re-records: prototype ADR 0013 (alee792/hctl)
- Count ceilings governed by:
  [ADR 0013](0013-bound-authored-projects-with-aggregate-budgets.md)

## Plain-English summary

An agent author can add a Markdown schedule whose path is its name, whose
frontmatter contains one cron string, and whose body is the task prompt.
Apply validates and fingerprints that source without starting a clock. An
operator can dispatch one occurrence explicitly, and every accepted
occurrence starts a fresh native-harness session so recurring work does not
silently inherit old model context. A caller-owned stable occurrence ID makes
retries deduplicatable. Automatic clocks, deployment registration, and output
delivery are separate concerns;
[ADR 0011](0011-run-schedules-from-a-foreground-utc-clock.md) supplies the
foreground UTC clock.

## Decision

**Authored format (exact).** Root-agent schedules use Eve's Markdown
convention at `schedules/NESTED/NAME.md`. The relative path without `.md` is
the schedule name. Frontmatter contains exactly one string field named
`cron`; it must be a bounded, standard five-field printable-ASCII expression.
The non-empty Markdown body is the prompt; matching Eve, only one optional
blank line after the closing frontmatter delimiter is removed.

**Validation without execution.** Apply discovers a bounded number of
schedules, validates their real paths and bounded contents, parses each
`cron` value, and includes their original bytes in the source fingerprint. It
starts no harness process, clock, or external registration.

**One-occurrence dispatch.** Triggering a schedule occurrence carries these
responsibilities, however the surface is rendered (reference rendering:
`tenon schedule trigger AGENT NAME --input-id ID`):

- Every occurrence is submitted under a caller-owned stable ID, and
  acceptance is durable: a repeated ID returns the retained outcome without
  opening a harness.
- Every accepted occurrence opens a fresh native session — never a resumed
  one — so task isolation is structural, not advisory.
- An operator-selected turn deadline bounds the occurrence independently of
  any whole-process bound. Expiry aborts the task process and durably records
  the occurrence as uncertain with a distinct, stable reason, so the stable
  ID returns that classified result rather than replaying the work.
- A crash between acceptance and a proven terminal result leaves the
  occurrence uncertain; it is never silently retried. A later occurrence
  still opens a fresh session.
- Lifecycle reporting is bounded — schedule name, occurrence ID, lifecycle
  status, duplicate flag, and available native runtime IDs — and never
  contains model text. Any non-completed terminal status yields a nonzero
  result after the status is reported.

**Bounded memory.** Deduplication rests on bounded retained recent outcomes
per schedule, not an unbounded execution ledger.

## Context

Eve treats a Markdown schedule as task mode: the file carries one five-field
cron value and a prompt, each occurrence gets its own session, and model
output is discarded. Tenon does not own a hosted runtime, so the first useful
slice is portable source plus explicit one-shot dispatch. Reusing the durable
turn dispatcher's existing acceptance, deduplication, terminal-outcome, and
uncertain-restart responsibilities avoids inventing a second scheduler state
store, and retaining bounded outcomes per schedule rather than a record per
occurrence keeps deduplication from becoming an unbounded ledger.

## Consequences

- Schedules are root-only because subagents accept `instructions.md` only.
- Nested names come directly from their bounded UTF-8 relative paths; tenon
  does not impose a model-tool identifier grammar on them.
- Changing a schedule invalidates the applied setup through the source
  fingerprint, but it does not require native-session migration.
- One-shot dispatch is local and explicit. It performs no delivery, network
  registration, missed-run replay, daemon installation, or schedule overlap
  management.
- TypeScript schedule handlers and Eve's hosted authenticated runtime remain
  unsupported.

## Sources

- [Eve schedules](https://github.com/vercel/eve/blob/84c3dfc1ff91e075444eee7c6d8e2ef55b2aaebe/docs/schedules.mdx)
- [Product specification](../product-spec.md)
- [ADR 0001](0001-use-native-harnesses.md)
