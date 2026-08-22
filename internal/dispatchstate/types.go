package dispatchstate

// Ref identifies one conversation's durable queue. Two Refs that differ in
// any field are independent: they queue, dedup, and resume separately even
// within the same state file.
type Ref struct {
	// Agent is the normalized agent name.
	Agent string
	// Fingerprint is the applied source fingerprint the turn was dispatched
	// against.
	Fingerprint string
	// Harness is the native harness name ("claude" or "codex").
	Harness string
	// Conversation is the caller-owned conversation identifier: a stable
	// session key for interactive mode, or the schedule name for task mode.
	Conversation string
}

// Status is a queued input's lifecycle state. "queued" and "active" are
// non-terminal; the rest are terminal and, once recorded, are never
// re-executed for the same input id.
type Status string

const (
	// Queued marks an input accepted but not yet started.
	Queued Status = "queued"
	// Active marks the one input per conversation currently running.
	Active Status = "active"
	// Completed marks a turn that finished normally.
	Completed Status = "completed"
	// Failed marks a turn that finished with an error.
	Failed Status = "failed"
	// Cancelled marks a turn that was cancelled before or during execution.
	Cancelled Status = "cancelled"
	// Uncertain marks a turn whose outcome could not be proven, typically
	// because the dispatcher restarted while it was active.
	Uncertain Status = "uncertain"
)

// dispatcherRestarted is the stable reason RecoverUncertain and
// RecoverTaskUncertain record; the dispatcher never invents its own text
// for this case so callers can match on it.
const dispatcherRestarted = "dispatcher_restarted"

// conversationState is the persisted state for one Ref: its resumable
// native session id, its FIFO queue (at most one entry, the head, is ever
// Active), and its bounded recent terminal outcomes.
type conversationState struct {
	Ref       Ref            `json:"ref"`
	SessionID string         `json:"session_id,omitempty"`
	Queue     []queuedEntry  `json:"queue,omitempty"`
	Outcomes  []outcomeEntry `json:"outcomes,omitempty"`
}

// queuedEntry is one accepted, not-yet-terminal input.
type queuedEntry struct {
	InputID string `json:"input_id"`
	Text    string `json:"text"`
	Status  Status `json:"status"`
}

// outcomeEntry is one recorded terminal outcome, retained for
// deduplication. Outcomes are appended in insertion order so the oldest is
// always at index 0 and eviction is a simple truncation from the front.
type outcomeEntry struct {
	InputID string `json:"input_id"`
	Status  Status `json:"status"`
	Reason  string `json:"reason,omitempty"`
}

// QueuedInput is the head input StartNext promotes to active.
type QueuedInput struct {
	InputID string
	Text    string
}

// Recovered is one input a recovery call terminalized as Uncertain.
type Recovered struct {
	InputID string
	Status  Status
	Reason  string
}

// AcceptResult reports what became of one Accept call.
type AcceptResult struct {
	// Status is the input's status after this call: "queued" for a newly
	// accepted input, the retained status for a duplicate, or the zero
	// value when Rejected is true.
	Status Status
	// Duplicate is true when inputID was already known in this
	// conversation, either still queued/active or already terminal.
	Duplicate bool
	// Rejected is true when the input was refused outright: it was never
	// queued and carries no retained status.
	Rejected bool
	// Reason explains a duplicate's retained terminal outcome or a
	// rejection; empty for a fresh accept or a duplicate still in flight.
	Reason string
}
