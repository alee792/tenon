package dispatch

import (
	"encoding/json"
	"io"
)

// schemaVersion is the only wire schema this dispatcher emits.
const schemaVersion = 1

// The wire event types, one per emitted line. input.* report acceptance
// decisions; turn.queued through turn.uncertain report a turn's lifecycle;
// driver.process_failed reports that the harness could not be driven to a
// proven result.
const (
	typeInputAccepted   = "input.accepted"
	typeInputRejected   = "input.rejected"
	typeInputDuplicate  = "input.duplicate"
	typeTurnQueued      = "turn.queued"
	typeTurnStarted     = "turn.started"
	typeSessionStarted  = "session.started"
	typeSessionResumed  = "session.resumed"
	typeAgentOutput     = "agent.output.delta"
	typeTurnCompleted   = "turn.completed"
	typeTurnFailed      = "turn.failed"
	typeTurnCancelled   = "turn.cancelled"
	typeTurnUncertain   = "turn.uncertain"
	typeDriverProcessed = "driver.process_failed"
	typeRunCompleted    = "run.completed"
)

// event is one wire line. The first five fields are always present; the rest
// are omitted when empty. status and reason never carry model text; delta is
// the only model-text-bearing field.
type event struct {
	SchemaVersion int    `json:"schema_version"`
	Sequence      int    `json:"sequence"`
	Type          string `json:"type"`
	Harness       string `json:"harness"`
	Conversation  string `json:"conversation"`
	// Fingerprint is the agent's source fingerprint, present on every event so
	// observation of the stream joins to the exact source configuration that
	// produced it even when no manifest is supplied (product spec, "Agent
	// manifest": every dispatch lifecycle event carries the source fingerprint).
	Fingerprint string `json:"fingerprint"`
	// Manifest is the supplied agent manifest's identity, a provenance join
	// key present on every event only when a manifest was supplied. It carries
	// no pin or fingerprint value and is omitted when no manifest is supplied.
	Manifest  string `json:"manifest,omitempty"`
	InputID   string `json:"input_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Status    string `json:"status,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Delta     string `json:"delta,omitempty"`
	Bytes     int    `json:"bytes,omitempty"`
	// Outcome, Turns, Error and SourceDigest appear on the terminal
	// run.completed event and on no other: they report how the whole
	// dispatch ended rather than what happened in one turn.
	Outcome      string `json:"outcome,omitempty"`
	Turns        *Turns `json:"turns,omitempty"`
	Error        string `json:"error,omitempty"`
	SourceDigest string `json:"source_digest,omitempty"`
}

// Turns counts the terminal statuses of the turns one dispatch ran. It is
// what a loop scores: run's own outcome says only whether the dispatcher
// finished the work it was given, so a run in which every turn failed still
// ends ok and is distinguished from a clean one here and nowhere else.
// Cancelled is carried alongside the four statuses a loop usually reads so
// the counts always sum to the turns dispatched.
type Turns struct {
	Completed     int `json:"completed"`
	Failed        int `json:"failed"`
	Uncertain     int `json:"uncertain"`
	ProcessFailed int `json:"process_failed"`
	Cancelled     int `json:"cancelled"`
}

// Summary is what one Run reports back to its caller: the last sequence
// number it emitted, so a caller that must terminate the stream itself
// continues the numbering rather than restarting it, and the turn counts.
// It is returned on the error paths too, where the caller writes the
// terminator Run never reached.
type Summary struct {
	Sequence int
	Turns    Turns
}

// Completion is one run.completed terminator: the wire envelope every event
// carries, plus how the dispatch ended. Callers outside this package build
// it on the paths where dispatch never ran or returned an error — a gate
// failure has no fingerprint to stamp, which is allowed and expected.
type Completion struct {
	Sequence     int
	Harness      string
	Conversation string
	Fingerprint  string
	Manifest     string
	Outcome      string
	Turns        Turns
	Error        string
	SourceDigest string
}

// WriteCompleted writes one run.completed event to out. It is the terminator
// of run's stream, and it is an event like every line before it — same
// schema version, same monotonic sequence, same harness, conversation and
// fingerprint envelope — so a consumer decodes the whole stream one way and
// reads the last line's outcome instead of inferring an ending from silence.
func WriteCompleted(out io.Writer, c Completion) error {
	turns := c.Turns
	return json.NewEncoder(out).Encode(event{
		SchemaVersion: schemaVersion,
		Sequence:      c.Sequence,
		Type:          typeRunCompleted,
		Harness:       c.Harness,
		Conversation:  c.Conversation,
		Fingerprint:   c.Fingerprint,
		Manifest:      c.Manifest,
		Outcome:       c.Outcome,
		Turns:         &turns,
		Error:         c.Error,
		SourceDigest:  c.SourceDigest,
	})
}

// emit stamps an event with the schema version, the next monotonic sequence,
// and the invariant harness and conversation identifiers, then writes it as one
// JSON line. The dispatcher's single owner goroutine is the only caller, so the
// sequence is race-free and the encoder is never shared.
func (d *dispatcher) emit(e event) error {
	d.seq++
	e.SchemaVersion = schemaVersion
	e.Sequence = d.seq
	e.Harness = d.harness
	e.Conversation = d.conversation
	e.Fingerprint = d.fingerprint
	e.Manifest = d.manifest
	return d.enc.Encode(e)
}
