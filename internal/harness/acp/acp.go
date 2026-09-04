// Package acp drives any Agent Client Protocol agent as a headless turn
// harness behind the harness.Driver seam. Each opened session launches one
// agent process (an adapter such as claude-agent-acp or codex-acp, or any
// registry agent) over stdio JSON-RPC, performs initialize and session/new (or
// session/load) once, then runs exactly one prompt turn to its stop reason.
//
// Tenon is a minimal client on purpose: it advertises no file-system or
// terminal capability, so the agent keeps its own native tools and the
// harness's own permission rules govern them; it passes no MCP servers, so the
// agent reads the applied native configuration from the workspace exactly as
// an interactive session would; and it answers session/request_permission
// only from an operator-supplied Policy, never by judgment.
//
// The same security boundary as the Codex driver runs through this package:
// no protocol text — a title, a raw input, an error message, a _meta value —
// is ever copied into an event, a reason, an error, or a log. Reasons are a
// fixed vocabulary.
package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/alee792/tenon/internal/harness"
)

// protocolVersion is the ACP major version this client speaks.
const protocolVersion = 1

// DefaultCommand is the launch command for the reference adapter of a
// harness, resolved on PATH like the native executables are. Acquiring the
// adapter is the operator's act; tenon never fetches one.
func DefaultCommand(harnessName string) []string {
	switch harnessName {
	case "claude":
		return []string{"claude-agent-acp"}
	case "codex":
		return []string{"codex-acp"}
	}
	return nil
}

// Driver opens ACP sessions against one launch command. harness is the applied
// harness name the dispatcher stamps on events (the compile target), which is
// independent of which agent process serves the turn.
type Driver struct {
	harness string
	command []string
	policy  Policy
	version string
}

// NewDriver constructs a driver. command is the agent's launch command and
// arguments; an empty command uses DefaultCommand for the harness. version is
// tenon's version, reported on initialize.
func NewDriver(harnessName string, command []string, policy Policy, version string) (Driver, error) {
	if len(command) == 0 {
		command = DefaultCommand(harnessName)
	}
	if len(command) == 0 || command[0] == "" {
		return Driver{}, fmt.Errorf("no ACP agent command for harness %q", harnessName)
	}
	if err := policy.Validate(); err != nil {
		return Driver{}, err
	}
	return Driver{harness: harnessName, command: command, policy: policy, version: version}, nil
}

// Command reports the launch command the driver will run.
func (d Driver) Command() []string { return append([]string(nil), d.command...) }

// Name reports the applied harness name.
func (d Driver) Name() string { return d.harness }

// Verify reports whether the agent executable resolves. Adapters do not
// uniformly support --version, so resolution on PATH is the whole check.
func (d Driver) Verify(ctx context.Context) error {
	_, err := exec.LookPath(d.command[0])
	return err
}

// Open launches the agent, initializes, and starts or loads the session,
// holding the session event until RunTurn emits it.
func (d Driver) Open(ctx context.Context, req harness.OpenRequest) (harness.Session, error) {
	proc, err := harness.StartProcess(ctx, d.command[0], req.Workspace, d.command[1:]...)
	if err != nil {
		return nil, err
	}
	s := &session{proc: proc, policy: d.policy, nextID: 1}
	loadable, err := s.initialize(d.version)
	if err != nil {
		proc.Close()
		return nil, err
	}
	ev, err := s.open(req, loadable)
	if err != nil {
		proc.Close()
		return nil, err
	}
	s.pending = &ev
	return s, nil
}

// session is one agent process driven for exactly one turn.
type session struct {
	proc      *harness.Process
	policy    Policy
	sessionID string
	nextID    int
	// writeMu serializes stdin writes: permission replies are written from
	// the read loop while Abort may write a cancel from another goroutine.
	writeMu sync.Mutex
	// pending holds the session started/loaded event captured during Open so
	// RunTurn emits it before streaming, keeping every emit on the turn path.
	pending *harness.Event
}

// envelope is the common shape of any inbound line. ID is kept raw so an
// agent's own request ids (which may be strings) are echoed back verbatim.
type envelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// rpcError is a reply error. Its Message may carry credential material and is
// never surfaced beyond safeReason's fixed vocabulary.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// message is one outbound JSON-RPC request, notification, or reply.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// initialize negotiates the protocol version with no client capabilities and
// reports whether the agent can load sessions.
func (s *session) initialize(version string) (loadable bool, err error) {
	result, err := s.call("initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{"name": "tenon", "version": version},
	}, nil)
	if err != nil {
		return false, err
	}
	var init struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(result, &init); err != nil {
		return false, errors.New("acp: agent sent a malformed initialize result")
	}
	if init.ProtocolVersion != protocolVersion {
		return false, fmt.Errorf("acp: agent speaks protocol version %d, tenon speaks %d", init.ProtocolVersion, protocolVersion)
	}
	return init.AgentCapabilities.LoadSession, nil
}

