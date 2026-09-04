package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
)

// FakeOptions scripts the FakeAgent. It is a non-test type so both this
// package's tests and cmd tests can run the same agent as a subprocess.
type FakeOptions struct {
	// ProtocolVersion is what initialize reports; 0 means the real one.
	ProtocolVersion int
	// Loadable advertises the loadSession capability.
	Loadable bool
	// StopReason ends the prompt; empty means end_turn.
	StopReason string
	// PromptError, when set, rejects session/prompt with this message.
	PromptError string
	// Permission, when set, is the toolCall object of one
	// session/request_permission sent before the prompt reply. The decision
	// is echoed back as an agent text chunk "decision:<optionId|cancelled>".
	Permission json.RawMessage
	// OnlyAllowOptions offers only allow options on the permission request.
	OnlyAllowOptions bool
	// RequestFS sends an fs/read_text_file request before the prompt reply
	// and echoes the reply's error code as "fs:<code>".
	RequestFS bool
}

// The environment variables FakeFromEnv reads, so a test can script the fake
// through the inherited environment of the process the driver launches.
const (
	FakeEnv           = "TENON_ACP_FAKE"
	FakeEnvProtocol   = "TENON_ACP_FAKE_PROTOCOL"
	FakeEnvLoadable   = "TENON_ACP_FAKE_LOADABLE"
	FakeEnvStop       = "TENON_ACP_FAKE_STOP"
	FakeEnvPromptErr  = "TENON_ACP_FAKE_PROMPT_ERROR"
	FakeEnvPermission = "TENON_ACP_FAKE_PERMISSION"
	FakeEnvAllowOnly  = "TENON_ACP_FAKE_ALLOW_ONLY"
	FakeEnvRequestFS  = "TENON_ACP_FAKE_REQUEST_FS"
)

// FakeFromEnv reports whether the process should serve as the fake agent and
// the options scripted for it.
func FakeFromEnv() (FakeOptions, bool) {
	if os.Getenv(FakeEnv) != "1" {
		return FakeOptions{}, false
	}
	o := FakeOptions{
		Loadable:    os.Getenv(FakeEnvLoadable) == "1",
		StopReason:  os.Getenv(FakeEnvStop),
		PromptError: os.Getenv(FakeEnvPromptErr),
		RequestFS:   os.Getenv(FakeEnvRequestFS) == "1",
	}
	o.ProtocolVersion, _ = strconv.Atoi(os.Getenv(FakeEnvProtocol))
	o.OnlyAllowOptions = os.Getenv(FakeEnvAllowOnly) == "1"
	if p := os.Getenv(FakeEnvPermission); p != "" {
		o.Permission = json.RawMessage(p)
	}
	return o, true
}

// RunFake serves one ACP agent over r and w until r closes. It writes a
// diagnostic line to stderr so tests prove stderr is swallowed.
func RunFake(r io.Reader, w io.Writer, stderr io.Writer, o FakeOptions) error {
	fmt.Fprintln(stderr, "fake-acp-agent: SECRET-STDERR-LOG")
	f := &fakeAgent{in: bufio.NewScanner(r), out: w, opts: o, nextID: 1000}
	f.in.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for f.in.Scan() {
		var env envelope
		if err := json.Unmarshal(f.in.Bytes(), &env); err != nil {
			return err
		}
		if err := f.handle(env); err != nil {
			return err
		}
	}
	return f.in.Err()
}

type fakeAgent struct {
	in     *bufio.Scanner
	out    io.Writer
	opts   FakeOptions
	nextID int
}

func (f *fakeAgent) send(m message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f.out, "%s\n", b)
	return err
}

func (f *fakeAgent) reply(id json.RawMessage, result any) error {
	return f.send(message{JSONRPC: "2.0", ID: id, Result: result})
}

func (f *fakeAgent) chunk(text string) error {
	return f.send(message{JSONRPC: "2.0", Method: "session/update", Params: map[string]any{
		"sessionId": "fake-session",
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": text},
		},
	}})
}

