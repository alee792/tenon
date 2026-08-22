package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// stubRecorder stands in for the friction inbox: it counts calls, keeps the
// last note, and reports whatever retention the test needs.
type stubRecorder struct {
	recorded bool
	calls    int
	note     string
}

func (r *stubRecorder) Record(note string) bool {
	r.calls++
	r.note = note
	return r.recorded
}

func config(frictionNotes bool, recorder Recorder) Config {
	return Config{
		Agent:             "my-agent",
		SourceFingerprint: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		FrictionNotes:     frictionNotes,
		Recorder:          recorder,
	}
}

// serve drives one session over in-memory buffers and returns the decoded
// responses with the audit output.
func serve(t *testing.T, cfg Config, requests ...string) ([]map[string]any, string) {
	t.Helper()
	responses, audit, err := serveRaw(t, cfg, strings.Join(requests, "\n")+"\n")
	if err != nil {
		t.Fatalf("serve failed: %v", err)
	}
	return responses, audit
}

func serveRaw(t *testing.T, cfg Config, input string) ([]map[string]any, string, error) {
	t.Helper()
	var out, audit bytes.Buffer
	err := Serve(context.Background(), strings.NewReader(input), &out, &audit, cfg)
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var decoded map[string]any
		if jsonErr := json.Unmarshal([]byte(line), &decoded); jsonErr != nil {
			t.Fatalf("response line %q is not JSON: %v", line, jsonErr)
		}
		responses = append(responses, decoded)
	}
	return responses, audit.String(), err
}

func result(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	value, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("response carries no result: %#v", response)
	}
	return value
}

func toolNames(t *testing.T, response map[string]any) []string {
	t.Helper()
	listed, ok := result(t, response)["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list carries no tools: %#v", response)
	}
	var names []string
	for _, tool := range listed {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	return names
}

// TestInitializeAnnouncesTheManagedServer proves the handshake shape and that
// the initialized notification is answered by silence.
func TestInitializeAnnouncesTheManagedServer(t *testing.T) {
	responses, _ := serve(t, config(false, nil),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	if len(responses) != 1 {
		t.Fatalf("got %d responses, want exactly one; a notification must not be answered", len(responses))
	}
	initialize := result(t, responses[0])
	if initialize["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocolVersion = %v, want %s", initialize["protocolVersion"], ProtocolVersion)
	}
	tools := initialize["capabilities"].(map[string]any)["tools"].(map[string]any)
	if tools["listChanged"] != false {
		t.Fatalf("capabilities = %#v", initialize["capabilities"])
	}
	info := initialize["serverInfo"].(map[string]any)
	if info["name"] != "tenon-managed" || info["version"] != Version {
		t.Fatalf("serverInfo = %#v", info)
	}
}

// TestToolListIsEchoUntilFrictionIsOptedIn proves record-friction is
// advertised only when root instructions opt in, and that its bounded schema
// and annotations are exactly the specified ones.
func TestToolListIsEchoUntilFrictionIsOptedIn(t *testing.T) {
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

	responses, _ := serve(t, config(false, nil), list)
	if names := toolNames(t, responses[0]); len(names) != 1 || names[0] != "echo" {
		t.Fatalf("default managed surface = %v, want echo alone", names)
	}

	responses, _ = serve(t, config(true, &stubRecorder{}), list)
	names := toolNames(t, responses[0])
	if len(names) != 2 || names[0] != "echo" || names[1] != "record-friction" {
		t.Fatalf("opted-in managed surface = %v", names)
	}
	listed := result(t, responses[0])["tools"].([]any)
	echo := listed[0].(map[string]any)
	if echo["description"] != "Return bounded text through the managed boundary." {
		t.Fatalf("echo description = %v", echo["description"])
	}
	echoInput := echo["inputSchema"].(map[string]any)
	if echoInput["additionalProperties"] != false ||
		echoInput["properties"].(map[string]any)["text"].(map[string]any)["maxLength"] != float64(MaxTextBytes) {
		t.Fatalf("echo inputSchema = %#v", echoInput)
	}
	echoOutput := echo["outputSchema"].(map[string]any)
	if _, bounded := echoOutput["properties"].(map[string]any)["text"].(map[string]any)["maxLength"]; bounded {
		t.Fatalf("echo outputSchema must mirror the input without a maxLength: %#v", echoOutput)
	}
	if got := echo["annotations"].(map[string]any); got["readOnlyHint"] != true ||
		got["idempotentHint"] != true || got["openWorldHint"] != false {
		t.Fatalf("echo annotations = %#v", got)
	}
	friction := listed[1].(map[string]any)
	if got := friction["annotations"].(map[string]any); got["readOnlyHint"] != false ||
		got["destructiveHint"] != false || got["idempotentHint"] != false || got["openWorldHint"] != false {
		t.Fatalf("record-friction annotations = %#v", got)
	}
	frictionOutput := friction["outputSchema"].(map[string]any)
	if _, ok := frictionOutput["properties"].(map[string]any)["recorded"]; !ok {
		t.Fatalf("record-friction outputSchema = %#v", frictionOutput)
	}
}

