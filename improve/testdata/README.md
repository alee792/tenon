# Recorded tenon streams

Every file here is the literal stdout of a real `tenon` binary, captured
against `agents/maintainer` in this repository and kept as the fixture
`improve/test_tenon.py` parses. Recorded rather than hand-written on purpose:
a fixture someone typed proves the parser agrees with the spec, and the spec
is not what runs.

| Fixture | What produced it |
| --- | --- |
| `check-ok.jsonl` | the gate passing |
| `check-ok-warning.jsonl` | a passing gate carrying one warning — see below |
| `check-gate-failed.jsonl` | a source whose `instructions.md` lost its frontmatter |
| `check-gate-failed-unreadable.jsonl` | an agent root that does not exist, so there is no digest to report |
| `check-error.jsonl` | `--write-pins` to an unwritable path: an environment failure |
| `check-truncated.jsonl` | `check-gate-failed.jsonl` with its terminator cut off |
| `check-pins-written.jsonl` | the gate writing a pin set and echoing the path |
| `check-emit-files.jsonl` | the authored inventory, followed by the terminator |
| `apply-ok.jsonl` | compiling into a fresh workspace |
| `drift-ok.jsonl` | the workspace still matching |
| `drift-drift.jsonl` | the same workspace after one hand edit to `CLAUDE.md` |
| `drift-gate-failed.jsonl` | drift over a source that fails the gate |
| `clean-ok.jsonl` | a forced clean, with the removed paths |
| `clean-blocked.jsonl` | clean refusing a modified recorded file |
| `clean-error.jsonl` | clean against a workspace with no record for the harness |
| `run-recovered-uncertain.jsonl` | a dispatch that leads with a startup-recovered `turn.uncertain` from a run killed mid-turn, then accepts and completes its own input |
| `run-gate-failed.jsonl` | a dispatch rejected at the gate: a digest and an empty fingerprint |
| `run-error-deadline.jsonl` | tenon's own `--timeout` expiring, which ends `outcome: "error"` like any other environment failure — the reason the adapter enforces the wall clock itself |

`check-ok-warning.jsonl` is the one exception, and it is a splice rather than
an invention: a recorded diagnostic with its severity set to `warning`,
followed by a recorded `ok` terminator. Nothing in tenon emits a warning
diagnostic today, but the vocabulary declares one, and the rule the fixture
guards — that a candidate which merely warns is not discarded — is precisely
the bug that existed before the terminator could be read. It is worth holding
a test on before there is a producer to record.
