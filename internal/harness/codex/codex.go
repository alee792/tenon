// Package codex drives Codex as a headless turn harness behind the
// harness.Driver seam. Each opened session launches one `codex app-server`
// process, performs the JSON-RPC handshake and thread start/resume once, then
// runs exactly one turn to a terminal. Server replies omit the "jsonrpc" field;
// requests tenon sends include it.
//
// A hard security boundary runs through this package: a turn failure message
// observed in the wild echoed a live API key. No raw frame text — least of all
// turn.error.message — is ever copied into an event, a reason, a log, or
// stderr. The failure reason is a fixed vocabulary produced by safeReason.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/alee792/tenon/internal/harness"
)

// Driver opens Codex sessions. exe is the resolved executable name and version
// is tenon's version, reported to the server on initialize.
type Driver struct {
	exe     string
	version string
}

// NewDriver constructs a Codex driver for the given executable (defaulting to
// "codex") and tenon version.
func NewDriver(exe, version string) Driver {
	if exe == "" {
		exe = "codex"
	}
	return Driver{exe: exe, version: version}
}

// Name reports the stable harness name.
func (Driver) Name() string { return "codex" }

// Verify reports whether the codex executable resolves and runs.
func (d Driver) Verify(ctx context.Context) error {
	return harness.VerifyExecutable(ctx, d.exe)
}

// Open launches one codex app-server, handshakes, and starts or resumes the
// thread, emitting the session event once the thread id is known.
func (d Driver) Open(ctx context.Context, req harness.OpenRequest) (harness.Session, error) {
	proc, err := harness.StartProcess(ctx, d.exe, req.Workspace, "app-server", "--stdio")
	if err != nil {
		return nil, err
	}
	s := &session{proc: proc, nextID: 3}
	if err := s.handshake(d.version); err != nil {
		proc.Close()
		return nil, err
	}
	ev, err := s.startThread(req)
	if err != nil {
		proc.Close()
		return nil, err
	}
	s.pending = &ev
	return s, nil
}

// session is one codex process driven for exactly one turn.
type session struct {
	proc     *harness.Process
	threadID string
	nextID   int
	// pending holds the session started/resumed event captured during Open so
	// RunTurn emits it before streaming, keeping all emits on the turn path.
	pending *harness.Event
}

// rpcEnvelope is the common shape of any inbound line: a reply carries a
// non-nil id and either result or error; a notification carries a method and no
// id.
type rpcEnvelope struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// rpcError is a reply error. Its Message may carry credential material and is
// never surfaced.
type rpcError struct {
	Message string `json:"message"`
}

// request is one outbound JSON-RPC request or notification. ID is omitted for
// notifications.
type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// handshake performs initialize/initialized once per Open.
func (s *session) handshake(version string) error {
	id := 1
	if err := s.send(request{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "initialize",
		Params: map[string]any{
			"clientInfo":   map[string]any{"name": "tenon", "version": version},
			"capabilities": map[string]any{},
		},
	}); err != nil {
		return err
	}
	if _, err := s.awaitReply(id); err != nil {
		return err
	}
	return s.send(request{JSONRPC: "2.0", Method: "initialized", Params: map[string]any{}})
}

// startThread starts a fresh thread or resumes the requested one, returning the
// session event to emit. It captures the thread id and rejects a resume that
// returns a different thread.
func (s *session) startThread(req harness.OpenRequest) (harness.Event, error) {
	id := 2
	resume := req.ResumeID != "" && !req.Fresh
	out := request{JSONRPC: "2.0", ID: &id}
	if resume {
		out.Method = "thread/resume"
		out.Params = map[string]any{"threadId": req.ResumeID, "cwd": req.Workspace}
	} else {
		out.Method = "thread/start"
		out.Params = map[string]any{"cwd": req.Workspace}
	}
	if err := s.send(out); err != nil {
		return harness.Event{}, err
	}
	result, err := s.awaitReply(id)
	if err != nil {
		return harness.Event{}, err
	}
	var decoded struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return harness.Event{}, fmt.Errorf("codex: decoding thread reply: %w", err)
	}
	if decoded.Thread.ID == "" {
		return harness.Event{}, errors.New("codex did not provide a thread id")
	}
	s.threadID = decoded.Thread.ID
	if resume {
		if decoded.Thread.ID != req.ResumeID {
			return harness.Event{}, errors.New("codex resumed an unexpected thread")
		}
		return harness.Event{Type: harness.EventSessionResumed, SessionID: s.threadID}, nil
	}
	return harness.Event{Type: harness.EventSessionStarted, SessionID: s.threadID}, nil
}