// TestEchoReturnsBoundedTextAndRefusesEverythingElse proves the echo
// contract: a valid call round-trips as content and structured content, and
// an unknown argument, an empty text, and an oversize text each fail closed
// in band without returning the input.
func TestEchoReturnsBoundedTextAndRefusesEverythingElse(t *testing.T) {
	oversize := strings.Repeat("x", MaxTextBytes+1)
	responses, _ := serve(t, config(false, nil),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hello"},"_meta":{"progressToken":1}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hello","tone":"warm"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":""}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{"text":"`+oversize+`"}}}`)

	success := result(t, responses[0])
	if success["isError"] != false {
		t.Fatalf("valid echo failed: %#v", success)
	}
	if success["structuredContent"].(map[string]any)["text"] != "hello" {
		t.Fatalf("structured echo = %#v", success)
	}
	content := success["content"].([]any)[0].(map[string]any)
	if content["type"] != "text" || content["text"] != "hello" {
		t.Fatalf("echo content = %#v", content)
	}
	for i, response := range responses[1:] {
		refused := result(t, response)
		if refused["isError"] != true {
			t.Fatalf("refusal %d was accepted: %#v", i, refused)
		}
		message := refused["content"].([]any)[0].(map[string]any)["text"].(string)
		if strings.Contains(message, oversize) || strings.Contains(message, "warm") {
			t.Fatalf("a refusal must never echo the input back: %q", message)
		}
	}
}

// TestRecordFrictionValidatesBeforeTheStoreAndReportsRetention proves a valid
// note reaches the store, a full or failed store is still a successful call
// reporting recorded false, and an invalid note never reaches the store.
func TestRecordFrictionValidatesBeforeTheStoreAndReportsRetention(t *testing.T) {
	recorder := &stubRecorder{recorded: true}
	responses, _ := serve(t, config(true, recorder),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"record-friction","arguments":{"note":"The managed tool contract needed rereading."}}}`)
	stored := result(t, responses[0])
	if stored["isError"] != false || stored["structuredContent"].(map[string]any)["recorded"] != true {
		t.Fatalf("stored note result = %#v", stored)
	}
	if recorder.calls != 1 || recorder.note != "The managed tool contract needed rereading." {
		t.Fatalf("recorder = %+v", recorder)
	}

	full := &stubRecorder{recorded: false}
	responses, _ = serve(t, config(true, full),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"record-friction","arguments":{"note":"Another note."}}}`)
	unretained := result(t, responses[0])
	if unretained["isError"] != false || unretained["structuredContent"].(map[string]any)["recorded"] != false {
		t.Fatalf("a full store must be a successful call reporting no retention: %#v", unretained)
	}

	rejected := &stubRecorder{recorded: true}
	responses, _ = serve(t, config(true, rejected),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"record-friction","arguments":{"note":"   "}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"record-friction","arguments":{"note":"ok","cause":"guess"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"record-friction","arguments":{"note":"`+strings.Repeat("n", MaxNoteBytes+1)+`"}}}`)
	for i, response := range responses {
		if result(t, response)["isError"] != true {
			t.Fatalf("invalid note %d was accepted: %#v", i, response)
		}
	}
	if rejected.calls != 0 {
		t.Fatalf("an invalid note reached the store %d times", rejected.calls)
	}
}

// TestFrictionToolIsUncallableWithoutOptIn proves the tool is not merely
// unadvertised when instructions did not opt in.
func TestFrictionToolIsUncallableWithoutOptIn(t *testing.T) {
	recorder := &stubRecorder{recorded: true}
	responses, _ := serve(t, config(false, recorder),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"record-friction","arguments":{"note":"A note."}}}`)
	if result(t, responses[0])["isError"] != true || recorder.calls != 0 {
		t.Fatalf("record-friction served without opt-in: %#v, %d calls", responses[0], recorder.calls)
	}
}

