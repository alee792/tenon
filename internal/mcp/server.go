// Package mcp is tenon's managed tool boundary: one stdio MCP server that
// exposes the bounded built-in tools and the project's authored tools to both
// harnesses over JSON-RPC 2.0 framed as line-delimited JSON. Every request
// line, tool name, and argument is bounded and validated before anything runs,
// authored calls cross into the long-lived language hosts through one caller
// under tenon's own deadline, and audit output
// carries only a safe request identifier, a fixed tool name, and a lifecycle
// outcome — never arguments, outputs, notes, or error text. The boundary is
// additive: it does not disable, authorize, observe, or retry the harness's
// own native tools.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// Version is the single tenon version constant. A later release slice
	// owns real versioning; every version-bearing surface reads this one.
	Version = "0.1.0-dev"
	// ProtocolVersion is the MCP revision this server implements.
	ProtocolVersion = "2025-06-18"
	// serverName identifies the managed server to its client.
	serverName = "tenon-managed"

	// MaxTextBytes bounds one echo input.
	MaxTextBytes = 1024
	// MaxNoteBytes bounds one friction note accepted at the boundary. The
	// store bounds what it retains independently.
	MaxNoteBytes = 1024
	// MaxResultTextBytes bounds the text rendering of one authored tool
	// result. Structured content carries the whole result.
	MaxResultTextBytes = 4096
	// MaxErrorBytes bounds one model-visible tool error.
	MaxErrorBytes = 512
	// maxLineBytes bounds one request line. A longer line is not truncated
	// and not partially interpreted: the server reports it and exits.
	maxLineBytes = 64 << 10
)

// toolName is the portable tool-name grammar. A name outside it is refused
// before dispatch, so a native or third-party MCP name such as
// "github__get-repository" can never reach a managed handler.
var toolName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// unnamedTool stands in for a refused tool name in audit output. Only names
// that pass the grammar and name a known managed tool are ever written, so
// audit output cannot carry model-supplied text.
const unnamedTool = "unknown"

// Recorder retains one bounded friction note. It reports retention rather
// than failing: a full or broken inbox is a successful managed call whose
// result says the note was not kept.
type Recorder interface {
	Record(note string) bool
}

// Definition is one authored tool as its language host reported it. The
// managed surface publishes it verbatim: tenon validated the shape before the
// server opened and does not rewrite an author's words or schemas.
type Definition struct {
	Name         string
	Description  string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
}

// Caller runs one authored tool call against the long-lived language hosts.
// The boundary hands it validated, bounded arguments and expects one bounded
// JSON object back; a violation of the tool's own contract is an ordinary
// error, answered in band.
type Caller interface {
	Call(name string, arguments json.RawMessage) (json.RawMessage, error)
}

// Config is the served project's identity and the surface it opts into.
// Identity travels with the session so audit output and any recorded note
// name the exact agent; none of it is model-facing.
type Config struct {
	// Agent is the normalized agent name.
	Agent string
	// SourceFingerprint is the agent project's source fingerprint.
	SourceFingerprint string
	// FrictionNotes advertises record-friction when root instructions opted
	// in. When false the tool is neither listed nor callable.
	FrictionNotes bool
	// Recorder stores friction notes. A nil recorder records nothing.
	Recorder Recorder
	// Definitions are the authored tools to publish after the built-ins,
	// already validated and sorted by the tool runtime.
	Definitions []Definition
	// Tools runs authored tool calls. It must be present whenever
	// Definitions is non-empty; a nil caller serves no authored tool.
	Tools Caller
}

// authored returns the definition of one authored tool, if the served project
// declares it.
func (c Config) authored(name string) (Definition, bool) {
	if c.Tools == nil {
		return Definition{}, false
	}
	for _, d := range c.Definitions {
		if d.Name == name {
			return d, true
		}
	}
	return Definition{}, false
}

// Serve reads line-delimited JSON-RPC requests from in, writes responses to
// out, and writes one audit line per lifecycle event to audit. It returns
// when in is exhausted, when ctx is done, or on a bounded-input or audit
// failure; a protocol or tool-contract violation is answered in-band and the
// server keeps serving.
func Serve(ctx context.Context, in io.Reader, out, audit io.Writer, cfg Config) error {
	if cfg.Agent == "" || cfg.SourceFingerprint == "" {
		return errors.New("the managed server requires the served agent's name and source fingerprint")
	}
	s := &server{cfg: cfg, encoder: json.NewEncoder(out), audit: audit}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), maxLineBytes)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		s.handle(scanner.Bytes())
		if s.auditErr != nil {
			return s.auditErr
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return fmt.Errorf("one managed request exceeded the bounded line size of %d bytes", maxLineBytes)
		}
		return fmt.Errorf("reading managed requests: %w", err)
	}
	return nil
}

// server carries one session's identity, its response encoder, and its audit
// writer. An audit failure is fatal and recorded once: a boundary that cannot
// record what it did must stop serving rather than serve unobserved.
type server struct {
	cfg      Config
	encoder  *json.Encoder
	audit    io.Writer
	auditErr error
}

