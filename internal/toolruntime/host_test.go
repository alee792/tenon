package toolruntime

// Every bound and every lifecycle rule of the host protocol is proven here
// against fake hosts — shell scripts emitting canned lines — so the protocol
// is tested without any language toolchain, credential, or network.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// cannedHost answers the nth request line with the nth canned response,
// verbatim. Responses are files rather than arguments so a multi-megabyte
// catalog line is exactly as easy to emit as a short one.
func cannedHost(t *testing.T, responses ...string) *host {
	t.Helper()
	dir := t.TempDir()
	for i, response := range responses {
		name := filepath.Join(dir, strconv.Itoa(i+1))
		if err := os.WriteFile(name, []byte(response+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `i=0
while IFS= read -r line; do
  i=$((i+1))
  if [ -f "$1/$i" ]; then cat "$1/$i"; else printf '{"id":"","error":"exhausted"}\n'; fi
done
`
	return start(t, exec.Command("/bin/sh", "-c", script, "sh", dir))
}

// mirrorHost answers every request with a well-formed result echoing the
// request identifier, after a short pause. Concurrent callers therefore
// succeed only if the host saw one request at a time.
func mirrorHost(t *testing.T) *host {
	t.Helper()
	script := `while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  sleep 0.02
  printf '{"id":"%s","result":{"instanceId":"fake:1","output":{"ok":true}}}\n' "$id"
done
`
	return start(t, exec.Command("/bin/sh", "-c", script, "sh"))
}

func start(t *testing.T, cmd *exec.Cmd) *host {
	t.Helper()
	h, err := startHost("typescript", cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.close)
	return h
}

// padded renders one syntactically valid response line of at least size bytes.
// The padding rides in an ignored member, so the line is oversized without
// being malformed.
func padded(id, member string, size int) string {
	prefix := `{"id":"` + id + `","result":{"instanceId":"fake:1",` + member + `,"padding":"`
	suffix := `"}}`
	pad := size - len(prefix) - len(suffix)
	if pad < 0 {
		pad = 0
	}
	return prefix + strings.Repeat("a", pad) + suffix
}

// TestOversizedCallLineKillsTheHostWhileAnEqualCatalogSucceeds proves the two
// bounds are genuinely different: the same 128 KiB line is a legitimate
// catalog and a protocol violation as a call result.
func TestOversizedCallLineKillsTheHostWhileAnEqualCatalogSucceeds(t *testing.T) {
	const size = 128 << 10
	h := cannedHost(t,
		padded("typescript-1", `"tools":[]`, size),
		padded("typescript-2", `"output":{"ok":true}`, size))

	if _, err := h.catalog(5 * time.Second); err != nil {
		t.Fatalf("a %d byte catalog line is within its bound: %v", size, err)
	}

	_, err := h.invoke("shout-text", json.RawMessage(`{"text":"hi"}`), 5*time.Second)
	if err == nil {
		t.Fatalf("a %d byte call response exceeds the %d byte bound", size, MaxCallLineBytes)
	}
	if !strings.Contains(err.Error(), "response line over its bound") ||
		!strings.Contains(err.Error(), "terminated") {
		t.Fatalf("error = %q, want the bounded-response message naming the termination", err)
	}
	// The host is dead: every later call reports the same recorded cause.
	if _, again := h.invoke("shout-text", json.RawMessage(`{"text":"hi"}`), time.Second); again == nil ||
		again.Error() != err.Error() {
		t.Fatalf("a terminated host must keep reporting its cause: %v", again)
	}
}

// TestMismatchedIdentifierKillsTheHost proves a host that answers the wrong
// request has left the protocol.
func TestMismatchedIdentifierKillsTheHost(t *testing.T) {
	h := cannedHost(t, `{"id":"typescript-9","result":{"instanceId":"fake:1","tools":[]}}`)

	_, err := h.catalog(5 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "mismatched request identifier") {
		t.Fatalf("error = %v, want the mismatched-identifier termination", err)
	}
	if h.failure() == nil {
		t.Fatal("the host must be recorded as terminated")
	}
}

// TestUnparseableLineKillsTheHost proves a line tenon cannot parse is never
// partially interpreted.
func TestUnparseableLineKillsTheHost(t *testing.T) {
	h := cannedHost(t, `this is not json`)

	_, err := h.catalog(5 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "unparseable response line") {
		t.Fatalf("error = %v, want the unparseable-line termination", err)
	}
}

// TestMismatchedInstanceKillsTheHost proves the process behind the pipe must
// stay the one tenon inspected.
func TestMismatchedInstanceKillsTheHost(t *testing.T) {
	h := cannedHost(t,
		`{"id":"typescript-1","result":{"instanceId":"fake:1","tools":[]}}`,
		`{"id":"typescript-2","result":{"instanceId":"fake:2","output":{"ok":true}}}`)

	if _, err := h.catalog(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	_, err := h.invoke("shout-text", json.RawMessage(`{"text":"hi"}`), 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "different instance") {
		t.Fatalf("error = %v, want the changed-instance termination", err)
	}
}

// TestCallDeadlineTerminatesTheHost proves tenon does not wait on authored
// code it cannot interrupt.
func TestCallDeadlineTerminatesTheHost(t *testing.T) {
	h := start(t, exec.Command("/bin/sh", "-c", "sleep 30"))

	started := time.Now()
	_, err := h.invoke("slow-tool", json.RawMessage(`{"text":"hi"}`), 100*time.Millisecond)
	if err == nil || err.Error() != "tool call exceeded its deadline; language host terminated" {
		t.Fatalf("error = %v, want the deadline termination", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the deadline took %v to fire", elapsed)
	}
}

// TestHostStderrIsRetainedButNeverReported proves a host's own output is
// bounded, captured, and kept out of every message tenon produces.
func TestHostStderrIsRetainedButNeverReported(t *testing.T) {
	script := `printf 'CONSPICUOUS-STDERR\n' >&2
while IFS= read -r line; do printf 'not json\n'; done
`
	h := start(t, exec.Command("/bin/sh", "-c", script, "sh"))

	_, err := h.catalog(5 * time.Second)
	if err == nil {
		t.Fatal("expected the unparseable line to fail")
	}
	if strings.Contains(err.Error(), "CONSPICUOUS") {
		t.Fatalf("a host error must never carry host output: %q", err)
	}
	// The bytes are captured, bounded, and go nowhere else.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(h.stderr.snapshot(), "CONSPICUOUS-STDERR") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(h.stderr.snapshot(), "CONSPICUOUS-STDERR") {
		t.Fatalf("host stderr must be retained in the bounded ring: %q", h.stderr.snapshot())
	}
}

// TestStderrRingStaysBounded proves the retained tail never grows past its
// ceiling however much a host writes.
func TestStderrRingStaysBounded(t *testing.T) {
	r := &ring{}
	for i := 0; i < 8; i++ {
		if _, err := r.Write([]byte(strings.Repeat("x", maxStderrBytes))); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.snapshot()) != maxStderrBytes {
		t.Fatalf("retained %d bytes, want the %d byte bound", len(r.snapshot()), maxStderrBytes)
	}
}

// TestCallsAreSerialized proves one host serves one call at a time: the fake
// mirrors request identifiers back, so an interleaved exchange would be
// answered with a mismatched identifier and kill the host.
func TestCallsAreSerialized(t *testing.T) {
	h := mirrorHost(t)

	var wait sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := h.invoke("shout-text", json.RawMessage(`{"text":"hi"}`), 10*time.Second); err != nil {
				failures <- err
			}
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("concurrent calls must serialize, not interleave: %v", err)
	}
	if h.seq != 8 {
		t.Fatalf("the host served %d requests, want 8", h.seq)
	}
}

// TestArgumentsAndOutputAreBounded proves both directions are held to the same
// non-empty, bounded, valid-JSON contract before anything crosses.
func TestArgumentsAndOutputAreBounded(t *testing.T) {
	h := mirrorHost(t)

	for name, arguments := range map[string]string{
		"empty":     "",
		"not json":  "{",
		"oversized": `{"text":"` + strings.Repeat("a", MaxArgumentBytes) + `"}`,
	} {
		if _, err := h.invoke("shout-text", json.RawMessage(arguments), time.Second); err == nil {
			t.Fatalf("%s arguments must be refused", name)
		}
	}
	if h.seq != 0 {
		t.Fatal("refused arguments must never reach the host")
	}

	// Output is held to the same contract: an empty output is not a result.
	empty := cannedHost(t, `{"id":"typescript-1","result":{"instanceId":"fake:1"}}`)
	if _, err := empty.invoke("shout-text", json.RawMessage(`{"text":"hi"}`), time.Second); err == nil ||
		!strings.Contains(err.Error(), "tool output") {
		t.Fatalf("error = %v, want the missing-output refusal", err)
	}
}

// TestHostErrorsAreReportedInTheHostsOwnWords proves an in-band error is
// passed through bounded rather than terminating the host: a tool that
// rejected its arguments is a working host.
func TestHostErrorsAreReportedInTheHostsOwnWords(t *testing.T) {
	long := strings.Repeat("z", 4096)
	h := cannedHost(t,
		`{"id":"typescript-1","error":"tools/shout_text.ts must export a default object"}`,
		`{"id":"typescript-2","error":"`+long+`"}`)

	_, err := h.catalog(5 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "tools/shout_text.ts must export") {
		t.Fatalf("error = %v, want the host's own sentence", err)
	}
	if h.failure() != nil {
		t.Fatal("an in-band error must not terminate the host")
	}
	_, err = h.invoke("shout-text", json.RawMessage(`{"text":"hi"}`), 5*time.Second)
	if err == nil || len(err.Error()) > maxMessageChars+3 {
		t.Fatalf("a host message must be bounded: %d bytes", len(err.Error()))
	}
}

// TestCloseStopsAHostThatIgnoresItsStdin proves the two-second grace period is
// followed by a kill, so a session never leaks a language host.
func TestCloseStopsAHostThatIgnoresItsStdin(t *testing.T) {
	h := start(t, exec.Command("/bin/sh", "-c", "trap '' TERM; sleep 60"))

	done := make(chan struct{})
	go func() {
		h.close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("close must not wait on a host that ignores its stdin")
	}
}