// TestNativeToolNamesNeverReachTheManagedBoundary proves the portable
// name gate: a native or third-party MCP tool name is refused before dispatch
// and never named in audit output.
func TestNativeToolNamesNeverReachTheManagedBoundary(t *testing.T) {
	responses, audit := serve(t, config(false, nil),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github__get-repository","arguments":{"owner":"acme"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read-file","arguments":{"path":"/etc/passwd"}}}`)
	for i, response := range responses {
		refused := result(t, response)
		if refused["isError"] != true {
			t.Fatalf("call %d was accepted: %#v", i, refused)
		}
		if refused["content"].([]any)[0].(map[string]any)["text"] != "invalid managed tool call" {
			t.Fatalf("call %d refusal = %#v", i, refused)
		}
	}
	if strings.Contains(audit, "github") || strings.Contains(audit, "read-file") || strings.Contains(audit, "acme") {
		t.Fatalf("audit named a refused tool or its arguments: %q", audit)
	}
	if strings.Count(audit, "outcome=failed") != 2 {
		t.Fatalf("both refusals must be audited: %q", audit)
	}
}

// TestProtocolErrorsAreJSONRPCErrors proves an unknown method and malformed
// input use the JSON-RPC error channel rather than a tool result.
func TestProtocolErrorsAreJSONRPCErrors(t *testing.T) {
	responses, _ := serve(t, config(false, nil),
		`{"jsonrpc":"2.0","id":1,"method":"resources/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,`,
		`{"id":3,"method":"tools/list"}`)

	unknown := responses[0]["error"].(map[string]any)
	if unknown["code"] != float64(-32601) || unknown["message"] != "method not found" {
		t.Fatalf("unknown method = %#v", unknown)
	}
	for _, response := range responses[1:] {
		malformed := response["error"].(map[string]any)
		if malformed["code"] != float64(-32600) || malformed["message"] != "invalid request" {
			t.Fatalf("malformed request = %#v", malformed)
		}
	}
}

// TestOversizedRequestLineStopsTheServer proves the bounded line size: an
// overlong line is neither truncated nor partially interpreted.
func TestOversizedRequestLineStopsTheServer(t *testing.T) {
	oversize := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"` +
		strings.Repeat("x", maxLineBytes) + `"}}}`
	responses, _, err := serveRaw(t, config(false, nil), oversize+"\n")
	if err == nil {
		t.Fatal("an oversized request line must stop the server")
	}
	if !strings.Contains(err.Error(), "bounded line size") {
		t.Fatalf("error must name the bounded line size: %v", err)
	}
	if len(responses) != 0 {
		t.Fatalf("an oversized line must not be answered: %#v", responses)
	}
}

// TestServeRequiresServedIdentity proves the boundary refuses to serve a
// session it cannot name in audit output.
func TestServeRequiresServedIdentity(t *testing.T) {
	var out, audit bytes.Buffer
	err := Serve(context.Background(), strings.NewReader(""), &out, &audit, Config{Agent: "my-agent"})
	if err == nil {
		t.Fatal("an identity-free session must be refused")
	}
}