func (s *server) handle(line []byte) {
	var request struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &request); err != nil || request.JSONRPC != "2.0" || request.Method == "" {
		s.writeError(nil, -32600, "invalid request")
		return
	}
	// A notification carries no id and is answered by silence, including the
	// initialized handshake.
	if len(request.ID) == 0 || request.Method == "notifications/initialized" {
		return
	}
	switch request.Method {
	case "initialize":
		s.writeResult(request.ID, map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": serverName, "version": Version},
		})
	case "tools/list":
		s.writeResult(request.ID, map[string]any{"tools": tools(s.cfg)})
	case "tools/call":
		s.callTool(request.ID, request.Params)
	default:
		s.writeError(request.ID, -32601, "method not found")
	}
}

// callTool decodes one strictly bounded tool call, audits its lifecycle, and
// answers with the handler's result or a bounded in-band error. A refused
// call never returns its own arguments.
func (s *server) callTool(id, params json.RawMessage) {
	requestID := requestIdentifier(id, params)
	var decoded struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		// MCP permits a _meta member on every call; it is tolerated and
		// ignored rather than rejected as an unknown field.
		Meta json.RawMessage `json:"_meta"`
	}
	if err := decodeStrict(params, &decoded); err != nil || !toolName.MatchString(decoded.Name) {
		s.writeAudit(unnamedTool, requestID, "failed")
		s.writeToolError(id, "invalid managed tool call")
		return
	}
	handler, known := handlers[decoded.Name]
	if known && decoded.Name == frictionTool && !s.cfg.FrictionNotes {
		known = false
	}
	definition, authored := s.cfg.authored(decoded.Name)
	if !known && !authored {
		s.writeAudit(unnamedTool, requestID, "failed")
		s.writeToolError(id, "invalid managed tool call")
		return
	}
	if !known {
		// An authored tool is dispatched through the same lifecycle as a
		// built-in: the boundary audits, bounds, and validates identically
		// whoever wrote the tool.
		handler = func(s *server, c call) (map[string]any, error) { return s.callAuthored(definition, c) }
	}

	s.writeAudit(decoded.Name, requestID, "requested")
	result, err := handler(s, call{name: decoded.Name, arguments: decoded.Arguments, requestID: requestID})
	if err != nil {
		s.writeAudit(decoded.Name, requestID, "failed")
		s.writeToolError(id, err.Error())
		return
	}
	s.writeAudit(decoded.Name, requestID, "completed")
	s.writeResult(id, result)
}

// call is one decoded managed tool call.
type call struct {
	name      string
	arguments json.RawMessage
	requestID string
}

// handler validates its own arguments, audits authorization, and only then
// runs its body. Its error is model-visible and must stay bounded and free of
// the arguments that caused it.
type handler func(*server, call) (map[string]any, error)

const (
	echoTool     = "echo"
	frictionTool = "record-friction"
)

// handlers routes the fixed built-in tools. Every other name is refused
// unless the served project authored a tool by exactly that name.
var handlers = map[string]handler{
	echoTool:     callEcho,
	frictionTool: callRecordFriction,
}

// authorize records that the call passed the boundary's checks, immediately
// before its effect.
func (s *server) authorize(c call) {
	s.writeAudit(c.name, c.requestID, "authorized")
}

func callEcho(s *server, c call) (map[string]any, error) {
	var arguments struct {
		Text string `json:"text"`
	}
	if err := decodeStrict(c.arguments, &arguments); err != nil ||
		arguments.Text == "" || !utf8.ValidString(arguments.Text) || len(arguments.Text) > MaxTextBytes {
		return nil, fmt.Errorf("echo text must be one non-empty UTF-8 string of at most %d bytes", MaxTextBytes)
	}
	s.authorize(c)
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": arguments.Text}},
		"structuredContent": map[string]any{"text": arguments.Text},
		"isError":           false,
	}, nil
}

func callRecordFriction(s *server, c call) (map[string]any, error) {
	var arguments struct {
		Note string `json:"note"`
	}
	if err := decodeStrict(c.arguments, &arguments); err != nil || !utf8.ValidString(arguments.Note) ||
		strings.TrimSpace(arguments.Note) == "" || len(arguments.Note) > MaxNoteBytes {
		return nil, fmt.Errorf("the friction note must be one non-empty UTF-8 string of at most %d bytes", MaxNoteBytes)
	}
	s.authorize(c)
	// A full or failed inbox is a successful call reporting no retention:
	// the model has nothing to retry and learns nothing about the store.
	recorded := s.cfg.Recorder != nil && s.cfg.Recorder.Record(arguments.Note)
	structured := map[string]any{"recorded": recorded}
	encoded, err := json.Marshal(structured)
	if err != nil {
		return nil, errors.New("the friction note result could not be encoded")
	}
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(encoded)}},
		"structuredContent": structured,
		"isError":           false,
	}, nil
}