// open starts a fresh session or loads the recorded one. A resume against an
// agent that cannot load sessions is refused rather than silently started
// fresh: the dispatcher recorded a session to continue, and continuing a
// different one would be an unproven result.
func (s *session) open(req harness.OpenRequest, loadable bool) (harness.Event, error) {
	resume := req.ResumeID != "" && !req.Fresh
	if !resume {
		result, err := s.call("session/new", map[string]any{
			"cwd":        req.Workspace,
			"mcpServers": []any{},
		}, nil)
		if err != nil {
			return harness.Event{}, err
		}
		var out struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(result, &out); err != nil || out.SessionID == "" {
			return harness.Event{}, errors.New("acp: agent did not provide a session id")
		}
		s.sessionID = out.SessionID
		return harness.Event{Type: harness.EventSessionStarted, SessionID: out.SessionID}, nil
	}
	if !loadable {
		return harness.Event{}, errors.New("acp: agent cannot load sessions, so the recorded session cannot be resumed")
	}
	s.sessionID = req.ResumeID
	// The agent replays history as session/update notifications before it
	// replies; call ignores them, so replayed output is never re-emitted.
	if _, err := s.call("session/load", map[string]any{
		"sessionId":  req.ResumeID,
		"cwd":        req.Workspace,
		"mcpServers": []any{},
	}, nil); err != nil {
		return harness.Event{}, err
	}
	return harness.Event{Type: harness.EventSessionResumed, SessionID: req.ResumeID}, nil
}

// RunTurn sends one prompt and reads updates until the reply, emitting the
// session event first and one output delta per agent text chunk.
func (s *session) RunTurn(ctx context.Context, in harness.Input, emit func(harness.Event)) (harness.TurnResult, error) {
	if s.pending != nil {
		emit(*s.pending)
		s.pending = nil
	}
	result, err := s.call("session/prompt", map[string]any{
		"sessionId": s.sessionID,
		"prompt":    []map[string]any{{"type": "text", "text": in.Text}},
	}, func(update json.RawMessage) {
		for _, delta := range chunkDeltas(update) {
			emit(harness.Event{Type: harness.EventAgentOutputDelta, Delta: delta})
		}
	})
	var rejected *rejectedError
	if errors.As(err, &rejected) {
		// The agent answered: it declined the prompt. That is a proven
		// terminal, classified without its message text.
		return harness.TurnResult{SessionID: s.sessionID, Status: harness.StatusFailed, Reason: rejected.reason}, nil
	}
	if err != nil {
		return harness.TurnResult{}, err
	}
	var out struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		return harness.TurnResult{}, errors.New("acp: agent sent a malformed prompt result")
	}
	return classifyStop(s.sessionID, out.StopReason), nil
}

// classifyStop maps a stop reason onto a TurnResult. Stop reasons are a closed
// protocol vocabulary, so one is safe to carry as the reason verbatim.
func classifyStop(sessionID, stop string) harness.TurnResult {
	switch stop {
	case "end_turn":
		return harness.TurnResult{SessionID: sessionID, Status: harness.StatusCompleted}
	case "cancelled":
		return harness.TurnResult{SessionID: sessionID, Status: harness.StatusCancelled, Reason: stop}
	case "refusal", "max_tokens", "max_turn_requests":
		return harness.TurnResult{SessionID: sessionID, Status: harness.StatusFailed, Reason: stop}
	}
	return harness.TurnResult{SessionID: sessionID, Status: harness.StatusFailed, Reason: "turn_failed"}
}

// chunkDeltas extracts the text of an agent_message_chunk update, or nothing
// for every other update kind (thoughts, tool calls, plans, usage).
func chunkDeltas(update json.RawMessage) []string {
	var u struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if err := json.Unmarshal(update, &u); err != nil {
		return nil
	}
	if u.Update.SessionUpdate != "agent_message_chunk" || u.Update.Content.Type != "text" || u.Update.Content.Text == "" {
		return nil
	}
	return []string{u.Update.Content.Text}
}

// rejectedError is a JSON-RPC error reply to one of tenon's requests, reduced
// to a bounded reason.
type rejectedError struct {
	method string
	reason string
}

func (e *rejectedError) Error() string {
	return fmt.Sprintf("acp: agent rejected %s (%s)", e.method, e.reason)
}

// safeReason reduces an error message to a fixed vocabulary. The message is
// consulted, never copied.
func safeReason(msg string) string {
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "401") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "auth_required") ||
		strings.Contains(lower, "not authenticated") {
		return "authentication"
	}
	return "prompt_rejected"
}