// RunTurn emits the captured session event, sends one turn/start, and reads
// notifications until turn/completed. Losing the process before that terminal
// is a process error.
func (s *session) RunTurn(ctx context.Context, in harness.Input, emit func(harness.Event)) (harness.TurnResult, error) {
	if s.pending != nil {
		emit(*s.pending)
		s.pending = nil
	}

	id := s.nextID
	s.nextID++
	if err := s.send(request{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "turn/start",
		Params: map[string]any{
			"threadId": s.threadID,
			"input":    []map[string]any{{"type": "text", "text": in.Text}},
		},
	}); err != nil {
		return harness.TurnResult{}, fmt.Errorf("codex: writing turn input: %w", err)
	}

	for {
		env, err := s.read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return harness.TurnResult{}, errors.New("codex exited before the turn completed")
			}
			return harness.TurnResult{}, err
		}
		if env.ID != nil {
			// The only reply we expect here is the turn/start ack. An error at
			// this level rejects the turn; its message may carry credentials,
			// so it never leaves the driver.
			if env.Error != nil {
				return harness.TurnResult{}, errors.New("codex rejected the turn")
			}
			continue
		}
		switch env.Method {
		case "item/completed":
			for _, delta := range itemDeltas(env.Params) {
				emit(harness.Event{Type: harness.EventAgentOutputDelta, Delta: delta})
			}
		case "turn/completed":
			return s.classifyCompleted(env.Params)
		}
		// Every other notification (status/started changes, warnings, errors,
		// item start/update) is ignored for output.
	}
}

// classifyCompleted maps a turn/completed notification onto a TurnResult, with
// a bounded reason that never includes the raw failure message.
func (s *session) classifyCompleted(params json.RawMessage) (harness.TurnResult, error) {
	var decoded struct {
		Turn struct {
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		return harness.TurnResult{}, fmt.Errorf("codex: decoding turn result: %w", err)
	}
	status := classifyStatus(decoded.Turn.Status)
	reason := ""
	if status == harness.StatusFailed {
		msg := ""
		if decoded.Turn.Error != nil {
			msg = decoded.Turn.Error.Message
		}
		reason = safeReason(msg)
	}
	return harness.TurnResult{SessionID: s.threadID, Status: status, Reason: reason}, nil
}

// classifyStatus maps a codex turn status onto a terminal Status, treating any
// non-terminal or unrecognized status conservatively as cancelled.
func classifyStatus(status string) harness.Status {
	switch status {
	case "completed":
		return harness.StatusCompleted
	case "failed":
		return harness.StatusFailed
	default:
		return harness.StatusCancelled
	}
}

// safeReason classifies a failure message into a fixed vocabulary and discards
// it. A real capture proved turn.error.message can echo a live API key, so the
// message is matched then dropped: only the category ever leaves the driver.
func safeReason(msg string) string {
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "401") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "api key") {
		return "authentication"
	}
	return "turn_failed"
}

// itemDeltas extracts the assistant text of a completed item, skipping the
// prompt echo (userMessage) and non-text content.
func itemDeltas(params json.RawMessage) []string {
	var decoded struct {
		Item struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"item"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		return nil
	}
	if decoded.Item.Type == "userMessage" {
		return nil
	}
	var out []string
	for _, c := range decoded.Item.Content {
		if c.Type == "text" && c.Text != "" {
			out = append(out, c.Text)
		}
	}
	return out
}

// send marshals and writes one JSON-RPC message.
func (s *session) send(v request) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.proc.WriteLine(b)
}

// read decodes the next inbound line into an envelope.
func (s *session) read() (rpcEnvelope, error) {
	raw, err := s.proc.ReadLine()
	if err != nil {
		return rpcEnvelope{}, err
	}
	var env rpcEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return rpcEnvelope{}, fmt.Errorf("codex: decoding server frame: %w", err)
	}
	return env, nil
}

// awaitReply reads until the reply with the given id, ignoring notifications
// that arrive first. A reply error is surfaced without its message text.
func (s *session) awaitReply(id int) (json.RawMessage, error) {
	for {
		env, err := s.read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("codex exited during the handshake")
			}
			return nil, err
		}
		if env.ID == nil || *env.ID != id {
			continue
		}
		if env.Error != nil {
			return nil, fmt.Errorf("codex rejected request %d", id)
		}
		return env.Result, nil
	}
}

// Close releases the process.
func (s *session) Close() error { return s.proc.Close() }

// Abort kills the process, interrupting an in-flight RunTurn.
func (s *session) Abort() { s.proc.Abort() }
