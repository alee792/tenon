package dispatchstate

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// inputIDPattern is the caller-owned input id grammar: it must start with
// an alphanumeric character and stay within MaxInputIDBytes.
var inputIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,191}$`)

// Accept durably enqueues one input for ref, or reports why it did not.
// A repeated inputID within the same conversation is never queued twice:
// it deduplicates against the queue (still in flight) or a recorded
// outcome (already terminal), returning the retained status in either
// case. Malformed input ids, invalid text, and a full queue are refused
// with a reason rather than an error; err is reserved for state-file I/O
// failures.
func (s *Store) Accept(ref Ref, inputID, text string) (AcceptResult, error) {
	if err := validateRef(ref); err != nil {
		return AcceptResult{}, err
	}
	if !inputIDPattern.MatchString(inputID) {
		return AcceptResult{Rejected: true, Reason: "input_id does not match the required grammar"}, nil
	}
	if !utf8.ValidString(text) {
		return AcceptResult{Rejected: true, Reason: "text is not valid UTF-8"}, nil
	}
	if strings.TrimSpace(text) == "" {
		return AcceptResult{Rejected: true, Reason: "text is empty"}, nil
	}
	if len(text) > MaxInputBytes {
		return AcceptResult{Rejected: true, Reason: fmt.Sprintf("text is %d bytes, over the %d byte bound", len(text), MaxInputBytes)}, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	clone := cloneState(s.st)
	conv := findConversation(clone, ref)
	if conv != nil {
		for _, q := range conv.Queue {
			if q.InputID == inputID {
				return AcceptResult{Status: q.Status, Duplicate: true}, nil
			}
		}
		for _, o := range conv.Outcomes {
			if o.InputID == inputID {
				return AcceptResult{Status: o.Status, Duplicate: true, Reason: o.Reason}, nil
			}
		}
		if len(conv.Queue) >= MaxQueue {
			return AcceptResult{Rejected: true, Reason: fmt.Sprintf("the conversation queue is full at %d entries", MaxQueue)}, nil
		}
	} else {
		conv = getOrCreateConversation(clone, ref)
	}
	conv.Queue = append(conv.Queue, queuedEntry{InputID: inputID, Text: text, Status: Queued})

	if err := s.commit(clone); err != nil {
		return AcceptResult{}, err
	}
	return AcceptResult{Status: Queued}, nil
}

// StartNext promotes the head of ref's queue to Active and returns it along
// with the persisted session id to resume, if any. ok is false when the
// queue is empty or its head is already active.
func (s *Store) StartNext(ref Ref) (head QueuedInput, resumeID string, ok bool, err error) {
	if err := validateRef(ref); err != nil {
		return QueuedInput{}, "", false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	clone := cloneState(s.st)
	conv := findConversation(clone, ref)
	if conv == nil || len(conv.Queue) == 0 || conv.Queue[0].Status == Active {
		return QueuedInput{}, "", false, nil
	}
	conv.Queue[0].Status = Active
	result := QueuedInput{InputID: conv.Queue[0].InputID, Text: conv.Queue[0].Text}
	resume := conv.SessionID

	if err := s.commit(clone); err != nil {
		return QueuedInput{}, "", false, err
	}
	return result, resume, true, nil
}

// SetSessionID persists the resumable native session id for ref's
// conversation, replacing any previously recorded one.
func (s *Store) SetSessionID(ref Ref, sessionID string) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	if !utf8.ValidString(sessionID) {
		return fmt.Errorf("dispatchstate: session id is not valid UTF-8")
	}
	if len(sessionID) > MaxSessionIDBytes {
		return fmt.Errorf("dispatchstate: session id is %d bytes, over the %d byte bound", len(sessionID), MaxSessionIDBytes)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	clone := cloneState(s.st)
	conv := getOrCreateConversation(clone, ref)
	conv.SessionID = sessionID
	return s.commit(clone)
}

// Complete terminalizes the active input of ref's conversation with status
// and an optional bounded reason, records the outcome for future
// deduplication, and pops it from the queue head. Only the current active
// head may be completed; a mismatched or absent active head is an error.
// When task is true, the native session id is also cleared so the next
// occurrence opens a fresh session (ADR 0008); otherwise it is left in
// place for resumption.
func (s *Store) Complete(ref Ref, inputID string, status Status, reason string, task bool) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	if !isTerminal(status) {
		return fmt.Errorf("dispatchstate: %q is not a terminal status", status)
	}
	if !utf8.ValidString(reason) {
		return fmt.Errorf("dispatchstate: reason is not valid UTF-8")
	}
	if len(reason) > MaxReasonBytes {
		return fmt.Errorf("dispatchstate: reason is %d bytes, over the %d byte bound", len(reason), MaxReasonBytes)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	clone := cloneState(s.st)
	conv := findConversation(clone, ref)
	if conv == nil || len(conv.Queue) == 0 {
		return fmt.Errorf("dispatchstate: no active input for this conversation")
	}
	head := conv.Queue[0]
	if head.Status != Active {
		return fmt.Errorf("dispatchstate: the queue head is not active")
	}
	if head.InputID != inputID {
		return fmt.Errorf("dispatchstate: %q is not the active head", inputID)
	}

	conv.Outcomes = appendOutcome(conv.Outcomes, outcomeEntry{InputID: inputID, Status: status, Reason: reason})
	conv.Queue = conv.Queue[1:]
	if task {
		conv.SessionID = ""
	}

	return s.commit(clone)
}

// RecoverUncertain terminalizes any queue entry left Active in ref's
// conversation as Uncertain with reason "dispatcher_restarted": a turn
// that was running when the dispatcher last stopped has no proven terminal
// result and is never re-executed. Call it once per conversation at
// interactive-mode startup, before StartNext can promote anything new.
func (s *Store) RecoverUncertain(ref Ref) ([]Recovered, error) {
	if err := validateRef(ref); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	clone := cloneState(s.st)
	conv := findConversation(clone, ref)
	if conv == nil {
		return nil, nil
	}

	var recovered []Recovered
	var kept []queuedEntry
	for _, q := range conv.Queue {
		if q.Status != Active {
			kept = append(kept, q)
			continue
		}
		recovered = append(recovered, Recovered{InputID: q.InputID, Status: Uncertain, Reason: dispatcherRestarted})
		conv.Outcomes = appendOutcome(conv.Outcomes, outcomeEntry{InputID: q.InputID, Status: Uncertain, Reason: dispatcherRestarted})
	}
	if recovered == nil {
		return nil, nil
	}
	conv.Queue = kept

	if err := s.commit(clone); err != nil {
		return nil, err
	}
	return recovered, nil
}

// RecoverTaskUncertain terminalizes every queued or active entry in ref's
// task conversation as Uncertain with reason "dispatcher_restarted" and
// clears the persisted session id. Unlike RecoverUncertain, a merely
// queued (not yet active) task entry is also terminalized: a caller may
// already have been told it was accepted, so it cannot be silently
// dropped or silently resumed. Call it once per task conversation at
// startup.
func (s *Store) RecoverTaskUncertain(ref Ref) ([]Recovered, error) {
	if err := validateRef(ref); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	clone := cloneState(s.st)
	conv := findConversation(clone, ref)
	if conv == nil {
		return nil, nil
	}

	var recovered []Recovered
	for _, q := range conv.Queue {
		recovered = append(recovered, Recovered{InputID: q.InputID, Status: Uncertain, Reason: dispatcherRestarted})
		conv.Outcomes = appendOutcome(conv.Outcomes, outcomeEntry{InputID: q.InputID, Status: Uncertain, Reason: dispatcherRestarted})
	}
	if recovered == nil && conv.SessionID == "" {
		return nil, nil
	}
	conv.Queue = nil
	conv.SessionID = ""

	if err := s.commit(clone); err != nil {
		return nil, err
	}
	return recovered, nil
}

// isTerminal reports whether status is one of Complete's accepted terminal
// statuses.
func isTerminal(status Status) bool {
	switch status {
	case Completed, Failed, Cancelled, Uncertain:
		return true
	default:
		return false
	}
}

// appendOutcome appends outcome to outcomes, evicting the oldest entries
// (by insertion order, from the front) past MaxRecentOutcomes so retained
// deduplication memory stays bounded.
func appendOutcome(outcomes []outcomeEntry, outcome outcomeEntry) []outcomeEntry {
	outcomes = append(outcomes, outcome)
	if len(outcomes) > MaxRecentOutcomes {
		outcomes = append([]outcomeEntry(nil), outcomes[len(outcomes)-MaxRecentOutcomes:]...)
	}
	return outcomes
}

// findConversation returns the conversation matching ref within st, or nil
// if none is recorded yet.
func findConversation(st *state, ref Ref) *conversationState {
	for i := range st.Conversations {
		if st.Conversations[i].Ref == ref {
			return &st.Conversations[i]
		}
	}
	return nil
}

// getOrCreateConversation returns the conversation matching ref within st,
// appending a fresh one if none is recorded yet.
func getOrCreateConversation(st *state, ref Ref) *conversationState {
	if conv := findConversation(st, ref); conv != nil {
		return conv
	}
	st.Conversations = append(st.Conversations, conversationState{Ref: ref})
	return &st.Conversations[len(st.Conversations)-1]
}

// validateRef bounds the opaque identifiers a caller supplies. These are
// dispatcher-owned identifiers, not raw model or JSONL input, so a
// violation is a caller bug reported as an error rather than an
// AcceptResult rejection.
func validateRef(ref Ref) error {
	for _, f := range []struct {
		name  string
		value string
		max   int
	}{
		{"agent", ref.Agent, MaxRefFieldBytes},
		{"fingerprint", ref.Fingerprint, MaxRefFieldBytes},
		{"harness", ref.Harness, MaxRefFieldBytes},
		{"conversation", ref.Conversation, MaxConversationBytes},
	} {
		if !utf8.ValidString(f.value) {
			return fmt.Errorf("dispatchstate: ref.%s is not valid UTF-8", f.name)
		}
		if len(f.value) > f.max {
			return fmt.Errorf("dispatchstate: ref.%s is %d bytes, over the %d byte bound", f.name, len(f.value), f.max)
		}
	}
	return nil
}