// callAuthored runs one authored tool through the long-lived language hosts.
// The arguments are bounded and proven to be one JSON object before they leave
// tenon, and the tool's own result must be one JSON object, published as
// structured content beside a bounded text rendering. A failure — the tool's,
// the host's, or the boundary's — is a bounded sentence: raw host output never
// reaches the model.
func (s *server) callAuthored(definition Definition, c call) (map[string]any, error) {
	arguments := c.arguments
	if len(bytes.TrimSpace(arguments)) == 0 {
		arguments = json.RawMessage("{}")
	}
	// The request line is already bounded, so size is settled; what remains
	// is that the arguments are one JSON object and not a bare value.
	var decoded map[string]any
	if err := json.Unmarshal(arguments, &decoded); err != nil || decoded == nil {
		return nil, errors.New("the tool arguments must be one JSON object")
	}
	s.authorize(c)

	output, err := s.cfg.Tools.Call(definition.Name, arguments)
	if err != nil {
		return nil, errors.New(boundMessage(err.Error()))
	}
	var structured map[string]any
	if err := json.Unmarshal(output, &structured); err != nil || structured == nil {
		return nil, errors.New("the tool returned something other than one JSON object")
	}
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": renderOutput(output)}},
		"structuredContent": structured,
		"isError":           false,
	}, nil
}

// renderOutput is the bounded text rendering of one tool result, for clients
// that read content rather than structured content.
func renderOutput(output json.RawMessage) string {
	var compact bytes.Buffer
	if err := json.Compact(&compact, output); err != nil {
		return "the tool result could not be rendered as text"
	}
	text := compact.String()
	if len(text) > MaxResultTextBytes {
		return text[:MaxResultTextBytes] + "..."
	}
	return text
}

// boundMessage trims a tool or host error to one bounded single-line sentence.
func boundMessage(message string) string {
	flat := strings.Join(strings.Fields(message), " ")
	if flat == "" {
		return "the tool call failed"
	}
	if len(flat) > MaxErrorBytes {
		return flat[:MaxErrorBytes] + "..."
	}
	return flat
}

// tools is the advertised managed surface: echo always, record-friction only
// when root instructions opted in, and then every authored tool, published
// exactly as its language host reported it.
func tools(cfg Config) []any {
	advertised := builtinTools(cfg.FrictionNotes)
	for _, d := range cfg.Definitions {
		if cfg.Tools == nil {
			break
		}
		advertised = append(advertised, map[string]any{
			"name":         d.Name,
			"description":  d.Description,
			"inputSchema":  d.InputSchema,
			"outputSchema": d.OutputSchema,
		})
	}
	return advertised
}

// builtinTools is the fixed built-in surface.
func builtinTools(frictionNotes bool) []any {
	advertised := []any{map[string]any{
		"name":        echoTool,
		"description": "Return bounded text through the managed boundary.",
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{"text": map[string]any{"type": "string", "maxLength": MaxTextBytes}},
			"required":             []string{"text"},
		},
		"outputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{"text": map[string]any{"type": "string"}},
			"required":             []string{"text"},
		},
		"annotations": map[string]any{"readOnlyHint": true, "idempotentHint": true, "openWorldHint": false},
	}}
	if !frictionNotes {
		return advertised
	}
	return append(advertised, map[string]any{
		"name": frictionTool,
		"description": "Retain one concise friction note in private local tenon state for later human review. " +
			"Use only after completing the primary task, when concrete material friction could improve the agent " +
			"project or its tenon integration. This is not telemetry and is not loaded into future sessions.",
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{"note": map[string]any{"type": "string", "maxLength": MaxNoteBytes}},
			"required":             []string{"note"},
		},
		"outputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{"recorded": map[string]any{"type": "boolean"}},
			"required":             []string{"recorded"},
		},
		"annotations": map[string]any{
			"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false,
		},
	})
}

// requestIdentifier derives a short, content-free correlation identifier from
// the request id and its raw parameters. It correlates audit lines to one
// call without carrying anything the call said.
func requestIdentifier(id, params json.RawMessage) string {
	sum := sha256.New()
	sum.Write(id)
	sum.Write(params)
	return hex.EncodeToString(sum.Sum(nil)[:8])
}

func (s *server) writeAudit(tool, requestID, outcome string) {
	if s.auditErr != nil {
		return
	}
	if _, err := fmt.Fprintf(s.audit, "managed agent=%s tool=%s request=%s outcome=%s\n",
		s.cfg.Agent, tool, requestID, outcome); err != nil {
		s.auditErr = errors.New("the managed boundary could not write its audit record")
	}
}

func (s *server) writeResult(id json.RawMessage, result any) {
	_ = s.encoder.Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: result})
}

// writeToolError answers a refused call in-band: MCP reports a tool-contract
// violation as an unsuccessful result, not a protocol error.
func (s *server) writeToolError(id json.RawMessage, message string) {
	s.writeResult(id, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": message}},
		"isError": true,
	})
}

func (s *server) writeError(id json.RawMessage, code int, message string) {
	_ = s.encoder.Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   any             `json:"error"`
	}{JSONRPC: "2.0", ID: id, Error: map[string]any{"code": code, "message": message}})
}

// decodeStrict decodes exactly one JSON value into target, rejecting unknown
// fields and trailing values so an unrecognized argument fails closed rather
// than being silently ignored.
func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("a managed call carries exactly one JSON value")
	}
	return nil
}
