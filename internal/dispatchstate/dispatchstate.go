// Package dispatchstate is the durable state model for the headless turn
// dispatcher (see docs/product-spec.md "Headless operation" and ADR 0008).
// It is pure state: no process spawning, no harness invocation, no CLI. The
// dispatcher layers FIFO-per-conversation input acceptance, input-id
// deduplication, resumable native session persistence, and uncertain-on-
// restart recovery on top of one owner-only JSON file per workspace,
// covering both interactive turns and task (schedule) turns.
//
// State lives at <workspace>/.tenon/dispatch.json, schema_version 1, mode
// 0600, written atomically (temp file in the same directory, renamed into
// place) so a reader never observes a partial file. Every mutation is
// copy-on-write: the in-memory state is cloned, the clone is mutated, then
// persisted; a failed write restores the prior in-memory state so a Store
// never drifts from what is durable on disk.
package dispatchstate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	// schemaVersion is the only state.SchemaVersion this package accepts.
	schemaVersion = 1
	// stateFileName is the state file's name inside the workspace's
	// .tenon directory.
	stateFileName = "dispatch.json"

	// MaxStateBytes bounds the on-disk state file. A file over this size is
	// refused at load rather than read into memory.
	MaxStateBytes = 1 << 20
	// MaxQueue bounds the number of entries queued per conversation,
	// including the active head. Accept rejects once the queue is full.
	MaxQueue = 32
	// MaxRecentOutcomes bounds the number of recorded terminal outcomes
	// retained per conversation. Past the cap the oldest outcome (by
	// insertion order) is evicted so deduplication memory stays bounded
	// rather than becoming an unbounded execution ledger.
	MaxRecentOutcomes = 256
	// MaxInputBytes bounds one accepted input's text.
	MaxInputBytes = 32 << 10
	// MaxInputIDBytes bounds a caller-owned input id.
	MaxInputIDBytes = 192
	// MaxRefFieldBytes bounds the opaque agent, fingerprint, and harness
	// identifiers a caller supplies in a Ref.
	MaxRefFieldBytes = 256
	// MaxConversationBytes bounds the opaque conversation identifier, per
	// the dispatcher's own tighter cap.
	MaxConversationBytes = 128
	// MaxSessionIDBytes bounds a persisted native session id.
	MaxSessionIDBytes = 4096
	// MaxReasonBytes bounds a terminal outcome's recorded reason.
	MaxReasonBytes = 1024
)

// Store is the durable, owner-only dispatch state for one workspace. A
// Store loads the whole file into memory at Open and keeps it there;
// every exported method mutates a clone and persists it before returning,
// so the in-memory copy and the on-disk file never disagree once a call
// returns successfully.
type Store struct {
	mu   sync.Mutex
	path string
	st   *state
}

// state is the on-disk (and in-memory) shape.
type state struct {
	SchemaVersion int                 `json:"schema_version"`
	Conversations []conversationState `json:"conversations,omitempty"`
}

// StatePath returns the owner-only dispatch state file for workspace.
func StatePath(workspace string) string {
	return filepath.Join(workspace, ".tenon", stateFileName)
}

// Open loads the dispatch state for workspace, or initializes an empty one
// if the file does not yet exist. It validates the file's permissions, size,
// schema version, and JSON shape (unknown and duplicate keys are rejected)
// before trusting its contents.
func Open(workspace string) (*Store, error) {
	path := StatePath(workspace)
	st, err := load(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, st: st}, nil
}

// load reads and validates the state file at path, or returns an empty
// initialized state when it does not exist.
func load(path string) (*state, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return &state{SchemaVersion: schemaVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dispatchstate: statting %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("dispatchstate: %s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("dispatchstate: %s has group- or world-accessible permissions %v; refusing to trust it", path, info.Mode().Perm())
	}
	if info.Size() > MaxStateBytes {
		return nil, fmt.Errorf("dispatchstate: %s is %d bytes, over the %d byte bound", path, info.Size(), int64(MaxStateBytes))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("dispatchstate: reading %s: %w", path, err)
	}
	if err := checkNoDuplicateKeys(raw); err != nil {
		return nil, fmt.Errorf("dispatchstate: %s carries a duplicate JSON key: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var st state
	if err := dec.Decode(&st); err != nil {
		return nil, fmt.Errorf("dispatchstate: decoding %s: %w", path, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("dispatchstate: %s carries trailing data after the JSON value", path)
	}
	if st.SchemaVersion != schemaVersion {
		return nil, fmt.Errorf("dispatchstate: %s has schema_version %d, only %d is supported", path, st.SchemaVersion, schemaVersion)
	}
	return &st, nil
}

// checkNoDuplicateKeys walks raw as JSON and reports the first duplicate key
// found within any single object, at any depth. encoding/json silently
// prefers the last occurrence of a duplicate key; state that carries one is
// refused outright instead.
func checkNoDuplicateKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	return checkNoDuplicateKeysValue(dec)
}

func checkNoDuplicateKeysValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // scalar value: nothing to walk
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := keyTok.(string)
			if seen[key] {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[key] = true
			if err := checkNoDuplicateKeysValue(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // consume closing '}'
		return err
	case '[':
		for dec.More() {
			if err := checkNoDuplicateKeysValue(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // consume closing ']'
		return err
	}
	return nil
}

// save marshals and atomically writes s.st to s.path, creating the owner-
// only .tenon directory if needed. It is only ever called with the lock
// held and the candidate state already installed in s.st by the caller;
// callers restore the prior s.st themselves on error.
func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("dispatchstate: creating %s: %w", filepath.Dir(s.path), err)
	}
	content, err := marshalIndent(s.st, "", "  ")
	if err != nil {
		return fmt.Errorf("dispatchstate: encoding state: %w", err)
	}
	return writeAtomic(s.path, append(content, '\n'))
}

// marshalIndent is a seam over json.MarshalIndent so tests can inject a
// marshal failure and prove save leaves no partial file behind.
var marshalIndent = json.MarshalIndent

// renameFile is a seam over os.Rename so tests can inject a failure at the
// last step of writeAtomic and prove no partial file is left in place.
var renameFile = os.Rename

// writeAtomic writes content to a same-directory temporary file at mode
// 0600 and renames it into place, so a reader never observes a partial
// write and the file is owner-only from the moment it exists at its final
// path.
func writeAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tenon-dispatch-*")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := renameFile(tmp.Name(), path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// cloneState returns a deep copy of st so a mutation can be attempted
// against the copy and discarded without disturbing st on failure.
func cloneState(st *state) *state {
	out := &state{SchemaVersion: st.SchemaVersion}
	if st.Conversations != nil {
		out.Conversations = make([]conversationState, len(st.Conversations))
		for i, c := range st.Conversations {
			nc := c
			nc.Queue = append([]queuedEntry(nil), c.Queue...)
			nc.Outcomes = append([]outcomeEntry(nil), c.Outcomes...)
			out.Conversations[i] = nc
		}
	}
	return out
}

// commit installs candidate as s.st and persists it, restoring the prior
// state on a write failure. It must be called with s.mu held.
func (s *Store) commit(candidate *state) error {
	prev := s.st
	s.st = candidate
	if err := s.save(); err != nil {
		s.st = prev
		return err
	}
	return nil
}
