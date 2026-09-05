# ADR 0029: Stop driving the harness

- Status: accepted
- Amends: [ADR 0001](0001-use-native-harnesses.md) (its optional turn
  dispatcher is withdrawn); [ADR 0008](0008-run-schedules-as-fresh-dispatch-tasks.md)
  and [ADR 0011](0011-run-schedules-from-a-foreground-utc-clock.md) are
  superseded in full: the authored `schedules/` format went with its
  executor once the maintainer decided a format nothing in tenon runs does
  not earn its keep;
  [ADR 0006](0006-use-a-local-secretless-operation-broker.md) is unaffected
- Supersedes: [ADR 0028](0028-drive-headless-turns-over-the-agent-client-protocol.md)
- Research record: [docs/workbench/acp-alignment.md](../workbench/acp-alignment.md)

## Decision

Tenon does not drive the harness. `tenon run`, `tenon schedule trigger`,
and `tenon schedule run` are removed, with the turn dispatcher, its durable
queue and conversation state, the Claude and Codex headless drivers, the
Agent Client Protocol driver of ADR 0028, and the foreground schedule clock
behind them.

Running headless is the operator launching the harness's own headless mode
(`claude -p`, `codex exec`) or an Agent Client Protocol client (acpx,
OpenClaw, an editor) in an applied workspace, after `tenon drift` — with
`--pins` when a pin set gates the run — has proven it. Attribution is the
fingerprint `check` or `drift` reports, recorded by the operator or loop
beside the run's output. Approval is the client's policy or the harness's
own authored mode. A task on a clock is the same launch under the
operator's scheduler, with the prompt kept wherever that scheduler reads it.

`schedules/` is removed with the executor. An authored format that nothing
in tenon runs is a second inventory the author must maintain for no
consumer; a `schedules/` directory now fails the gate with
`schedules.removed`, so the removal is never silent. `internal/cron` and
the `robfig/cron` dependency go with it.

The improve module's `dispatch` role goes with the dispatcher. fanout and
evolve take a `runner`: the caller's command, run once per task in the
variant's workspace under fanout's own wall clock, with the prompt in
`FANOUT_TASK`, its exit code as the task's verdict, and its stdout as the
text a judge reads. Nothing in `improve/` names a harness client; acpx is
one recipe in its documentation.

## Context

The dispatcher, its durable state, and the harness drivers existed for the
conversational channel product — a chat surface delivering messages faster
than the model answers, from many people, across restarts — that the
prototype kept and this core never shipped. Every consumer that remained
sent one fresh task turn per run: the improve module and the schedule
paths. The queue held at most one item and the resume path never fired.

Meanwhile the open standards settled the two halves of the job. Agent
Skills and Agent Plugins define the agent; the Agent Client Protocol
defines the session. acpx, OpenClaw, Zed, and JetBrains launch the
harness's adapter in a workspace and read whatever is applied there, with
their own permission policies and their own session state. The channel
product exists, over an open protocol, maintained by someone else.

What tenon's run added over those clients — the fingerprint on every event
line, a secret-safe failure vocabulary, an outcome terminator — was worth
about three thousand lines of Go and two vendor wire protocols to track.
The first is a recorded value; the second is a rule in a recipe; the third
was scored only by `improve/`, which now scores an exit code.

Tenet 1 decides it: cost is what an author or contributor must know, and a
dispatcher, a wire schema, a pin-verification-per-turn rule, and two
permission-policy surfaces were things to know. The north star's second
commitment already said tenon never absorbs runtime supervision; this
record reads "the crossing" as ending at the applied workspace.

## Consequences

- `internal/dispatch`, `internal/dispatchstate`, `internal/harness` (all
  drivers), `internal/schedule`, `internal/cron`, and the schedules loader
  are deleted, and `go.mod` loses `robfig/cron`. The CLI loses `run` and
  `schedule` and their flags; the catalog loses its schedule entries.
- The north star's measure named "scheduled" as a leg and "runs" as a
  tenon act; [ADR 0030](0030-amend-the-measure-for-the-operators-client.md)
  amends it on this record's evidence, as that file's change rule
  requires.
- `docs/product-spec.md` "Headless operation" becomes the recipe and its
  four rules; acceptance items 7 and 8 are restated; the known limitation
  is that headless runs are attributed by the operator.
- Known loss: no tenon process classifies a headless failure, so a loop
  that keeps output must itself refuse to copy raw harness error text —
  which has carried a live API key — into a record. The recipe says so.
- The staged entrypoint already verified and exec'd the harness rather than
  a dispatcher; it is unchanged.

## Falsifier and review

Reverse this if an operator journey needs something no ACP client or
harness headless mode provides and that only a process between the input
and the harness can: an attribution the client cannot record, or a fail-
closed check that cannot run before launch. Review with the next release.