// request sends one agent request and reads until its reply, returning the
// reply envelope.
func (f *fakeAgent) request(method string, params any) (envelope, error) {
	id := f.nextID
	f.nextID++
	rawID, _ := json.Marshal(id)
	if err := f.send(message{JSONRPC: "2.0", ID: rawID, Method: method, Params: params}); err != nil {
		return envelope{}, err
	}
	for f.in.Scan() {
		var env envelope
		if err := json.Unmarshal(f.in.Bytes(), &env); err != nil {
			return envelope{}, err
		}
		if string(env.ID) == string(rawID) && env.Method == "" {
			return env, nil
		}
	}
	return envelope{}, io.EOF
}

func (f *fakeAgent) handle(env envelope) error {
	switch env.Method {
	case "initialize":
		version := f.opts.ProtocolVersion
		if version == 0 {
			version = protocolVersion
		}
		return f.reply(env.ID, map[string]any{
			"protocolVersion":   version,
			"agentCapabilities": map[string]any{"loadSession": f.opts.Loadable},
			"agentInfo":         map[string]any{"name": "fake-acp-agent", "version": "0.0.0"},
		})
	case "session/new":
		return f.reply(env.ID, map[string]any{"sessionId": "fake-session"})
	case "session/load":
		if !f.opts.Loadable {
			return f.send(message{JSONRPC: "2.0", ID: env.ID, Error: &rpcError{Code: -32601, Message: "method not found"}})
		}
		// Replayed history arrives before the reply and must not be
		// re-emitted by the client.
		if err := f.chunk("replayed history"); err != nil {
			return err
		}
		return f.reply(env.ID, map[string]any{})
	case "session/prompt":
		return f.prompt(env)
	case "session/cancel":
		return nil
	}
	if len(env.ID) > 0 {
		return f.send(message{JSONRPC: "2.0", ID: env.ID, Error: &rpcError{Code: -32601, Message: "method not found"}})
	}
	return nil
}

func (f *fakeAgent) prompt(env envelope) error {
	if f.opts.PromptError != "" {
		return f.send(message{JSONRPC: "2.0", ID: env.ID, Error: &rpcError{Code: -32000, Message: f.opts.PromptError}})
	}
	if err := f.chunk("hello "); err != nil {
		return err
	}
	// A thought chunk and a tool call carry no model output for the seam.
	if err := f.send(message{JSONRPC: "2.0", Method: "session/update", Params: map[string]any{
		"sessionId": "fake-session",
		"update": map[string]any{
			"sessionUpdate": "agent_thought_chunk",
			"content":       map[string]any{"type": "text", "text": "THOUGHT"},
		},
	}}); err != nil {
		return err
	}
	if err := f.send(message{JSONRPC: "2.0", Method: "session/update", Params: map[string]any{
		"sessionId": "fake-session",
		"update":    map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "TOOL", "kind": "read"},
	}}); err != nil {
		return err
	}
	if f.opts.RequestFS {
		rep, err := f.request("fs/read_text_file", map[string]any{"sessionId": "fake-session", "path": "/etc/passwd"})
		if err != nil {
			return err
		}
		code := 0
		if rep.Error != nil {
			code = rep.Error.Code
		}
		if err := f.chunk(fmt.Sprintf("fs:%d", code)); err != nil {
			return err
		}
	}
	if f.opts.Permission != nil {
		options := []map[string]any{
			{"optionId": "allow-once", "name": "Allow", "kind": "allow_once"},
			{"optionId": "allow-always", "name": "Always allow", "kind": "allow_always"},
		}
		if !f.opts.OnlyAllowOptions {
			options = append(options,
				map[string]any{"optionId": "reject-once", "name": "Reject", "kind": "reject_once"},
				map[string]any{"optionId": "reject-always", "name": "Always reject", "kind": "reject_always"},
			)
		}
		rep, err := f.request("session/request_permission", map[string]any{
			"sessionId": "fake-session",
			"toolCall":  f.opts.Permission,
			"options":   options,
		})
		if err != nil {
			return err
		}
		var res struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		}
		_ = json.Unmarshal(rep.Result, &res)
		decision := res.Outcome.OptionID
		if res.Outcome.Outcome != "selected" {
			decision = res.Outcome.Outcome
		}
		if err := f.chunk("decision:" + decision); err != nil {
			return err
		}
	}
	if err := f.chunk("world"); err != nil {
		return err
	}
	stop := f.opts.StopReason
	if stop == "" {
		stop = "end_turn"
	}
	return f.reply(env.ID, map[string]any{"stopReason": stop})
}
