# ADR 0011: Run schedules from a foreground UTC clock

- Status: superseded by [ADR 0029](0029-stop-driving-the-harness.md),
  which removed the schedules/ surface and its execution
- Re-records: prototype ADR 0026 (alee792/hctl)

## Plain-English summary

An operator can run one foreground process that evaluates an agent's
already-applied five-field schedules in UTC and dispatches current
occurrences. It installs no daemon and does not replay missed work.

## Decision

The foreground schedule runner (reference rendering:
`tenon schedule run AGENT`) carries these responsibilities:

- **Current setup only.** It loads and compiles source, verifies the selected
  harness and current generated setup, and holds exclusive local ownership of
  the workspace/agent/harness combination for its lifetime, so two clocks
  cannot run the same schedules. It does not auto-apply or hot reload.
- **UTC, current occurrences only.** Evaluation is UTC only. The first
  candidate is strictly after startup, and only an occurrence due in the
  current UTC minute is ever admitted. Downtime, sleep, forward jumps, and
  repeated or backward clock movement never admit an older candidate, a
  duplicate, or a catch-up burst.
- **No overlap per schedule.** An occurrence that is queued or active counts
  as in flight, and a later due minute for that schedule is skipped while it
  remains so. Distinct schedules may run concurrently up to an
  operator-selected capacity.
- **Stable occurrence identity.** Each occurrence's ID is derived
  deterministically from the exact schedule name and its scheduled UTC
  minute, so the same due minute deduplicates through the ordinary dispatch
  contract of [ADR 0008](0008-run-schedules-as-fresh-dispatch-tasks.md),
  which also supplies the fresh native session and turn deadline.
- **Graceful drain.** Termination signals stop admission first; already
  admitted work completes or reaches its turn deadline before durable state
  and ownership are released. A durable-state or diagnostic-output failure
  also stops admission, while one schedule's terminal failure does not stop
  unrelated schedules. Queued or active state retained from an interrupted
  prior runtime is classified uncertain at startup and never executed.

## Consequences

- Tenon provides a foreground clock, not daemon supervision or hosted
  scheduling.
- Sleep, downtime, and clock movement never create catch-up bursts or
  duplicate admissions.
- Model output and prompts never enter lifecycle diagnostics.
- Ownership is local; distributed scheduling remains unsupported.

## Related decisions

- [ADR 0008](0008-run-schedules-as-fresh-dispatch-tasks.md)
- [Product specification](../product-spec.md)
