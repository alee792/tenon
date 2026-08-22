package mcp

import (
	"bytes"
	"context"
	"encoding/json"
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