// call sends one request and reads until its reply, servicing everything that
// arrives first: session/update notifications go to onUpdate (when non-nil),
// agent requests are answered — permission by policy, anything else with
// method-not-found — and unrelated replies are ignored.
func (s *session) call(method string, params any, onUpdate func(json.RawMessage)) (json.RawMessage, error) {
	id := s.nextID
	s.nextID++
	rawID, _ := json.Marshal(id)
	if err := s.write(message{JSONRPC: "2.0", ID: rawID, Method: method, Params: params}); err != nil {
		return nil, fmt.Errorf("acp: writing %s: %w", method, err)
	}
	for {
		raw, err := s.proc.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("acp: agent exited before replying to %s", method)
			}
			return nil, fmt.Errorf("acp: reading agent output: %w", err)
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, errors.New("acp: agent sent a malformed frame")
		}
		switch {
		case env.Method == "" && len(env.ID) > 0:
			// A reply to one of ours.
			if !bytes.Equal(env.ID, rawID) {
				continue
			}
			if env.Error != nil {
				return nil, &rejectedError{method: method, reason: safeReason(env.Error.Message)}
			}
			return env.Result, nil
		case env.Method != "" && len(env.ID) > 0 && !bytes.Equal(env.ID, []byte("null")):
			// A request from the agent.
			if err := s.answer(env); err != nil {
				return nil, err
			}
		case env.Method == "session/update":
			if onUpdate != nil {
				onUpdate(env.Params)
			}
		}
		// Other notifications carry nothing tenon acts on.
	}
}

// answer replies to one agent request: permission by policy, everything else
// (fs/*, terminal/*, and unknown methods) with method-not-found, since tenon
// advertised none of those capabilities.
func (s *session) answer(env envelope) error {
	if env.Method != "session/request_permission" {
		return s.write(message{JSONRPC: "2.0", ID: env.ID, Error: &rpcError{Code: -32601, Message: "method not found"}})
	}
	var req permissionRequest
	if err := json.Unmarshal(env.Params, &req); err != nil {
		return s.write(message{JSONRPC: "2.0", ID: env.ID, Error: &rpcError{Code: -32602, Message: "invalid params"}})
	}
	decision := s.policy.Decide(req.call())
	optionID, ok := req.option(decision.Action)
	var outcome map[string]any
	if ok {
		outcome = map[string]any{"outcome": "selected", "optionId": optionID}
	} else {
		// The agent offered no option of the decided kind; declining to
		// choose is the only honest answer left, and the protocol's word
		// for a request answered without a selection is cancelled.
		outcome = map[string]any{"outcome": "cancelled"}
	}
	return s.write(message{JSONRPC: "2.0", ID: env.ID, Result: map[string]any{"outcome": outcome}})
}

// permissionRequest is the policy-relevant projection of a
// session/request_permission request.
type permissionRequest struct {
	ToolCall struct {
		Kind      string `json:"kind"`
		Title     string `json:"title"`
		Locations []struct {
			Path string `json:"path"`
		} `json:"locations"`
		Meta struct {
			ClaudeCode struct {
				ToolName string `json:"toolName"`
			} `json:"claudeCode"`
		} `json:"_meta"`
	} `json:"toolCall"`
	Options []struct {
		OptionID string `json:"optionId"`
		Kind     string `json:"kind"`
	} `json:"options"`
}

func (r permissionRequest) call() Call {
	c := Call{
		Tool:  r.ToolCall.Meta.ClaudeCode.ToolName,
		Kind:  r.ToolCall.Kind,
		Title: r.ToolCall.Title,
	}
	for _, l := range r.ToolCall.Locations {
		if l.Path != "" {
			c.Paths = append(c.Paths, l.Path)
		}
	}
	return c
}

// option picks the agent's option for an action: the once variant first, so a
// decision is never remembered by the agent beyond this call, then the always
// variant. It reports false when the agent offered neither.
func (r permissionRequest) option(a Action) (string, bool) {
	var prefer []string
	if a == Allow {
		prefer = []string{"allow_once", "allow_always"}
	} else {
		prefer = []string{"reject_once", "reject_always"}
	}
	for _, kind := range prefer {
		for _, o := range r.Options {
			if o.Kind == kind && o.OptionID != "" {
				return o.OptionID, true
			}
		}
	}
	return "", false
}

// write marshals and writes one message under the write lock.
func (s *session) write(m message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.proc.WriteLine(b)
}

// Close releases the process.
func (s *session) Close() error { return s.proc.Close() }

// Abort sends session/cancel on a best-effort basis, then kills the process,
// interrupting an in-flight RunTurn.
func (s *session) Abort() {
	if s.sessionID != "" {
		_ = s.write(message{JSONRPC: "2.0", Method: "session/cancel", Params: map[string]any{"sessionId": s.sessionID}})
	}
	s.proc.Abort()
}