// TestAuditIsContentFreeAcrossTheLifecycle proves spec acceptance 10: a
// conspicuous note and a conspicuous echo argument appear nowhere in audit
// output, while every lifecycle outcome does.
func TestAuditIsContentFreeAcrossTheLifecycle(t *testing.T) {
	const conspicuousNote = "CONSPICUOUS-NOTE-c0ffee"
	const conspicuousText = "CONSPICUOUS-ARGUMENT-decafbad"
	responses, audit := serve(t, config(true, &stubRecorder{recorded: true}),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"`+conspicuousText+`"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"record-friction","arguments":{"note":"`+conspicuousNote+`"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":""}}}`)

	if result(t, responses[0])["structuredContent"].(map[string]any)["text"] != conspicuousText {
		t.Fatalf("echo must still return its text to the model: %#v", responses[0])
	}
	for _, secret := range []string{conspicuousNote, conspicuousText, "CONSPICUOUS"} {
		if strings.Contains(audit, secret) {
			t.Fatalf("audit carried model-visible content %q: %q", secret, audit)
		}
	}
	for _, outcome := range []string{"requested", "authorized", "completed", "failed"} {
		if !strings.Contains(audit, "outcome="+outcome) {
			t.Fatalf("audit is missing outcome=%s: %q", outcome, audit)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(audit), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 5 || fields[0] != "managed" || !strings.HasPrefix(fields[1], "agent=my-agent") ||
			!strings.HasPrefix(fields[2], "tool=") || len(strings.TrimPrefix(fields[3], "request=")) != 16 {
			t.Fatalf("audit line %q is not the bounded managed audit form", line)
		}
	}
}

// TestCanceledContextStopsServing proves the session ends when its context
// is done rather than draining queued requests.
func TestCanceledContextStopsServing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, audit bytes.Buffer
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n"
	if err := Serve(ctx, strings.NewReader(input), &out, &audit, config(false, nil)); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("a canceled session must not answer: %q", out.String())
	}
}

// stubCaller stands in for the language hosts: it records what crossed the
// boundary and returns whatever the test needs.
type stubCaller struct {
	calls     int
	name      string
	arguments string
	output    string
	err       error
}

func (c *stubCaller) Call(name string, arguments json.RawMessage) (json.RawMessage, error) {
	c.calls++
	c.name = name
	c.arguments = string(arguments)
	if c.err != nil {
		return nil, c.err
	}
	return json.RawMessage(c.output), nil
}

// authoredConfig serves one authored tool with a conspicuous schema, so a
// rewritten or re-derived surface is visible in the test.
func authoredConfig(caller Caller) Config {
	cfg := config(false, nil)
	cfg.Definitions = []Definition{{
		Name:         "hash-text",
		Description:  "Hash bounded text with SHA-256.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"hex":{"type":"string"}},"required":["hex"],"additionalProperties":false}`),
	}}
	cfg.Tools = caller
	return cfg
}

// TestAuthoredToolsArePublishedAfterTheBuiltIns proves the authored surface
// joins the managed one verbatim: tenon publishes the author's own name,
// description, and schemas, after echo and record-friction.
func TestAuthoredToolsArePublishedAfterTheBuiltIns(t *testing.T) {
	cfg := authoredConfig(&stubCaller{output: `{"hex":"ab"}`})
	cfg.FrictionNotes = true
	cfg.Recorder = &stubRecorder{}

	responses, _ := serve(t, cfg, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	names := toolNames(t, responses[0])
	if len(names) != 3 || names[0] != "echo" || names[1] != "record-friction" || names[2] != "hash-text" {
		t.Fatalf("managed surface = %v, want the built-ins then the authored tool", names)
	}
	listed := result(t, responses[0])["tools"].([]any)[2].(map[string]any)
	if listed["description"] != "Hash bounded text with SHA-256." {
		t.Fatalf("description = %v, want the author's own words", listed["description"])
	}
	input := listed["inputSchema"].(map[string]any)
	if input["type"] != "object" || input["additionalProperties"] != false ||
		input["properties"].(map[string]any)["text"].(map[string]any)["type"] != "string" {
		t.Fatalf("inputSchema = %#v, want the reported schema verbatim", input)
	}
	if listed["outputSchema"].(map[string]any)["properties"].(map[string]any)["hex"] == nil {
		t.Fatalf("outputSchema = %#v", listed["outputSchema"])
	}
}

// TestAuthoredToolCallRoundTripsAndAudits proves an authored call crosses the
// boundary with its arguments intact, returns structured content beside a
// bounded text rendering, and audits through the same content-free lifecycle
// as a built-in.
func TestAuthoredToolCallRoundTripsAndAudits(t *testing.T) {
	caller := &stubCaller{output: `{"hex":"CONSPICUOUS-OUTPUT"}`}
	responses, audit := serve(t, authoredConfig(caller),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hash-text","arguments":{"text":"hi"}}}`)

	called := result(t, responses[0])
	if called["isError"] != false {
		t.Fatalf("authored call failed: %#v", called)
	}
	if called["structuredContent"].(map[string]any)["hex"] != "CONSPICUOUS-OUTPUT" {
		t.Fatalf("structuredContent = %#v", called["structuredContent"])
	}
	text := called["content"].([]any)[0].(map[string]any)["text"].(string)
	if text != `{"hex":"CONSPICUOUS-OUTPUT"}` {
		t.Fatalf("text rendering = %q", text)
	}
	if caller.calls != 1 || caller.name != "hash-text" || caller.arguments != `{"text":"hi"}` {
		t.Fatalf("caller = %+v", caller)
	}
	for _, outcome := range []string{"requested", "authorized", "completed"} {
		if !strings.Contains(audit, "managed agent=my-agent tool=hash-text request=") ||
			!strings.Contains(audit, "outcome="+outcome) {
			t.Fatalf("audit is missing tool=hash-text outcome=%s: %q", outcome, audit)
		}
	}
	if strings.Contains(audit, "CONSPICUOUS") {
		t.Fatalf("audit must stay content-free: %q", audit)
	}
}

// TestAuthoredToolFailuresAreBoundedInBandErrors proves a failing tool is an
// unsuccessful result carrying one bounded sentence — never a protocol error,
// never unbounded host output.
func TestAuthoredToolFailuresAreBoundedInBandErrors(t *testing.T) {
	caller := &stubCaller{err: errors.New(strings.Repeat("z", 4096) + "\nsecond line")}
	responses, audit := serve(t, authoredConfig(caller),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hash-text","arguments":{"text":"hi"}}}`)

	failed := result(t, responses[0])
	if failed["isError"] != true {
		t.Fatalf("a failing tool must be an in-band error result: %#v", failed)
	}
	message := failed["content"].([]any)[0].(map[string]any)["text"].(string)
	if len(message) > MaxErrorBytes+3 || strings.Contains(message, "\n") {
		t.Fatalf("a tool error must be one bounded line: %d bytes", len(message))
	}
	if !strings.Contains(audit, "outcome=failed") {
		t.Fatalf("a failing authored call must audit its outcome: %q", audit)
	}

	// A tool whose result is not one JSON object never becomes structured
	// content.
	responses, _ = serve(t, authoredConfig(&stubCaller{output: `[1,2]`}),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hash-text","arguments":{"text":"hi"}}}`)
	if result(t, responses[0])["isError"] != true {
		t.Fatalf("a non-object result must fail: %#v", responses[0])
	}
}

// TestAuthoredToolArgumentsAreValidatedBeforeTheHost proves malformed
// arguments never reach a language host, and that an oversized call is
// refused by the line bound before it is ever decoded.
func TestAuthoredToolArgumentsAreValidatedBeforeTheHost(t *testing.T) {
	caller := &stubCaller{output: `{"hex":"ab"}`}
	responses, _ := serve(t, authoredConfig(caller),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hash-text","arguments":"not an object"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"hash-text","arguments":[1,2]}}`)

	for i, response := range responses {
		refused := result(t, response)
		if refused["isError"] != true {
			t.Fatalf("refusal %d was accepted: %#v", i, refused)
		}
	}
	if caller.calls != 0 {
		t.Fatalf("invalid arguments reached the host %d times", caller.calls)
	}

	oversize := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hash-text","arguments":{"text":"` +
		strings.Repeat("x", maxLineBytes) + `"}}}` + "\n"
	if _, _, err := serveRaw(t, authoredConfig(caller), oversize); err == nil {
		t.Fatal("a request line over its bound must stop the session")
	}
	if caller.calls != 0 {
		t.Fatalf("an oversized line reached the host %d times", caller.calls)
	}
}

// TestAuthoredToolsNeedARuntime proves a published definition without an open
// runtime is not merely unadvertised but uncallable.
func TestAuthoredToolsNeedARuntime(t *testing.T) {
	cfg := authoredConfig(nil)
	cfg.Tools = nil

	responses, _ := serve(t, cfg,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"hash-text","arguments":{"text":"hi"}}}`)
	if names := toolNames(t, responses[0]); len(names) != 1 || names[0] != "echo" {
		t.Fatalf("surface without a runtime = %v, want echo alone", names)
	}
	if result(t, responses[1])["isError"] != true {
		t.Fatalf("an authored tool without a runtime must be uncallable: %#v", responses[1])
	}
}
