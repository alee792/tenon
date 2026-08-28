package dispatch

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
